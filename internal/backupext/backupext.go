// Package backupext is the one seam between the converger (internal/resolve) and the backup capability (internal/backupmeta). It imports nothing
// of tink's, so each side can import it and neither imports the other: the converger holds the SHAPE of a storage volume's backup block and asks
// a registered Backup for everything that follows from it; the capability registers itself.
//
// Of the three extension points (docs/roadmap.md, "Three extension points"), backup uses two: a PASS that derives a volume's config from its
// declaration (SnapshotConfig, Policy, and the Keys the converger stamps and reserves), and CHECKS that return findings about it (Validate when a
// stack is loaded, CopyWarnings and VerifyWarning when it is planned). Backup is not a kind of its own: the kind is storage-volume, which the
// converger owns, and the block is a field of it.
//
// What lives here, and why: the declaration types (VolumeBackup and its parts) because they are the schema of a field of the storage-volume kind,
// which the converger decodes and carries; the Target the converger resolves a copy's backup-target to; and the registry, which holds one Backup
// (a program has one backup capability, so this is a slot and not a plugin framework). backupmeta re-exports the declaration types under their
// old names, so nothing that already says backupmeta.VolumeBackup changes.
//
// Wiring: backupmeta registers in its init, so a program gets backup by linking backupmeta (cmd/tink does, in capabilities.go; a test that plans
// storage volumes imports it for the same reason). Get panics when nothing is registered, because a converger that silently skipped the backup
// rules would plan a volume as safe that it never checked.
package backupext

import (
	"sync"
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

// BackupCopy is one replica of the volume: where (Target names a
// kind: backup-target), how often, and how long its snapshots are kept there.
type BackupCopy struct {
	Target   string
	Schedule string // same syntax as SnapshotPolicy.Schedule
	Retain   string // same syntax as SnapshotPolicy.Retain
}

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

// Target is a kind: backup-target as a copy's policy needs it: the fields of the resolver's resource that a reader of the policy cannot look up
// in the stack.
type Target struct {
	Name, Location, Engine, Remote, Pool string
}

// Keys are the volume config keys the backup capability owns. The converger needs their names to stamp, to reserve them from a volume's own
// config, to leave the policy out of a raw diff, and to read a live volume back into a declaration.
type Keys struct {
	// SnapshotSchedule and SnapshotExpiry are Incus's own snapshot keys, which a snapshots block converges.
	SnapshotSchedule, SnapshotExpiry string
	// Policy is the resolved copy policy a volume carries.
	Policy string
	// Stack names the stack that applied the volume.
	Stack string
	// Prefix starts every other key the capability writes (stamps and markers): state, not declaration.
	Prefix string
}

// Backup is what the converger asks of the backup capability. Every method is a function of what it is given: none reads the server or the stack.
type Backup interface {
	// Keys names the config keys the capability owns.
	Keys() Keys
	// PolicyProto is the protocol version of the copy policy this build writes.
	PolicyProto() int

	// Validate rejects a malformed backup block (a check, at load). A nil block is not an error: it is a plan-time warning, which the converger gives.
	Validate(volume string, b *VolumeBackup) error

	// SnapshotConfig is the Incus volume config a snapshots block converges to; nil when there is none.
	SnapshotConfig(b *VolumeBackup) map[string]string
	// Policy resolves the block into the copy policy text the volume should carry, copies named in targets. "" means the volume carries none.
	Policy(b *VolumeBackup, targets map[string]Target) (string, error)
	// DescribePolicyChange puts the change from one stored policy to another in words, for plan. Either may be "".
	DescribePolicyChange(have, want string) []string

	// CopyWarnings and VerifyWarning are the plan-time findings about an existing volume's copies and its restore rehearsal (checks).
	CopyWarnings(volume string, b *VolumeBackup, current map[string]string, now time.Time) []string
	VerifyWarning(volume string, b *VolumeBackup, current map[string]string, now time.Time) string
}

var (
	mu   sync.RWMutex
	impl Backup
)

// Register installs the backup capability. The capability's package calls it from init; a second call replaces the first (a test may).
func Register(b Backup) {
	mu.Lock()
	defer mu.Unlock()
	impl = b
}

// Get is the registered backup capability. It panics when there is none: see the package comment.
func Get() Backup {
	mu.RLock()
	defer mu.RUnlock()
	if impl == nil {
		panic("backupext: no backup capability registered (import internal/backupmeta, which registers it)")
	}
	return impl
}
