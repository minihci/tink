package resolve

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/incusapi"
)

// PlanOptions are the read-only knobs shared by plan and plan apply, so the
// two cannot disagree about what they saw.
type PlanOptions struct {
	// Offline skips every registry lookup. Images that then cannot be verified
	// are handled per on_image_change policy (see decideInstance).
	Offline bool

	env *imageEnv

	// ctx ends the run early: an exec stops waiting for its guest command when it does. Set by WithContext; nil means never. It is carried
	// here, with the run's other per-run state, because planning reaches an exec through planOne and planKind, which already take opts.
	ctx context.Context

	// targets are the WHOLE stack's backup-target resources, which a volume's
	// 3-2-1 check needs. Set by ForResources; a caller that plans one dependency
	// level at a time (as `tink plan` does) must call it with the full stack,
	// because the targets and the volumes that copy to them land in different
	// levels. PlanWithOptions/ApplyPrepared only fill it in when it is unset.
	targets map[string]Resource
	// helperReads is the newest copy-policy protocol the server's helper says it can read (0: there is no helper, or it has said
	// nothing), and helperLabel names it. A policy of a newer protocol would be skipped by that helper without a word, so plan blocks
	// writing one.
	helperReads int
	helperLabel string
	// helperRemotes are the Incus remotes the helper says it can reach, by name; nil means it has not said (no helper, or an older one),
	// and nothing is checked. A copy to a remote the helper does not have fails on every attempt.
	helperRemotes map[string]bool
	// stack is the name the stack gives itself (kind: stack), stamped on the storage volumes it applies. Set with
	// targets, from the full stack, for the same reason.
	stack string
	// profiles are the WHOLE stack's profile resources, by name, for the devices an instance gets from a profile (the NIC check in ocinic.go, the
	// root-disk check in rootdisk.go). Set with targets, from the full stack, because a profile and the instance that uses it land in different levels.
	profiles map[string]Resource
	// acls are the names of the WHOLE stack's network-acl resources, so a network, profile or instance that names an ACL is only warned about
	// when the stack does not declare it (networkacl.go). Set with targets, from the full stack, because an ACL and what names it land in
	// different levels.
	acls map[string]bool
	// extras: also report, for each instance that exists, what it carries that the stack does not declare (see Extras). Set by WithExtras.
	extras bool
	// projectNotes are warnings about resources that name no project in a stack where others do (see mixedProjectNotes), by resource
	// name, computed from the full stack with targets.
	projectNotes map[string]string
	// firstStart are the files to place with each instance this apply creates, before its first start (see firststart.go), from the full stack.
	firstStart map[string][]Resource
	// listenerWarnings are, by instance name, the warnings about host-side proxy listeners that overlap another instance's in the stack (proxydev.go),
	// computed from the full stack with targets, because the two instances that clash land in different levels.
	listenerWarnings map[string][]finding.Finding
	// opTimeout, when set, replaces the deadlines on every call to the server (opbounds.go). Unexported: a test seam, not a setting.
	opTimeout time.Duration
	// helperAuthz is the narrowing of the daemon's certificate (WithHelperAuthz), and narrowers the labels of the helpers of the full stack that ask for it.
	helperAuthz HelperAuthz
	narrowers   []string
}

// ForResources returns the options carrying the backup targets of the full
// stack, so planning a subset of it (one level) can still judge a volume's
// copies against the targets declared elsewhere in the stack.
func (o PlanOptions) ForResources(resources []Resource) PlanOptions {
	o.targets = backupTargets(resources)
	o.profiles = declaredProfiles(resources)
	o.acls = declaredACLs(resources)
	o.stack, _ = StackName(resources) // more than one is reported by Levels
	o.projectNotes = mixedProjectNotes(resources)
	o.firstStart = firstStartFiles(resources)
	o.listenerWarnings = proxyListenerWarnings(resources, o.profiles)
	o.narrowers = authzNarrowers(resources)
	return o
}

// withTargets fills the targets in from resources unless the caller already
// supplied the full stack's: a subset must never replace them.
func (o PlanOptions) withTargets(resources []Resource) PlanOptions {
	if o.targets == nil {
		o.targets = backupTargets(resources)
		o.stack, _ = StackName(resources)
		o.projectNotes = mixedProjectNotes(resources)
		if o.profiles == nil {
			o.profiles = declaredProfiles(resources)
		}
		o.listenerWarnings = proxyListenerWarnings(resources, o.profiles)
		o.narrowers = authzNarrowers(resources)
	}
	if o.profiles == nil {
		o.profiles = declaredProfiles(resources)
	}
	if o.acls == nil {
		o.acls = declaredACLs(resources)
	}
	if o.firstStart == nil {
		o.firstStart = firstStartFiles(resources)
	}
	return o
}

// WithContext makes planning and applying stop when ctx ends: a guest exec in flight is abandoned, and apply starts no further level.
func (o PlanOptions) WithContext(ctx context.Context) PlanOptions {
	o.ctx = ctx
	return o
}

// context is the context to run under, never nil.
func (o PlanOptions) context() context.Context {
	if o.ctx == nil {
		return context.Background()
	}
	return o.ctx
}

// WithHelperPolicy tells planning what the server's helper can read. Without it nothing is checked: a server with no helper has
// nobody to skip a policy, and one that has not said what it reads cannot be held to it.
func (o PlanOptions) WithHelperPolicy(label string, readsUpTo int) PlanOptions {
	o.helperLabel, o.helperReads = label, readsUpTo
	return o
}

// WithHelperRemotes tells planning which remotes the helper can reach, so that a copy to another one is warned about, not found out at the
// first scheduled run. A nil list means the helper has not said, and nothing is checked; an empty one means it has none.
func (o PlanOptions) WithHelperRemotes(label string, remotes []string) PlanOptions {
	if o.helperLabel == "" {
		o.helperLabel = label
	}
	if remotes == nil {
		o.helperRemotes = nil
		return o
	}
	o.helperRemotes = map[string]bool{}
	for _, r := range remotes {
		o.helperRemotes[r] = true
	}
	return o
}

// HelperRemotes is what planning was told the helper can reach, sorted: nil when it was told nothing, empty when the helper has none.
func (o PlanOptions) HelperRemotes() []string {
	if o.helperRemotes == nil {
		return nil
	}
	out := make([]string, 0, len(o.helperRemotes))
	for r := range o.helperRemotes {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// HelperPolicy is what planning was told the helper can read: its name and the newest copy-policy protocol, or 0 for nothing.
func (o PlanOptions) HelperPolicy() (label string, readsUpTo int) {
	return o.helperLabel, o.helperReads
}

// NewPlanOptions builds options with one registry cache shared by every
// resource planned (or applied) with them.
func NewPlanOptions(offline bool) PlanOptions {
	return PlanOptions{Offline: offline, env: newImageEnv(offline)}
}

func (r Resource) onImageChangePolicy() string {
	if r.OnImageChange == "" {
		return OnImageChangeReport
	}
	return r.OnImageChange
}

// planInstance reports config/device drift and image drift, and decides what
// apply may do about each according to the instance's on_image_change policy.
func planInstance(server incus.InstanceServer, r Resource, opts PlanOptions) (PlannedResource, error) {
	current, _, found, err := incusapi.LookupInstance(server, r.Name)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live instance: %w", err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate, Findings: firstStartNotes(server, r, opts)}, nil
	}
	changes := diffConfig(current.Config, r.Config, r.SecretKeys)
	// The stamps of kind: publish are the one owned prefix: a live stamp the stack no longer derives is removed, not left (publishstamp.go).
	if stale := staleOwnedChanges(current.Config, r.Config); len(stale) > 0 {
		changes = append(changes, stale...)
		sort.Strings(changes)
	}
	changes = append(changes, diffDevices(current.Devices, r.Devices)...)
	if r.Ephemeral && !current.Ephemeral {
		changes = append(changes, "ephemeral: false -> true")
	}
	// A stack declares its instances running (there is no field to declare one stopped), so one that exists and is Stopped is a start to plan. It is
	// what a create that failed between the instance existing and its first successful start leaves. Only Stopped counts: the other statuses are
	// not something a start fixes.
	if current.Status == "Stopped" {
		changes = append(changes, "state: Stopped -> Running (start)")
	}

	env := opts.env
	if env == nil {
		env = newImageEnv(opts.Offline)
	}
	chk := env.checkInstance(server, current, r)

	var pre preflight
	if r.onImageChangePolicy() == OnImageChangeRebuild && len(chk.Drift) > 0 && len(chk.Unverified) == 0 {
		pre = rebuildPreflight(server, current, r, env)
	}
	p := observed(decideInstance(r, changes, chk, pre, missingProfileBlockers(current, r)...), current.Config)
	if opts.extras {
		ex := instanceExtras(server, env, current, r, opts.Offline)
		p.Extras = &ex
	}
	return p, nil
}

// missingProfileBlockers are the findings that block an instance that lacks a profile its stack names. Apply never changes an instance's profile list
// (ApplyConfig writes config and devices only), so planning it as an update would promise a change that does not happen. Profiles the instance has
// beyond the stack's are not drift (Extras reports them) and the order is not compared; a stack that names no profiles says nothing about them.
func missingProfileBlockers(current *api.Instance, r Resource) []finding.Finding {
	if len(r.Profiles) == 0 {
		return nil
	}
	missing := difference(r.Profiles, current.Profiles)
	if len(missing) == 0 {
		return nil
	}
	return []finding.Finding{finding.Errorf(reasonProfilesMissing, "", "profile(s) %s are in the stack's profiles list but not on the instance, and apply does not change an instance's profiles (add them with `incus profile add`, or recreate the instance)", strings.Join(missing, ", "))}
}

// decideInstance turns the findings into a disposition. Pure, so the policy
// table is directly testable. blockers are further reasons to block that do
// not depend on the policy (a profile the instance lacks).
//
//	policy   confirmed drift             could not verify
//	report   BLOCKED, nothing applied    warning, proceeds
//	ignore   warning, proceeds           warning, proceeds
//	rebuild  would rebuild (or BLOCKED   BLOCKED -- never rebuild blind
//	         if a preflight check fails)
func decideInstance(r Resource, changes []string, chk imageCheck, pre preflight, blockers ...finding.Finding) PlannedResource {
	p := PlannedResource{Resource: r, Changes: changes}
	drift := func() {
		for _, d := range chk.Drift {
			p.Findings = append(p.Findings, finding.Warnf(ReasonImageDrift, "", "%s", d))
		}
	}
	unverified := func() {
		for _, u := range chk.Unverified {
			p.Findings = append(p.Findings, finding.Warnf(reasonImageUnverified, "", "%s", u))
		}
	}
	if len(changes) > 0 {
		p.Action = ActionUpdate
	}
	block := func(fs ...finding.Finding) {
		p.Action = ActionBlocked
		p.Findings = append(p.Findings, fs...)
		if len(changes) > 0 {
			p.Findings = append(p.Findings, finding.Errorf(reasonWithheldChanges, "", "%d config/device change(s) are withheld until this instance matches the YAML", len(changes)))
		}
	}

	// Drift is the first thing a plan prints about an instance after its changes, and the printer takes it out of the findings by its reason, so where it
	// is raised among them does not matter. Under on_image_change: ignore it is reported once, as a warning that says it is ignored, and not as drift.
	switch r.onImageChangePolicy() {
	case OnImageChangeIgnore:
		for _, d := range chk.Drift {
			p.Findings = append(p.Findings, finding.Warnf(reasonImageDriftIgnored, "", "%s -- ignored (on_image_change: ignore)", d))
		}
		unverified()

	case OnImageChangeRebuild:
		drift()
		switch {
		case len(chk.Unverified) > 0:
			head := finding.Errorf(reasonRebuildUnverified, "", "on_image_change: rebuild will not act on an image it cannot verify")
			fs := []finding.Finding{head}
			for _, u := range chk.Unverified {
				fs = append(fs, finding.Errorf(reasonImageUnverified, "", "%s", u))
			}
			block(fs...)
		case len(chk.Drift) > 0 && finding.HasErrors(pre.Findings):
			block(pre.errors()...)
		case len(chk.Drift) > 0:
			p.Action = ActionRebuild
		}
		p.Findings = append(p.Findings, pre.warnings()...)

	default: // report
		drift()
		if len(chk.Drift) > 0 {
			block(finding.Errorf(reasonImageDriftBlocks, "", "image drift with on_image_change: report -- nothing on this instance is changed (set on_image_change: ignore to accept it, or on_image_change: rebuild to converge it)"))
		}
		unverified()
	}
	if len(blockers) > 0 {
		if p.Action == ActionBlocked { // the withheld count is already there
			p.Findings = append(p.Findings, blockers...)
		} else {
			block(blockers...)
		}
	}
	return p
}
