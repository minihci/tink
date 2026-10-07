package resolve

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
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

// The state `tink backup verify` leaves on the SOURCE volume as Incus config, so
// `plan` can see it without tink keeping a state file of its own (the same idea as
// user.ingress.*). Written only when a verification passes: a failed one must not
// look fresh.
const (
	StampVerifiedAt       = "user.tink.backup.verified-at"       // RFC 3339, UTC
	StampVerifiedSnapshot = "user.tink.backup.verified-snapshot" // which snapshot was restored
	StampVerifiedWith     = "user.tink.backup.verified-with"     // "check" or "restore"
	StampVerifiedFrom     = "user.tink.backup.verified-from"     // "local" (a snapshot on the volume's pool) or a backup-target's name
)

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
	volKeySnapshotSchedule = "snapshots.schedule"
	volKeySnapshotExpiry   = "snapshots.expiry"
)

var (
	// One Incus expiry field: units are S|M|H|d|w|m|y. Zero is excluded --
	// Incus treats a zero expiry as "never expires", the opposite of retaining
	// for a bounded time.
	expiryFieldRe   = regexp.MustCompile(`^([1-9][0-9]*)(S|M|H|d|w|m|y)$`)
	scheduleAliases = map[string]bool{"@hourly": true, "@daily": true, "@midnight": true, "@weekly": true, "@monthly": true, "@annually": true, "@yearly": true}
)

const cronFields = 5

// validateBackup rejects a malformed backup block at load time. A missing block
// is NOT an error here: it is a plan-time warning (see decideVolume).
func validateBackup(r Resource) error {
	b := r.Backup
	if b == nil {
		return nil
	}
	hasSnap, hasCopies, hasNone := b.Snapshots != nil, len(b.Copies) > 0, b.None != ""
	switch {
	case hasNone && (hasSnap || hasCopies || b.Verify != "" || b.VerifyCheck != nil):
		return fmt.Errorf("resource %q: backup: none is mutually exclusive with snapshots, copies and verify", r.Name)
	case !hasSnap && !hasCopies && !hasNone:
		return fmt.Errorf("resource %q: backup: give snapshots (schedule + retain) and/or copies, or none (the reason this volume is not backed up)", r.Name)
	case hasNone && strings.TrimSpace(b.None) == "":
		return fmt.Errorf("resource %q: backup.none needs a reason, not whitespace", r.Name)
	}
	if b.Verify != "" && !verifyCadences[b.Verify] {
		return fmt.Errorf("resource %q: backup.verify must be daily, weekly or monthly, got %q", r.Name, b.Verify)
	}
	if c := b.VerifyCheck; c != nil {
		if (!hasSnap && !hasCopies) || strings.TrimSpace(c.Image) == "" || len(c.Command) == 0 {
			return fmt.Errorf("resource %q: backup.verify.check needs image and command, and something to restore (snapshots or copies)", r.Name)
		}
		if c.Mount != "" && !strings.HasPrefix(c.Mount, "/") {
			return fmt.Errorf("resource %q: backup.verify.check.mount must be an absolute path, got %q", r.Name, c.Mount)
		}
	}
	seen := map[string]bool{}
	for i, c := range b.Copies {
		if c.Target == "" {
			return fmt.Errorf("resource %q: backup.copies[%d]: target is required", r.Name, i)
		}
		if seen[c.Target] {
			return fmt.Errorf("resource %q: backup.copies names target %q twice; one copy per target", r.Name, c.Target)
		}
		seen[c.Target] = true
		if err := validateSchedule(c.Schedule); err != nil {
			return fmt.Errorf("resource %q: backup.copies[%d] (%s) schedule: %w", r.Name, i, c.Target, err)
		}
		if err := validateRetain(c.Retain); err != nil {
			return fmt.Errorf("resource %q: backup.copies[%d] (%s) retain: %w", r.Name, i, c.Target, err)
		}
	}
	if hasSnap {
		if err := validateSchedule(b.Snapshots.Schedule); err != nil {
			return fmt.Errorf("resource %q: backup.snapshots.schedule: %w", r.Name, err)
		}
		if err := validateRetain(b.Snapshots.Retain); err != nil {
			return fmt.Errorf("resource %q: backup.snapshots.retain: %w", r.Name, err)
		}
	}
	return nil
}

func validateSchedule(s string) error {
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

func validateRetain(s string) error {
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

// backupVolumeConfig is the Incus volume config a snapshot policy converges
// to. Nil for no policy (no block, or an opt-out): tink only ever sets keys it
// owns, matching diffConfig's one-directional rule.
func backupVolumeConfig(b *VolumeBackup) map[string]string {
	if b == nil || b.Snapshots == nil {
		return nil
	}
	return map[string]string{
		volKeySnapshotSchedule: strings.TrimSpace(b.Snapshots.Schedule),
		volKeySnapshotExpiry:   strings.TrimSpace(b.Snapshots.Retain),
	}
}

// volumeBackupConfig is everything a storage volume's declaration converges its config to: the snapshot keys, and the
// copy policy (PolicyKey). set is what to write; remove is the keys tink owns outright that must not be there, which
// today is the policy when the declaration has none. It errors when a copy names a target that is not in targets, which
// loading a stack already rejects.
func volumeBackupConfig(r Resource, env volumeEnv) (set map[string]string, remove []string, err error) {
	set = backupVolumeConfig(r.Backup)
	if env.stack != "" { // the pointer back to the stack that applied it; never removed (see StackKey)
		if set == nil {
			set = map[string]string{}
		}
		set[StackKey] = env.stack
	}
	policy, err := BuildPolicy(r, env.targets)
	if err != nil {
		return nil, nil, err
	}
	if policy == "" {
		return set, []string{PolicyKey}, nil
	}
	if set == nil {
		set = map[string]string{}
	}
	set[PolicyKey] = policy
	return set, nil, nil
}

// volumeEnv is what deciding a storage volume needs from the rest of the stack, which one dependency level on its own
// does not have: the backup targets its copies name, and the name the stack gives itself.
type volumeEnv struct {
	targets map[string]Resource
	stack   string
	// helperReads and helperLabel are what the helper says it can read (see PlanOptions); policyProto is the protocol this tink writes
	// (0 means PolicyProto: it is a field only so a test can ask what happens when a newer one is written).
	helperReads int
	helperLabel string
	// helperRemotes are the remotes the helper can reach (nil: it has not said, so none is checked).
	helperRemotes map[string]bool
	policyProto   int
}

func (e volumeEnv) writes() int {
	if e.policyProto > 0 {
		return e.policyProto
	}
	return PolicyProto
}

// helperCannotRead is the reason a policy must not be written, or "" when it may be: the helper reads older policies than this tink
// writes, and would skip the volume's copies, silently.
func (e volumeEnv) helperCannotRead() string {
	if e.helperReads <= 0 || e.helperReads >= e.writes() {
		return ""
	}
	return fmt.Sprintf("the helper (%s) reads copy policies up to protocol %d and this tink writes protocol %d, so it would skip this volume's copies, silently: "+
		"upgrade the helper first (tink helper upgrade), or apply with a tink that writes protocol %d", e.helperLabel, e.helperReads, e.writes(), e.helperReads)
}

// helperRemoteWarnings names the copies of r that go to an Incus remote the helper does not have. The helper's schedule would try them and
// fail every time, which shows up as a failing copy, late: this says it while the policy is being written.
func (e volumeEnv) helperRemoteWarnings(r Resource) []string {
	if e.helperRemotes == nil || r.Backup == nil || r.Backup.None != "" {
		return nil
	}
	var out []string
	said := map[string]bool{}
	for _, c := range r.Backup.Copies {
		t, ok := e.targets[c.Target]
		if !ok || t.Remote == "" || e.helperRemotes[t.Remote] || said[t.Remote] {
			continue
		}
		said[t.Remote] = true
		msg := fmt.Sprintf("copies to %q (remote %q) will fail: the helper (%s) has no remote of that name. Add it with a trust token made on that server (incus config trust add helper -q): tink helper remote add %s --token-file -",
			t.Name, t.Remote, e.helperLabel, t.Remote)
		if addr, fp, ok := t.DeclaredRemote(); ok {
			// the stack opted in to saying where the server is: a server that already trusts the helper's certificate needs no token
			msg += fmt.Sprintf(". This stack declares where it is, so if that server already trusts the helper's certificate no token is needed: tink helper remote add %s %s --fingerprint %s",
				t.Remote, addr, fp)
		}
		out = append(out, msg)
	}
	return out
}

// decideVolume is planStorageVolume's decision with the Incus read already
// done, so the policy is testable without a daemon. current is nil when the
// volume does not exist yet.
//
// A volume with no backup block is converged exactly as before, plus a warning
// whether or not it already exists: the question should be answered in the
// YAML, and a volume that predates the field is the one that most needs it
// asked. (This is the line to flip to ActionBlocked when the warning graduates
// to an error.)
func decideVolume(r Resource, current *api.StorageVolume, env volumeEnv) PlannedResource {
	desired, remove, err := volumeBackupConfig(r, env)
	if err != nil {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{err.Error()}}
	}
	var warnings []string
	if r.Backup == nil {
		warnings = append(warnings, "no backup declared -- add `backup: {snapshots: {schedule: ..., retain: ...}}`, "+
			"or `backup: {none: \"<why this volume needs no backup>\"}` if it really does not need one; "+
			"this will become an error in a future release")
	}
	if r.Backup != nil && r.Backup.None != "" && current != nil && current.Config[volKeySnapshotSchedule] != "" {
		warnings = append(warnings, fmt.Sprintf(
			"backup: none, but the volume still has %s=%q set live -- tink does not remove it; clear it with `incus storage volume unset`",
			volKeySnapshotSchedule, current.Config[volKeySnapshotSchedule]))
	}

	if current != nil && env.stack != "" {
		if owner := current.Config[StackKey]; owner != "" && owner != env.stack {
			warnings = append(warnings, fmt.Sprintf("this volume is stamped as belonging to stack %q, and applying takes it over for %q (%s) -- if two stacks declare it they will keep taking it from each other",
				owner, env.stack, StackKey))
		}
	}

	if current != nil {
		if w := verifyWarning(r, current.Config, timeNow()); w != "" {
			warnings = append(warnings, w)
		}
		warnings = append(warnings, copyWarnings(r, current.Config, timeNow())...)
	}

	// The policy is one escaped line of JSON, which is for the volume and not for a reader: it is described in words, and left out of the
	// raw config diff.
	wantPolicy := desired[PolicyKey]
	rest := make(map[string]string, len(desired))
	for k, v := range desired {
		if k != PolicyKey {
			rest[k] = v
		}
	}
	if current == nil {
		return PlannedResource{Resource: r, Action: ActionCreate, Changes: append(diffConfig(nil, rest, nil), DescribePolicyChange("", wantPolicy)...), Warnings: warnings}
	}
	changes := diffConfig(current.Config, rest, nil)
	changes = append(changes, DescribePolicyChange(current.Config[PolicyKey], wantPolicy)...)
	for _, k := range remove {
		if _, there := current.Config[k]; there && k != PolicyKey { // the policy's removal is said above
			changes = append(changes, fmt.Sprintf("config.%s: removed (the declaration no longer has one)", k))
		}
	}
	if len(changes) > 0 {
		return PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes, Warnings: warnings}
	}
	return PlannedResource{Resource: r, Action: ActionNone, Warnings: warnings}
}

// timeNow is overridable so the staleness rule can be tested.
var timeNow = time.Now

var verifyCadenceAge = map[string]time.Duration{
	"daily":   24 * time.Hour,
	"weekly":  7 * 24 * time.Hour,
	"monthly": 31 * 24 * time.Hour,
}

// verifyWarning says so when a volume declares a verify cadence but its last
// successful verification (the stamp `tink backup verify` leaves) is missing or
// older than the cadence. "Untested backup" becomes a visible state of the stack
// instead of something found out during an outage. No cadence declared, no nag.
func verifyWarning(r Resource, current map[string]string, now time.Time) string {
	if r.Backup == nil || r.Backup.None != "" || r.Backup.Verify == "" {
		return ""
	}
	maxAge := verifyCadenceAge[r.Backup.Verify]
	stamp := current[StampVerifiedAt]
	if stamp == "" {
		return fmt.Sprintf("verify: %s is declared but this volume has never been verified -- run `tink backup verify %s`", r.Backup.Verify, r.Name)
	}
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return fmt.Sprintf("%s=%q is not a timestamp tink wrote -- run `tink backup verify %s` to replace it", StampVerifiedAt, stamp, r.Name)
	}
	// A restore-only verification does not satisfy a declared check: otherwise running verify from
	// somewhere the stack file is not found would make the volume look freshly verified while the
	// check that matters never ran.
	if r.Backup.VerifyCheck != nil && current[StampVerifiedWith] != "check" {
		return fmt.Sprintf("a verify check is declared, but the last verification only proved the snapshot restores -- run `tink backup verify %s` with the stack file so the check runs", r.Name)
	}
	if age := now.Sub(at); age > maxAge {
		return fmt.Sprintf("last verified %s ago, older than the declared verify: %s -- run `tink backup verify %s`", humanAge(age), r.Backup.Verify, r.Name)
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
