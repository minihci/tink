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

// Markers `tink backup run` puts on each restore-point volume it creates on a target. Pruning and
// restoring touch only volumes that carry the marker for the volume in question, so a volume tink did
// not make is never deleted and never mistaken for a backup.
const (
	MarkerCopyOf     = "user.tink.backup.copy-of"     // "<project>/<pool>/<volume>"
	MarkerCopyAt     = "user.tink.backup.copy-at"     // RFC 3339, UTC
	MarkerCopyTarget = "user.tink.backup.copy-target" // the backup-target's name
	MarkerCopySnap   = "user.tink.backup.copy-snapshot"
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

// copyWarnings says so when a declared copy has never run, or is overdue by its own schedule. It
// needs the volume's live config (where the stamps are), so it only speaks about volumes that exist.
func copyWarnings(r Resource, current map[string]string, now time.Time) []string {
	if r.Backup == nil || r.Backup.None != "" {
		return nil
	}
	var out []string
	for _, c := range r.Backup.Copies {
		stamp := current[CopyStampAt(c.Target)]
		if stamp == "" {
			out = append(out, fmt.Sprintf("the copy to %s has never run -- `tink backup run %s`", c.Target, r.Name))
			continue
		}
		last, err := time.Parse(time.RFC3339, stamp)
		if err != nil {
			out = append(out, fmt.Sprintf("%s=%q is not a timestamp tink wrote -- `tink backup run %s` replaces it", CopyStampAt(c.Target), stamp, r.Name))
			continue
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

// CopyIsDue reports whether the copy to target should run at `now` by its schedule, given the volume's
// live config: never run, or the first scheduled time after the last success has passed.
func CopyIsDue(schedule string, current map[string]string, target string, now time.Time) (bool, error) {
	stamp := current[CopyStampAt(target)]
	if stamp == "" {
		return true, nil
	}
	last, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return true, nil // a stamp we cannot read is as good as none
	}
	due, err := NextRun(schedule, last.In(now.Location()))
	if err != nil {
		return false, err
	}
	return !now.Before(due), nil
}
