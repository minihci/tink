package resolve

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// NextRun returns the first time after `after` at which schedule fires. schedule is anything
// validateSchedule accepts: a 5-field cron expression, or a comma-separated list of @aliases
// (Incus's own syntax), in which case it is the earliest of them. It is evaluated in the location of
// `after`, which is how Incus evaluates a snapshot schedule: in the server's local time.
func NextRun(schedule string, after time.Time) (time.Time, error) {
	schedule = strings.TrimSpace(schedule)
	if err := validateSchedule(schedule); err != nil {
		return time.Time{}, err
	}
	specs := []string{schedule}
	if strings.HasPrefix(schedule, "@") {
		specs = strings.Split(schedule, ",")
	}
	var next time.Time
	for _, s := range specs {
		sch, err := cron.ParseStandard(strings.TrimSpace(s))
		if err != nil {
			return time.Time{}, fmt.Errorf("schedule %q: %w", s, err)
		}
		if t := sch.Next(after); next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next, nil
}

// ExpiryAfter returns the time an Incus expiry expression ("14d", "1w 3d", "6m") reaches counting
// from `from`, with Incus's own arithmetic (years, months and days by calendar, the rest by duration).
func ExpiryAfter(from time.Time, expr string) (time.Time, error) {
	if err := validateRetain(expr); err != nil {
		return time.Time{}, err
	}
	var y, m, d int
	var dur time.Duration
	for _, f := range strings.Fields(expr) {
		var n int
		fmt.Sscanf(f, "%d", &n)
		switch f[len(f)-1] {
		case 'y':
			y += n
		case 'm':
			m += n
		case 'w':
			d += 7 * n
		case 'd':
			d += n
		case 'H':
			dur += time.Duration(n) * time.Hour
		case 'M':
			dur += time.Duration(n) * time.Minute
		case 'S':
			dur += time.Duration(n) * time.Second
		}
	}
	return from.AddDate(y, m, d).Add(dur), nil
}

// The state `tink backup run` leaves on the SOURCE volume for each copy target, as Incus volume
// config (no state file of tink's own), so `plan` can see whether copies are happening.
const (
	copyStampPrefix = "user.tink.backup.copy."
)

// CopyStampAt is the config key holding when the last copy to target succeeded (RFC 3339, UTC).
func CopyStampAt(target string) string { return copyStampPrefix + target + ".at" }

// CopyStampVolume is the config key naming the newest restore point on target.
func CopyStampVolume(target string) string { return copyStampPrefix + target + ".volume" }

// CopyFailAt and CopyFailCount are the config keys recording that the copy to target is failing: when the last
// attempt failed (RFC 3339, UTC) and how many attempts in a row have. A success removes both. They hold no
// message on purpose: errors from Incus and its drivers can echo credentials (a TrueNAS API key has appeared in
// one), and a volume's config is the wrong place to keep that. The reason goes in the run's own output.
func CopyFailAt(target string) string    { return copyStampPrefix + target + ".fail.at" }
func CopyFailCount(target string) string { return copyStampPrefix + target + ".fail.n" }

// FirstRetryDelay is how long to wait after the first failure; each further consecutive failure doubles it,
// up to the copy's own interval (retrying more often than the schedule would is the thing to avoid).
const FirstRetryDelay = 5 * time.Minute

// CopyFailure is a failing copy: the last attempt's time and the consecutive failures so far.
type CopyFailure struct {
	At time.Time
	N  int
}

// FailureOf reads the failure state of the copy to target from the volume's live config. It is false when the
// copy is not failing: no stamp, an unreadable one, or one older than the last success (a stale stamp a success
// should have cleared, never believed over a newer success).
func FailureOf(current map[string]string, target string) (CopyFailure, bool) {
	at, err := time.Parse(time.RFC3339, current[CopyFailAt(target)])
	if err != nil {
		return CopyFailure{}, false
	}
	var n int
	if _, err := fmt.Sscanf(current[CopyFailCount(target)], "%d", &n); err != nil || n < 1 {
		n = 1
	}
	if last, err := time.Parse(time.RFC3339, current[CopyStampAt(target)]); err == nil && !at.After(last) {
		return CopyFailure{}, false
	}
	return CopyFailure{At: at, N: n}, true
}

// RetryAfter is when a failing copy may be tried again: FirstRetryDelay doubled for every failure after the
// first, never longer than interval (the copy's own gap between scheduled runs), so a copy that fails is
// retried more slowly than a healthy one but never more slowly than its schedule.
func RetryAfter(f CopyFailure, interval time.Duration) time.Time {
	delay := FirstRetryDelay
	for i := 1; i < f.N && delay < interval; i++ {
		delay *= 2
	}
	if interval > 0 && delay > interval {
		delay = interval
	}
	return f.At.Add(delay)
}

// scheduleInterval is the gap between two consecutive scheduled runs, measured at `at`.
func scheduleInterval(schedule string, at time.Time) (time.Duration, error) {
	first, err := NextRun(schedule, at)
	if err != nil {
		return 0, err
	}
	second, err := NextRun(schedule, first)
	if err != nil {
		return 0, err
	}
	return second.Sub(first), nil
}

// Markers `tink backup run` puts on each restore-point volume it creates on a target. Pruning and
// restoring touch only volumes that carry the marker for the volume in question, so a volume tink did
// not make is never deleted and never mistaken for a backup.
const (
	MarkerCopyOf     = "user.tink.backup.copy-of"     // "<project>/<pool>/<volume>"
	MarkerCopyAt     = "user.tink.backup.copy-at"     // RFC 3339, UTC
	MarkerCopyTarget = "user.tink.backup.copy-target" // the backup-target's name
	MarkerCopySnap   = "user.tink.backup.copy-snapshot"
	// MarkerCopyServer names the Incus server the SOURCE volume lives on (its server_name, the host name by default).
	// Pruning only ever removes restore points made by the server doing the pruning, so two servers copying a
	// volume with the same name into one target cannot delete each other's backups. Restore sees every point.
	MarkerCopyServer = "user.tink.backup.copy-server"
)

// CopyOf is the value of MarkerCopyOf for a volume.
func CopyOf(project, pool, volume string) string {
	if project == "" {
		project = "default"
	}
	if pool == "" {
		pool = "default"
	}
	return project + "/" + pool + "/" + volume
}

// copyWarnings says so when a declared copy has never run, is failing, or is overdue by its own schedule. It
// needs the volume's live config (where the stamps are), so it only speaks about volumes that exist.
func copyWarnings(r Resource, current map[string]string, now time.Time) []string {
	if r.Backup == nil || r.Backup.None != "" {
		return nil
	}
	var out []string
	for _, c := range r.Backup.Copies {
		fail, failing := FailureOf(current, c.Target)
		failingNote := ""
		if failing {
			failingNote = fmt.Sprintf("%d attempt(s) in a row have failed, the last %s ago", fail.N, humanAge(now.Sub(fail.At)))
		}
		stamp := current[CopyStampAt(c.Target)]
		if stamp == "" {
			if failing {
				out = append(out, fmt.Sprintf("the copy to %s has never succeeded: %s -- `tink backup run %s` shows why", c.Target, failingNote, r.Name))
			} else {
				out = append(out, fmt.Sprintf("the copy to %s has never run -- `tink backup run %s`", c.Target, r.Name))
			}
			continue
		}
		last, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			out = append(out, fmt.Sprintf("%s=%q is not a timestamp tink wrote -- `tink backup run %s` replaces it", CopyStampAt(c.Target), stamp, r.Name))
			continue
		}
		if failing {
			out = append(out, fmt.Sprintf("the copy to %s is failing: %s -- `tink backup run %s` shows why", c.Target, failingNote, r.Name))
		}
		due, err := NextRun(c.Schedule, last.In(now.Location()))
		if err != nil {
			continue // a bad schedule is rejected at load; nothing sensible to say here
		}
		interval := due.Sub(last)
		if next2, err := NextRun(c.Schedule, due); err == nil {
			interval = next2.Sub(due)
		}
		grace := interval / 4
		if grace < 10*time.Minute {
			grace = 10 * time.Minute
		}
		if grace > 6*time.Hour {
			grace = 6 * time.Hour
		}
		if now.After(due.Add(grace)) {
			out = append(out, fmt.Sprintf("the copy to %s is overdue: last ran %s ago, and its schedule %q was due at %s -- `tink backup run %s` (or run `tink backup run --due` from cron)",
				c.Target, humanAge(now.Sub(last)), c.Schedule, due.Format("2006-01-02 15:04"), r.Name))
		}
	}
	return out
}

// CopyDecision is whether the copy to a target should run now, and, when it should not, why.
type CopyDecision struct {
	Due bool
	// Reason is set when Due is false: "not yet due" or the backoff after failures.
	Reason string
	// Failure is the failure state, when the copy is failing.
	Failure *CopyFailure
	// RetryAt is when a failing copy may next be tried (zero if it is not failing).
	RetryAt time.Time
}

// CopyDue decides whether the copy to target should run at `now`, given the volume's live config: it must be
// due by its schedule (never run, or the first scheduled time after the last success has passed), and, if the
// last attempts failed, the backoff after them must have elapsed.
func CopyDue(schedule string, current map[string]string, target string, now time.Time) (CopyDecision, error) {
	d := CopyDecision{Due: true}
	if stamp := current[CopyStampAt(target)]; stamp != "" {
		if last, err := time.Parse(time.RFC3339, stamp); err == nil { // a stamp we cannot read is as good as none
			due, err := NextRun(schedule, last.In(now.Location()))
			if err != nil {
				return CopyDecision{}, err
			}
			if now.Before(due) {
				return CopyDecision{Reason: "not yet due, next at " + clock(due, now)}, nil
			}
		}
	}
	if fail, failing := FailureOf(current, target); failing {
		interval, err := scheduleInterval(schedule, fail.At.In(now.Location()))
		if err != nil {
			return CopyDecision{}, err
		}
		d.Failure, d.RetryAt = &fail, RetryAfter(fail, interval)
		if now.Before(d.RetryAt) {
			d.Due = false
			d.Reason = fmt.Sprintf("backing off after %d failed attempt(s), next try after %s", fail.N, clock(d.RetryAt, now))
		}
	}
	return d, nil
}

// clock formats t in the zone of now, with the zone's name, so every time a message shows is in the same zone
// and says which.
func clock(t, now time.Time) string { return t.In(now.Location()).Format("2006-01-02 15:04 MST") }

// CopyIsDue reports whether the copy to target should run at `now`; see CopyDue.
func CopyIsDue(schedule string, current map[string]string, target string, now time.Time) (bool, error) {
	d, err := CopyDue(schedule, current, target, now)
	return d.Due, err
}
