package backupmeta

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// VolumeBackup is a storage-volume's answer to "how is this backed up?".
// Every custom volume should give one: either a snapshot policy, or an explicit
// opt-out with a reason. A volume that says nothing gets a warning from plan --
// the point is to force the backup question at authoring time, not to discover
// at restore time that nobody asked it. The warning is a stepping stone: it
// exists so stacks written before this field keep applying, and is meant to
// become an error (BLOCKED, like image drift) once the feature has matured.
//
// Tier 1 only: local, Incus-native snapshots. Same pool, same disk, so this
// protects against mistakes (a bad upgrade, a deleted file), not against
// losing the disk. See docs/volume-backup.md.
type VolumeBackup struct {
	// Snapshots is a scheduled-snapshot policy (tier 1: a rollback aid on the
	// live pool, not a copy). Mutually exclusive with None.
	Snapshots *SnapshotPolicy
	// Copies are independent replicas in other failure domains, each to a
	// kind: backup-target. Mutually exclusive with None. Declared and checked
	// against 3-2-1 by plan; nothing executes them yet.
	Copies []BackupCopy
	// Verify is how often a restore of the backups should be rehearsed:
	// daily, weekly or monthly. `tink backup verify` does the rehearsal and
	// stamps the volume; `plan` warns when the stamp is missing or older than
	// this.
	Verify string
	// VerifyCheck is what `tink backup verify` runs against the restored data,
	// beyond proving the snapshot can be restored at all.
	VerifyCheck *VerifyCheck
	// None is the reason this volume is deliberately not backed up. A
	// non-empty string is the opt-out: a bare "none" with no reason would be
	// indistinguishable from not having thought about it.
	None string
}

// VerifyCheck runs inside a throwaway instance with the restored volume mounted
// read-only. Exit status 0 means the data is good.
type VerifyCheck struct {
	// Image is an OCI image (e.g. docker-oci:library/alpine:3) that has `sleep`: the
	// throwaway instance's entrypoint is replaced with a sleep so it can be exec'd into.
	Image string
	// Command is argv, run inside the instance. No shell unless you ask for one.
	Command []string
	// Mount is where the restored volume appears. Defaults to /data.
	Mount string
}

// DefaultVerifyMount is where a verify check sees the restored volume.
const DefaultVerifyMount = "/data"

// BackupCopy is one replica of the volume: where (Target names a
// kind: backup-target), how often, and how long its snapshots are kept there.
type BackupCopy struct {
	Target   string
	Schedule string // same syntax as SnapshotPolicy.Schedule
	Retain   string // same syntax as SnapshotPolicy.Retain
}

var verifyCadences = map[string]bool{"daily": true, "weekly": true, "monthly": true}

// IsVerifyCadence reports whether c is a cadence a volume may declare for rehearsing a restore: daily, weekly or monthly.
func IsVerifyCadence(c string) bool { return verifyCadences[c] }

// SnapshotPolicy maps one-to-one onto Incus's own snapshots.schedule and
// snapshots.expiry volume keys, so Incus does the work and tink only converges
// the config -- no daemon, no state of its own.
type SnapshotPolicy struct {
	// Schedule is a cron expression (5 fields) or a comma-separated list of
	// @hourly/@daily/@midnight/@weekly/@monthly/@annually/@yearly.
	Schedule string
	// Retain is how long a snapshot lives, in Incus's expiry syntax
	// ("14d", "1w 3d", "6m"). Required: a schedule with no expiry fills the
	// pool forever.
	Retain string
}

const (
	KeySnapshotSchedule = "snapshots.schedule"
	KeySnapshotExpiry   = "snapshots.expiry"
)

var (
	// One Incus expiry field: units are S|M|H|d|w|m|y. Zero is excluded --
	// Incus treats a zero expiry as "never expires", the opposite of retaining
	// for a bounded time.
	expiryFieldRe   = regexp.MustCompile(`^([1-9][0-9]*)(S|M|H|d|w|m|y)$`)
	scheduleAliases = map[string]bool{"@hourly": true, "@daily": true, "@midnight": true, "@weekly": true, "@monthly": true, "@annually": true, "@yearly": true}
)

const cronFields = 5

// ValidateBackup rejects a malformed backup block at load time. A missing block
// is NOT an error here: it is a plan-time warning (see decideVolume).
//
// The checks run in the order the errors are reported in: what the block is (a block that is wrong about that has nothing else
// worth saying), then verification, then copies, then snapshots.
func ValidateBackup(name string, b *VolumeBackup) error {
	if b == nil {
		return nil
	}
	for _, check := range []func(string, *VolumeBackup) error{validateShape, validateVerify, validateCopies, validateSnapshots} {
		if err := check(name, b); err != nil {
			return err
		}
	}
	return nil
}

// validateShape checks what the block says it is: none (with a reason) or snapshots and/or copies, never both.
func validateShape(name string, b *VolumeBackup) error {
	hasSnap, hasCopies, hasNone := b.Snapshots != nil, len(b.Copies) > 0, b.None != ""
	switch {
	case hasNone && (hasSnap || hasCopies || b.Verify != "" || b.VerifyCheck != nil):
		return fmt.Errorf("resource %q: backup: none is mutually exclusive with snapshots, copies and verify", name)
	case !hasSnap && !hasCopies && !hasNone:
		return fmt.Errorf("resource %q: backup: give snapshots (schedule + retain) and/or copies, or none (the reason this volume is not backed up)", name)
	case hasNone && strings.TrimSpace(b.None) == "":
		return fmt.Errorf("resource %q: backup.none needs a reason, not whitespace", name)
	}
	return nil
}

// validateVerify checks how often a restore is rehearsed and the check that decides whether the restored data is good.
func validateVerify(name string, b *VolumeBackup) error {
	if b.Verify != "" && !verifyCadences[b.Verify] {
		return fmt.Errorf("resource %q: backup.verify must be daily, weekly or monthly, got %q", name, b.Verify)
	}
	if c := b.VerifyCheck; c != nil {
		if (b.Snapshots == nil && len(b.Copies) == 0) || strings.TrimSpace(c.Image) == "" || len(c.Command) == 0 {
			return fmt.Errorf("resource %q: backup.verify.check needs image and command, and something to restore (snapshots or copies)", name)
		}
		if c.Mount != "" && !strings.HasPrefix(c.Mount, "/") {
			return fmt.Errorf("resource %q: backup.verify.check.mount must be an absolute path, got %q", name, c.Mount)
		}
	}
	return nil
}

// validateCopies checks each copy: it names a target, once, and can be scheduled and expired.
func validateCopies(name string, b *VolumeBackup) error {
	seen := map[string]bool{}
	for i, c := range b.Copies {
		if c.Target == "" {
			return fmt.Errorf("resource %q: backup.copies[%d]: target is required", name, i)
		}
		if seen[c.Target] {
			return fmt.Errorf("resource %q: backup.copies names target %q twice; one copy per target", name, c.Target)
		}
		seen[c.Target] = true
		if err := ValidateSchedule(c.Schedule); err != nil {
			return fmt.Errorf("resource %q: backup.copies[%d] (%s) schedule: %w", name, i, c.Target, err)
		}
		if err := ValidateRetain(c.Retain); err != nil {
			return fmt.Errorf("resource %q: backup.copies[%d] (%s) retain: %w", name, i, c.Target, err)
		}
	}
	return nil
}

// validateSnapshots checks the Incus snapshot schedule and expiry the block maps to.
func validateSnapshots(name string, b *VolumeBackup) error {
	if b.Snapshots == nil {
		return nil
	}
	if err := ValidateSchedule(b.Snapshots.Schedule); err != nil {
		return fmt.Errorf("resource %q: backup.snapshots.schedule: %w", name, err)
	}
	if err := ValidateRetain(b.Snapshots.Retain); err != nil {
		return fmt.Errorf("resource %q: backup.snapshots.retain: %w", name, err)
	}
	return nil
}

func ValidateSchedule(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("required")
	}
	if strings.HasPrefix(s, "@") {
		for _, a := range strings.Split(s, ",") {
			if !scheduleAliases[strings.TrimSpace(a)] {
				return fmt.Errorf("%q is not one of @hourly, @daily, @midnight, @weekly, @monthly, @annually, @yearly", strings.TrimSpace(a))
			}
		}
		return nil
	}
	if n := len(strings.Fields(s)); n != cronFields {
		return fmt.Errorf("%q is neither a list of @aliases nor a %d-field cron expression", s, cronFields)
	}
	return nil
}

func ValidateRetain(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("required: a schedule with no expiry keeps snapshots forever and eventually fills the pool")
	}
	seen := map[string]bool{}
	for _, f := range strings.Split(s, " ") {
		m := expiryFieldRe.FindStringSubmatch(f)
		if m == nil {
			return fmt.Errorf("%q is not valid Incus expiry syntax: space-separated <positive integer><unit> fields, units S|M|H|d|w|m|y (e.g. \"14d\" or \"1w 3d\")", s)
		}
		if seen[m[2]] {
			return fmt.Errorf("%q repeats the unit %q", s, m[2])
		}
		seen[m[2]] = true
	}
	return nil
}

// SnapshotConfig is the Incus volume config a snapshot policy converges
// to. Nil for no policy (no block, or an opt-out): tink only ever sets keys it
// owns, matching diffConfig's one-directional rule.
func SnapshotConfig(b *VolumeBackup) map[string]string {
	if b == nil || b.Snapshots == nil {
		return nil
	}
	return map[string]string{
		KeySnapshotSchedule: strings.TrimSpace(b.Snapshots.Schedule),
		KeySnapshotExpiry:   strings.TrimSpace(b.Snapshots.Retain),
	}
}

var verifyCadenceAge = map[string]time.Duration{
	"daily":   24 * time.Hour,
	"weekly":  7 * 24 * time.Hour,
	"monthly": 31 * 24 * time.Hour,
}

// VerifyWarning says so when a volume declares a verify cadence but its last
// successful verification (the stamp `tink backup verify` leaves) is missing or
// older than the cadence. "Untested backup" becomes a visible state of the stack
// instead of something found out during an outage. No cadence declared, no nag.
func VerifyWarning(name string, b *VolumeBackup, current map[string]string, now time.Time) string {
	if b == nil || b.None != "" || b.Verify == "" {
		return ""
	}
	maxAge := verifyCadenceAge[b.Verify]
	stamp := current[StampVerifiedAt]
	if stamp == "" {
		return fmt.Sprintf("verify: %s is declared but this volume has never been verified -- run `tink backup verify %s`", b.Verify, name)
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return fmt.Sprintf("%s=%q is not a timestamp tink wrote -- run `tink backup verify %s` to replace it", StampVerifiedAt, stamp, name)
	}
	// A restore-only verification does not satisfy a declared check: otherwise running verify from
	// somewhere the stack file is not found would make the volume look freshly verified while the
	// check that matters never ran.
	if b.VerifyCheck != nil && current[StampVerifiedWith] != "check" {
		return fmt.Sprintf("a verify check is declared, but the last verification only proved the snapshot restores -- run `tink backup verify %s` with the stack file so the check runs", name)
	}
	if age := now.Sub(at); age > maxAge {
		return fmt.Sprintf("last verified %s ago, older than the declared verify: %s -- run `tink backup verify %s`", humanAge(age), b.Verify, name)
	}
	return ""
}

func humanAge(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
