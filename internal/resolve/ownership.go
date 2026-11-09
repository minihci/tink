package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/ownership"
)

// Ownership stamps, phase 1 (issue #127; the design is #133, the notes docs/ownership.md).
//
// Every object tink CREATES for a named stack carries `user.tink.intent.stack: <stack name>` from the creation request on (createOne, createInstance). An
// object that was there already is not touched by an ordinary apply: whose it is is a separate dimension of its plan (StampState), read from the
// object as the plan read it, that is never a change line, never an action and never an exit status. `tink plan` says in one line how many declared
// objects have no stamp; `tink plan apply --adopt` (or `ownership: adopt-on-apply` on the `kind: stack`) stamps them in one narrow pass (adopt.go) that
// writes the one key and nothing else, and in particular never restarts anything.
//
// Which kinds: project, profile, storage-volume, instance, network, network-acl and image (its `properties`). Not the helper (`kind: helper` is
// installed by the helper's own installer, and its project is the installer's), not a project that is only named by `project:` (a reference, never
// made by the stack), and not the declarations that are no object (file, exec, backup-target, publish, stack).

// stampedKind says whether a declared resource of this kind is an Incus object that carries the ownership stamp.
func stampedKind(k Kind) bool {
	switch k {
	case KindProject, KindProfile, KindStorageVolume, KindInstance, KindNetwork, KindNetworkACL, KindImage:
		return true
	}
	return false
}

// StampState is the ownership dimension of one planned resource.
type StampState struct {
	// State is whose the object is for this stack: ownership.Unknown unless the kind carries a stamp, the object exists and the stack has a name.
	State ownership.State
	// Owner is the stack the object names, when State is Elsewhere (and the stack's own name when it is Here).
	Owner string
}

// observed records the config (an image's properties) a plan read from the live object, so that planKind can say whose the object is once the kind has
// decided everything else. Every kind's plan calls it where it has the live object in hand.
func observed(p PlannedResource, config map[string]string) PlannedResource {
	p.live, p.liveRead = config, true
	return p
}

// withOwnership classifies the object the plan observed, for the named stack, and forgets what it read. It changes nothing else about the plan.
func (p PlannedResource) withOwnership(stack string) PlannedResource {
	if stack != "" && p.liveRead && stampedKind(p.Resource.Kind) {
		p.Ownership.State, p.Ownership.Owner = ownership.Classify(p.live, stack)
	}
	p.live, p.liveRead = nil, false
	return p
}

// withoutOwnershipKey returns config without the ownership stamp.
func withoutOwnershipKey(config map[string]string) map[string]string {
	if _, ok := config[ownership.Key]; !ok {
		return config
	}
	out := make(map[string]string, len(config))
	for k, v := range config {
		if k != ownership.Key {
			out[k] = v
		}
	}
	return out
}

// withoutTinkState returns a copy of config without the keys tink wrote itself (intent and outcomes, internal/ownership), which are state and not
// something a stack declares: the ownership stamp, above all.
func withoutTinkState(config map[string]string) map[string]string {
	out := make(map[string]string, len(config))
	for k, v := range config {
		if !ownership.WrittenByTink(k) {
			out[k] = v
		}
	}
	return out
}

// validateStateKeys refuses, for every kind that has a config or an image's properties, any key that tink writes itself: intent (what it derives from
// the stack: the owner stamp, the publish, site and derive stamps, the copy policy) and outcome (what ran: a copy's stamps, an exec's hash, the helper's
// marker). A second source for such a key could only disagree with the first. Facts (user.tink.role and the rest of the short list in internal/ownership)
// are a person's, and pass. To take over an object that already exists, adopt it.
func validateStateKeys(r Resource) error {
	for _, m := range []struct {
		field string
		keys  map[string]string
	}{{"config", r.Config}, {"properties", r.Properties}} {
		for _, k := range sortedKeys(m.keys) {
			if ownership.WrittenByTink(k) {
				return stateKeyError(r, m.field, k)
			}
		}
	}
	return nil
}

// stateKeyError says why k may not be declared, and where the declaration it comes from lives where there is one: the stamps of a publish, of a derived
// file and of a site are written from a document of the stack (instances and network ACLs), and the owner stamp from the stack's name.
func stateKeyError(r Resource, field, k string) error {
	if field == "config" && (r.Kind == KindInstance || r.Kind == KindNetworkACL) {
		switch {
		case r.Kind == KindInstance && k == SiteKey:
			return fmt.Errorf("resource %q: config %s is the site stamp, which tink owns: it writes it from `site:` on the stack's kind: stack document (on the instances that state a role or are published) and removes it where the stack no longer derives it, so declare the site and not the key", r.Name, k)
		case strings.HasPrefix(k, DeriveStampPrefix) && r.Kind == KindNetworkACL:
			return fmt.Errorf("resource %q: config %s is under %s, which tink owns: it writes that key from derive_ingress: true and removes it when the stack no longer derives the rules, so declare derive_ingress and not the key", r.Name, k, DeriveStampPrefix)
		case strings.HasPrefix(k, DeriveStampPrefix):
			return fmt.Errorf("resource %q: config %s is under %s, which tink owns: it writes those keys from the kind: file documents of the stack that say derive: and removes the ones no file derives, so declare the file and not the key", r.Name, k, DeriveStampPrefix)
		case strings.HasPrefix(k, PublishStampPrefix) && r.Kind == KindInstance:
			return fmt.Errorf("resource %q: config %s is under %s, which tink owns: it writes those keys from the kind: publish documents of the stack and removes the ones no publish derives, so declare the publish and not the key", r.Name, k, PublishStampPrefix)
		}
	}
	if k == ownership.Key {
		return fmt.Errorf("%s %q: %s.%s is written by tink itself, from the stack's name (the kind: stack document): it records which stack an object belongs to, so it cannot be declared "+
			"(an object that was made before stamps is stamped by `tink plan apply --adopt`)", r.Kind, r.Name, field, k)
	}
	what := "from the stack's declaration"
	if ownership.ClassOf(k) == ownership.Outcome {
		what = "to record what ran"
	}
	return fmt.Errorf("%s %q: %s.%s is written by tink itself, %s (every key under %s or %s is): it cannot be declared", r.Kind, r.Name, field, k, what, ownership.IntentPrefix, ownership.OutcomePrefix)
}

// Ownership modes of a `kind: stack` (its `ownership:` field).
const (
	// OwnershipExplicit is the default: an object that exists is stamped only when a person runs `tink plan apply --adopt`.
	OwnershipExplicit = "explicit"
	// OwnershipAdoptOnApply makes adopting standing: every `tink plan apply` of the stack also stamps the declared objects that have no stamp.
	OwnershipAdoptOnApply = "adopt-on-apply"
)

// validateStackOwnership checks the `ownership:` of a `kind: stack` document, and refuses it on any other kind (fieldOwners).
func validateStackOwnership(r Resource) error {
	if r.Kind != KindStack {
		return nil
	}
	switch r.Ownership {
	case "", OwnershipExplicit, OwnershipAdoptOnApply:
		return nil
	}
	return fmt.Errorf("resource %q: ownership %q is not one of %s (the default) or %s", r.Name, r.Ownership, OwnershipExplicit, OwnershipAdoptOnApply)
}

// AdoptOnApply reports whether the stack's `kind: stack` document asks for adopting on every apply.
func AdoptOnApply(resources []Resource) bool {
	for _, r := range resources {
		if r.Kind == KindStack && r.Ownership == OwnershipAdoptOnApply {
			return true
		}
	}
	return false
}

// OwnershipReport collects, over the plans of a whole stack, what `plan` says about ownership: one line, and nothing else. It is the only change to the
// output of `plan` for a stack that exists already.
type OwnershipReport struct {
	stack     string
	adopting  bool
	unnamed   int // declared objects that would carry a stamp if the stack had a name
	unstamped []string
}

// NewOwnershipReport starts a report for a stack. adopting says the apply that follows is going to stamp what has no stamp (--adopt, or
// ownership: adopt-on-apply), which changes what the line promises.
func NewOwnershipReport(stack string, adopting bool) *OwnershipReport {
	return &OwnershipReport{stack: stack, adopting: adopting}
}

// Add records one planned resource.
func (o *OwnershipReport) Add(p PlannedResource) {
	r := p.Resource
	if !stampedKind(r.Kind) {
		return
	}
	if o.stack == "" {
		o.unnamed++
		return
	}
	if p.Ownership.State == ownership.Unstamped {
		o.unstamped = append(o.unstamped, stampLabel(r))
	}
}

// stampLabel names a resource the way a line about ownership lists it: kind/name, and @project when it is in one.
func stampLabel(r Resource) string {
	label := string(r.Kind) + "/" + r.Name
	if r.Kind == KindImage && r.Alias != "" {
		label = string(r.Kind) + "/" + r.Alias
	}
	if r.Project != "" && r.Project != "default" {
		label += "@" + r.Project
	}
	return label
}

// maxOwnershipNames is how many unstamped objects the line names before it counts the rest.
const maxOwnershipNames = 5

// Line is the one line to print, or "" when there is nothing to say: every declared object that exists is stamped, or there is none.
func (o *OwnershipReport) Line() string {
	switch {
	case o.stack == "" && o.unnamed > 0:
		return fmt.Sprintf("ownership: this stack has no name (add a `kind: stack` document), so nothing it creates is stamped with the stack it belongs to and nothing can be adopted; "+
			"once it is named, %d declared object(s) would be stamped", o.unnamed)
	case len(o.unstamped) == 0:
		return ""
	}
	names := append([]string(nil), o.unstamped...)
	sort.Strings(names)
	shown := names
	if len(shown) > maxOwnershipNames {
		shown = shown[:maxOwnershipNames]
	}
	list := strings.Join(shown, ", ")
	if more := len(names) - len(shown); more > 0 {
		list += fmt.Sprintf(" (+%d more)", more)
	}
	noun, them := "objects have", "them"
	if len(names) == 1 {
		noun, them = "object has", "it"
	}
	tail := "nothing changes because of it; `tink plan apply --adopt` stamps " + them + " in one pass and restarts none"
	if o.adopting {
		tail = "the next apply stamps " + them + " in one pass and restarts none (ownership: adopt-on-apply)"
	}
	return fmt.Sprintf("ownership: %d declared %s no stamp (made before stamps, or by hand): %s; %s", len(names), noun, list, tail)
}
