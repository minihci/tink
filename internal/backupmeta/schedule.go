package backupmeta

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// NextRun returns the first time after `after` at which schedule fires. schedule is anything
// ValidateSchedule accepts: a 5-field cron expression, or a comma-separated list of @aliases
// (Incus's own syntax), in which case it is the earliest of them. It is evaluated in the location of
// `after`, which is how Incus evaluates a snapshot schedule: in the server's local time.
func NextRun(schedule string, after time.Time) (time.Time, error) {
	schedule = strings.TrimSpace(schedule)
	if err := ValidateSchedule(schedule); err != nil {
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
	if err := ValidateRetain(expr); err != nil {
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

// CopyWarnings says so when a declared copy has never run, is failing, or is overdue by its own schedule. It
// needs the volume's live config (where the stamps are), so it only speaks about volumes that exist.
func CopyWarnings(name string, b *VolumeBackup, current map[string]string, now time.Time) []string {
	if b == nil || b.None != "" {
		return nil
	}
	var out []string
	for _, c := range b.Copies {
		fail, failing := FailureOf(current, c.Target)
		failingNote := ""
		if failing {
			failingNote = fmt.Sprintf("%d attempt(s) in a row have failed, the last %s ago", fail.N, humanAge(now.Sub(fail.At)))
		}
		stamp := current[CopyStampAt(c.Target)]
		if stamp == "" {
			if failing {
				out = append(out, fmt.Sprintf("the copy to %s has never succeeded: %s -- `tink backup run %s` shows why", c.Target, failingNote, name))
			} else {
				out = append(out, fmt.Sprintf("the copy to %s has never run -- `tink backup run %s`", c.Target, name))
			}
			continue
		}
		last, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			out = append(out, fmt.Sprintf("%s=%q is not a timestamp tink wrote -- `tink backup run %s` replaces it", CopyStampAt(c.Target), stamp, name))
			continue
		}
		if failing {
			out = append(out, fmt.Sprintf("the copy to %s is failing: %s -- `tink backup run %s` shows why", c.Target, failingNote, name))
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
				c.Target, humanAge(now.Sub(last)), c.Schedule, due.Format("2006-01-02 15:04"), name))
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
