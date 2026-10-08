// Package backupmeta is what tink records on a storage volume, what a volume's backup declaration is, and the rules that follow from them.
//
// Records: the config key names (the copy policy, the stamps `backup run` and `backup verify` leave on the source volume, the markers on a
// restore point, the pointer to the stack that applied the volume) and the arithmetic on the failure stamps. Declaration: the backup block
// (VolumeBackup and its parts), its validation, the Incus snapshot keys it maps to, and the warnings `plan` gives about a volume's copies
// and verification. Policy: the resolved copy policy a volume carries (BackupPolicy), how it is written and read, and how a change to it
// is put in words for `plan`. Schedule: when a cron expression next fires, when an Incus expiry is reached, and whether a copy is due.
//
// It is the vocabulary that the planner, the backup engine, the daemon's scheduler and the CLI all have to agree on, and it depends on
// none of them. That is the point of it being a package of its own: the engine that makes a copy should not have to import the resolver
// to learn what to call the keys it writes or when a copy is due. Its functions take what they need (a volume's name, its backup block, its
// live config) and not a resolver Resource, which is what lets it sit below the resolver.
package backupmeta

import (
	"fmt"
	"time"
)

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

// The mark of a copy that is still being made. It is a DIFFERENT key from a restore point's (MarkerCopyOf), so a copy in
// progress, or one cut off part way, is never listed, restored from, verified or counted as the newest backup. It is
// there so that tink can recognise, and later remove, a volume it started and never finished (the process was killed,
// the host lost power): without it such a volume carries no mark at all and nothing may touch it. The finished copy
// swaps these for the restore point's markers.
const (
	MarkerPartialOf = "user.tink.backup.copy-partial-of" // "<project>/<pool>/<volume>", as MarkerCopyOf
	MarkerPartialAt = "user.tink.backup.copy-partial-at" // RFC 3339, UTC: when the copy was started
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

// PolicyKey is the one volume config key tink owns outright for a volume's copy policy: the answer to "where does this
// volume get copied, how often, and how is the copy checked?", written by `apply` and read by whatever runs the copies
// (the helper's scheduler), so that the volume itself says what is meant to happen to it, `plan` can see when the
// declaration and the volume differ, and nothing has to keep a second copy of the stack.
//
// It is the one key tink also REMOVES: a volume whose declaration no longer has copies or verification must stop
// being copied, which a key tink never clears would not allow. The snapshot policy stays on Incus's own keys.
const PolicyKey = "user.tink.backup.policy"

// PolicyProto is the version of the policy document. A reader refuses a document with another one rather than
// guessing at it.
const PolicyProto = 1

// StackKey is the volume config key that points a storage volume back at the stack that applied it: the name given by
// that stack's `kind: stack` declaration. It makes a volume traceable (`incus storage volume show` says whose YAML to
// edit) and lets a stack find its own volumes without guessing, for instance the ones it applied once and no longer
// declares. It is written by `apply` and never removed by tink: a stack that is applied unnamed leaves it alone.
const StackKey = "user.tink.stack"

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
