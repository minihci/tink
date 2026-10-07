package resolve

import (
	"fmt"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// PlanOptions are the read-only knobs shared by plan and plan apply, so the
// two cannot disagree about what they saw.
type PlanOptions struct {
	// Offline skips every registry lookup. Images that then cannot be verified
	// are handled per on_image_change policy (see decideInstance).
	Offline bool

	env *imageEnv

	// targets are the WHOLE stack's backup-target resources, which a volume's
	// 3-2-1 check needs. Set by ForResources; a caller that plans one dependency
	// level at a time (as `tink plan` does) must call it with the full stack,
	// because the targets and the volumes that copy to them land in different
	// levels. PlanWithOptions/ApplyWithOptions only fill it in when it is unset.
	targets map[string]Resource
	// helperReads is the newest copy-policy protocol the server's helper says it can read (0: there is no helper, or it has said
	// nothing), and helperLabel names it. A policy of a newer protocol would be skipped by that helper without a word, so plan blocks
	// writing one.
	helperReads int
	helperLabel string
	// stack is the name the stack gives itself (kind: stack), stamped on the storage volumes it applies. Set with
	// targets, from the full stack, for the same reason.
	stack string
}

// ForResources returns the options carrying the backup targets of the full
// stack, so planning a subset of it (one level) can still judge a volume's
// copies against the targets declared elsewhere in the stack.
func (o PlanOptions) ForResources(resources []Resource) PlanOptions {
	o.targets = backupTargets(resources)
	o.stack, _ = StackName(resources) // more than one is reported by Levels
	return o
}

// withTargets fills the targets in from resources unless the caller already
// supplied the full stack's: a subset must never replace them.
func (o PlanOptions) withTargets(resources []Resource) PlanOptions {
	if o.targets == nil {
		o.targets = backupTargets(resources)
		o.stack, _ = StackName(resources)
	}
	return o
}

// WithHelperPolicy tells planning what the server's helper can read. Without it nothing is checked: a server with no helper has
// nobody to skip a policy, and one that has not said what it reads cannot be held to it.
func (o PlanOptions) WithHelperPolicy(label string, readsUpTo int) PlanOptions {
	o.helperLabel, o.helperReads = label, readsUpTo
	return o
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
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	changes := diffConfig(current.Config, r.Config, r.SecretKeys)
	changes = append(changes, diffDevices(current.Devices, r.Devices)...)

	env := opts.env
	if env == nil {
		env = newImageEnv(opts.Offline)
	}
	chk := env.checkInstance(server, current, r)

	var pre preflight
	if r.onImageChangePolicy() == OnImageChangeRebuild && len(chk.Drift) > 0 && len(chk.Unverified) == 0 {
		pre = rebuildPreflight(server, current, r, env)
	}
	return decideInstance(r, changes, chk, pre), nil
}

// decideInstance turns the findings into a disposition. Pure, so the policy
// table is directly testable.
//
//	policy   confirmed drift             could not verify
//	report   BLOCKED, nothing applied    warning, proceeds
//	ignore   warning, proceeds           warning, proceeds
//	rebuild  would rebuild (or BLOCKED   BLOCKED -- never rebuild blind
//	         if a preflight check fails)
func decideInstance(r Resource, changes []string, chk imageCheck, pre preflight) PlannedResource {
	p := PlannedResource{Resource: r, Changes: changes, Drift: chk.Drift}
	if len(changes) > 0 {
		p.Action = ActionUpdate
	}
	block := func(reasons ...string) {
		p.Action = ActionBlocked
		p.Blocked = append(p.Blocked, reasons...)
		if len(changes) > 0 {
			p.Blocked = append(p.Blocked, fmt.Sprintf("%d config/device change(s) are withheld until this instance matches the YAML", len(changes)))
		}
	}

	switch r.onImageChangePolicy() {
	case OnImageChangeIgnore:
		for _, d := range chk.Drift {
			p.Warnings = append(p.Warnings, d+" -- ignored (on_image_change: ignore)")
		}
		p.Drift = nil // already reported above; do not print it twice
		p.Warnings = append(p.Warnings, chk.Unverified...)

	case OnImageChangeRebuild:
		switch {
		case len(chk.Unverified) > 0:
			block(append([]string{"on_image_change: rebuild will not act on an image it cannot verify"}, chk.Unverified...)...)
		case len(chk.Drift) > 0 && len(pre.Blockers) > 0:
			block(pre.Blockers...)
		case len(chk.Drift) > 0:
			p.Action = ActionRebuild
		}
		p.Warnings = append(p.Warnings, pre.Warnings...)

	default: // report
		if len(chk.Drift) > 0 {
			block(fmt.Sprintf("image drift with on_image_change: report -- nothing on this instance is changed (set on_image_change: ignore to accept it, or on_image_change: rebuild to converge it): %s", strings.Join(chk.Drift, "; ")))
		}
		p.Warnings = append(p.Warnings, chk.Unverified...)
	}
	return p
}
