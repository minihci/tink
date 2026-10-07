package resolve

import (
	"fmt"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

// volumeBackupConfig is everything a storage volume's declaration converges its config to: the snapshot keys, and the
// copy policy (backupmeta.PolicyKey). set is what to write; remove is the keys tink owns outright that must not be there, which
// today is the policy when the declaration has none. It errors when a copy names a target that is not in targets, which
// loading a stack already rejects.
func volumeBackupConfig(r Resource, env volumeEnv) (set map[string]string, remove []string, err error) {
	set = backupmeta.SnapshotConfig(r.Backup)
	if env.stack != "" { // the pointer back to the stack that applied it; never removed (see backupmeta.StackKey)
		if set == nil {
			set = map[string]string{}
		}
		set[backupmeta.StackKey] = env.stack
	}
	policy, err := BuildPolicy(r, env.targets)
	if err != nil {
		return nil, nil, err
	}
	if policy == "" {
		return set, []string{backupmeta.PolicyKey}, nil
	}
	if set == nil {
		set = map[string]string{}
	}
	set[backupmeta.PolicyKey] = policy
	return set, nil, nil
}

// volumeEnv is what deciding a storage volume needs from the rest of the stack, which one dependency level on its own
// does not have: the backup targets its copies name, and the name the stack gives itself.
type volumeEnv struct {
	targets map[string]Resource
	stack   string
	// helperReads and helperLabel are what the helper says it can read (see PlanOptions); policyProto is the protocol this tink writes
	// (0 means backupmeta.PolicyProto: it is a field only so a test can ask what happens when a newer one is written).
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
	return backupmeta.PolicyProto
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
	if r.Backup != nil && r.Backup.None != "" && current != nil && current.Config[backupmeta.KeySnapshotSchedule] != "" {
		warnings = append(warnings, fmt.Sprintf(
			"backup: none, but the volume still has %s=%q set live -- tink does not remove it; clear it with `incus storage volume unset`",
			backupmeta.KeySnapshotSchedule, current.Config[backupmeta.KeySnapshotSchedule]))
	}

	if current != nil && env.stack != "" {
		if owner := current.Config[backupmeta.StackKey]; owner != "" && owner != env.stack {
			warnings = append(warnings, fmt.Sprintf("this volume is stamped as belonging to stack %q, and applying takes it over for %q (%s) -- if two stacks declare it they will keep taking it from each other",
				owner, env.stack, backupmeta.StackKey))
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
	wantPolicy := desired[backupmeta.PolicyKey]
	rest := make(map[string]string, len(desired))
	for k, v := range desired {
		if k != backupmeta.PolicyKey {
			rest[k] = v
		}
	}
	if current == nil {
		return PlannedResource{Resource: r, Action: ActionCreate, Changes: append(diffConfig(nil, rest, nil), DescribePolicyChange("", wantPolicy)...), Warnings: warnings}
	}
	changes := diffConfig(current.Config, rest, nil)
	changes = append(changes, DescribePolicyChange(current.Config[backupmeta.PolicyKey], wantPolicy)...)
	for _, k := range remove {
		if _, there := current.Config[k]; there && k != backupmeta.PolicyKey { // the policy's removal is said above
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

// The backup block's own rules live in backupmeta, as functions of the volume's name and its block; these adapt a Resource to them.

func validateBackup(r Resource) error { return backupmeta.ValidateBackup(r.Name, r.Backup) }

func copyWarnings(r Resource, current map[string]string, now time.Time) []string {
	return backupmeta.CopyWarnings(r.Name, r.Backup, current, now)
}

func verifyWarning(r Resource, current map[string]string, now time.Time) string {
	return backupmeta.VerifyWarning(r.Name, r.Backup, current, now)
}
