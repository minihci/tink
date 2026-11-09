package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/backupext"
	"github.com/minihci/tink/internal/ownership"
)

// Ownership stamps, phase 1 (issue #127; the design is #133, the notes docs/ownership.md), and the resource stamp (issue #309).
//
// Every object tink CREATES for a named stack carries two stamps from the creation request on (createOne, createInstance): `user.tink.intent.stack: <stack
// name>`, whose it is, and `user.tink.intent.resource: <address>` (ownership.Address), which object it was made for. An object that was there already is
// not claimed by an ordinary apply: whose it is, and whether its stamps are meant for it, is a separate dimension of its plan, the stamp dimension
// (StampState), read from the object as the plan read it, that is never a change line, never an action and never an exit status. `tink plan` says in
// grouped lines how many declared objects have no stamp, carry stamps made for another object (a copy), or have no resource stamp yet.
//
// What may write a stamp is narrow. An ordinary apply keeps the stamps current on an object this stack already owns: it writes a missing resource stamp,
// in the same call as a real change, or alone otherwise (the narrow write of adopt.go), and restarts nothing for it. `tink plan apply --adopt` (or
// `ownership: adopt-on-apply` on the `kind: stack`) claims the declared objects that carry no stamp and the ones that are copies, in one narrow pass that
// writes the two stamps and nothing else. An object another stack stamped is left alone.
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

// StampState is the stamp dimension of one planned resource.
type StampState struct {
	// State is whose the object is for this stack, and whether its stamps are meant for it (ownership.Classify): ownership.Unknown unless the kind
	// carries a stamp, the object exists and the stack has a name.
	State ownership.State
	// Owner is the stack the object names, when State is Elsewhere (and the stack's own name for the other states that have one).
	Owner string
	// RestorePoint says that the object is one of tink's own restore points (or a copy in progress), which carries its source's stamps by design and is not
	// counted as a copy. It is read from the markers the backup capability registers.
	RestorePoint bool
	// Record is the applied record the object carries, judged against the one this declaration would write (applied.go): RecordNone unless the object is
	// stamped here and its kind has a record. Like State it is never a change line, an action or an exit status.
	Record RecordState
}

// addressOf is the address (ownership.Address) of the Incus object r declares: what its resource stamp holds. An image is found by its alias, so that is
// its name here; every other kind is placed by the project and pool the stack gave it (Levels puts the stack's project in each resource).
func addressOf(r Resource) string {
	name := r.Name
	if r.Kind == KindImage {
		name = r.Alias
	}
	return ownership.Address(string(r.Kind), r.Project, r.Pool, name)
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
		p.Ownership.State, p.Ownership.Owner = ownership.Classify(p.live, stack, addressOf(p.Resource))
		if p.Action != ActionBlocked { // a blocked resource is not written, so there is nothing to say about its record
			p.Ownership.Record = recordState(p.Resource, stack, p.Ownership.State, p.live)
		}
		if p.Ownership.State == ownership.Copy && p.Resource.Kind == KindStorageVolume {
			for _, marker := range backupext.Get().Keys().RestorePointMarkers {
				if _, ok := p.live[marker]; ok {
					p.Ownership.RestorePoint = true
				}
			}
		}
	}
	p.live, p.liveRead = nil, false
	return p.withRecordNotes(stack)
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

// OwnershipReport collects, over the plans of a whole stack, what `plan` says about the stamps: a few grouped lines, never a line of any resource, and
// never an exit status. It is the only change to the output of `plan` for a stack that exists already.
type OwnershipReport struct {
	stack     string
	adopting  bool
	unnamed   int // declared objects that would carry a stamp if the stack had a name
	unstamped []string
	copies    []string // carry this stack's stamps, made for another object
	unbound   []string // carry this stack's stamp and no resource stamp yet

	recordMissing    []string // stamped here, no applied record yet
	recordChanged    []string // stamped here, an applied record that this declaration changes
	recordUnreadable []string // stamped here, an applied record that cannot be read
}

// NewOwnershipReport starts a report for a stack. adopting says the apply that follows is going to claim what has no stamp and what is a copy (--adopt,
// or ownership: adopt-on-apply), which changes what those lines promise.
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
	switch p.Ownership.State {
	case ownership.Unstamped:
		o.unstamped = append(o.unstamped, stampLabel(r))
	case ownership.Copy:
		if !p.Ownership.RestorePoint { // tink's own restore points carry their source's stamps on purpose
			o.copies = append(o.copies, stampLabel(r))
		}
	case ownership.Unbound:
		o.unbound = append(o.unbound, stampLabel(r))
	}
	switch p.Ownership.Record {
	case RecordMissing:
		o.recordMissing = append(o.recordMissing, stampLabel(r))
	case RecordChanged:
		o.recordChanged = append(o.recordChanged, stampLabel(r))
	case RecordUnreadable:
		o.recordUnreadable = append(o.recordUnreadable, stampLabel(r))
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

// maxOwnershipNames is how many objects a line names before it counts the rest.
const maxOwnershipNames = 5

// listed is the sorted, shortened list a line carries, and the words that agree with how many there are.
func listed(labels []string) (list, noun, them string) {
	names := append([]string(nil), labels...)
	sort.Strings(names)
	shown := names
	if len(shown) > maxOwnershipNames {
		shown = shown[:maxOwnershipNames]
	}
	list = strings.Join(shown, ", ")
	if more := len(names) - len(shown); more > 0 {
		list += fmt.Sprintf(" (+%d more)", more)
	}
	noun, them = "objects", "them"
	if len(names) == 1 {
		noun, them = "object", "it"
	}
	return list, noun, them
}

// Lines are the lines to print, or none when there is nothing to say: every declared object that exists is stamped, for itself.
func (o *OwnershipReport) Lines() []string {
	if o.stack == "" {
		if o.unnamed == 0 {
			return nil
		}
		return []string{fmt.Sprintf("ownership: this stack has no name (add a `kind: stack` document), so nothing it creates is stamped with the stack it belongs to and nothing can be adopted; "+
			"once it is named, %d declared object(s) would be stamped", o.unnamed)}
	}
	var out []string
	if n := len(o.unstamped); n > 0 {
		list, noun, them := listed(o.unstamped)
		have := "have"
		if n == 1 {
			have = "has"
		}
		tail := "nothing changes because of it; `tink plan apply --adopt` stamps " + them + " in one pass and restarts none"
		if o.adopting {
			tail = "the next apply stamps " + them + " in one pass and restarts none (ownership: adopt-on-apply)"
		}
		out = append(out, fmt.Sprintf("ownership: %d declared %s %s no stamp (made before stamps, or by hand): %s; %s", n, noun, have, list, tail))
	}
	if n := len(o.copies); n > 0 {
		list, noun, them := listed(o.copies)
		carry := "carry"
		if n == 1 {
			carry = "carries"
		}
		tail := "nothing acts on them and nothing changes because of it; `tink plan apply --adopt` takes " + them + " over for this declaration in one pass and restarts none"
		if o.adopting {
			tail = "nothing acts on them until the next apply takes " + them + " over for this declaration in one pass, restarting none (ownership: adopt-on-apply)"
		}
		out = append(out, fmt.Sprintf("ownership: %d declared %s %s stamps made for another resource (copies?): %s; %s", n, noun, carry, list, tail))
	}
	if n := len(o.unbound); n > 0 {
		list, noun, _ := listed(o.unbound)
		have := "have"
		if n == 1 {
			have = "has"
		}
		out = append(out, fmt.Sprintf("ownership: %d declared %s %s no resource stamp yet: %s; `tink plan apply` writes it and restarts none", n, noun, have, list))
	}
	// The applied record is a derived stamp: it says what the declaration applied, so only that it is missing or changed is said, never its value.
	if n := len(o.recordMissing); n > 0 {
		list, noun, _ := listed(o.recordMissing)
		have := "have"
		if n == 1 {
			have = "has"
		}
		out = append(out, fmt.Sprintf("applied record: %d declared %s %s no record of what was applied yet: %s; `tink plan apply` writes it, in the same write as any change and otherwise alone, and restarts none", n, noun, have, list))
	}
	if n := len(o.recordChanged); n > 0 {
		list, noun, _ := listed(o.recordChanged)
		out = append(out, fmt.Sprintf("applied record: %d declared %s whose record of what was applied is changed by this declaration (a derived stamp: its value is not shown): %s; `tink plan apply` writes the new one and restarts none for it", n, noun, list))
	}
	if n := len(o.recordUnreadable); n > 0 {
		list, noun, _ := listed(o.recordUnreadable)
		out = append(out, fmt.Sprintf("applied record: %s on %d declared %s: %s; it is treated as absent, so nothing is removed because of it, and `tink plan apply` writes a new one and restarts none for it", reasonRecordUnreadable, n, noun, list))
	}
	return out
}
