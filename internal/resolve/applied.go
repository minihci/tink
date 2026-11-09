package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/applied"
	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/secrets"
)

// The applied record (issue #312; the package is internal/applied, the notes docs/ownership.md): what tink applied to an object, stored on the object in
// the intent key ownership.AppliedKey, in the same call as the config it describes. The resolver does three things with it.
//
//   - It writes it. Every create and every update of an instance, a profile, a storage volume, a network and a network ACL carries the record of the
//     declaration being applied (stampedConfig on a creation, applyOne for an update). A project and an image are create-only, so they have nothing to
//     record. An object this stack owns that has no record, or an out-of-date one, and nothing else to change, gets it through the narrow stamp-only
//     write (upkeepJob), which restarts nothing: the record is a stamp, and a difference in it is never a change.
//   - It reads it, and only where it is true: on an object whose resource stamp matches (ownership.Here). On a copy, an unbound object, another stack's
//     object or one nobody stamped it is ignored.
//   - It asks it one question, through applied.StaleApplied: what did this stack apply that it no longer declares? `plan` shows the answer as removals and
//     `apply` makes them, both through staleFor, so they cannot disagree. Anything not in the record is somebody else's and is never removed.
//
// What the record lists. The declared config keys with their values, the declared devices as whole blocks, the profile list of an instance and the rules of
// an ACL. Not the keys tink writes itself (the stamps, the derived publish, site and derive stamps and the copy policy: other passes own those), not the
// snapshot keys a volume's backup block writes (the backup capability owns them, and `backup: none` deliberately leaves a schedule in place: see
// reasonBackupNoneButSet), and not a volume's creation-only keys (initial.*), which Incus reads once and the update path never writes again.

// staleWhy is what a planned removal says about why it is planned.
const staleWhy = "applied by this stack before, no longer declared"

// recordedKind says whether a declared resource of this kind carries an applied record: the kinds whose declared config `apply` can change.
func recordedKind(k Kind) bool {
	switch k {
	case KindInstance, KindProfile, KindStorageVolume, KindNetwork, KindNetworkACL:
		return true
	}
	return false
}

// RecordState is the applied record of one planned resource, read beside its other stamps: never a change, an action or an exit status.
type RecordState int

const (
	// RecordNone: nothing was judged. The kind has no record, the stack has no name, the object does not exist, or it is not one the record is true of.
	RecordNone RecordState = iota
	// RecordCurrent: the object carries exactly the record this declaration would write.
	RecordCurrent
	// RecordMissing: the object is this stack's and carries no record.
	RecordMissing
	// RecordChanged: the object carries a record that differs from the one this declaration would write.
	RecordChanged
	// RecordUnreadable: the object carries a record that cannot be read, which is treated as absent.
	RecordUnreadable
)

// appliedInput is what the record of r is made from, and whether r is a kind that has one.
func appliedInput(r Resource) (applied.Input, bool) {
	in := applied.Input{Withhold: func(k string) bool { return r.SecretKeys[k] || secrets.SensitiveKey(k) }}
	switch r.Kind {
	case KindInstance:
		in.Config, in.Devices, in.Profiles = r.Config, r.Devices, r.Profiles
	case KindProfile:
		in.Config, in.Devices = r.Config, r.Devices
	case KindStorageVolume:
		in.Config = r.Config
		if len(r.Config) > 0 {
			in.Config = withoutKeys(r.Config, func(k string) bool { return strings.HasPrefix(k, creationOnlyPrefix) })
		}
	case KindNetwork:
		in.Config = r.Config
	case KindNetworkACL:
		in.Config = r.Config
		in.Ingress, in.Egress = ruleList(r.Ingress), ruleList(r.Egress)
	default:
		return in, false
	}
	return in, true
}

// withoutKeys is m without the keys drop names, as a new map.
func withoutKeys(m map[string]string, drop func(string) bool) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if !drop(k) {
			out[k] = v
		}
	}
	return out
}

// ruleList is a declared or live rule list in the record's shape, as Incus holds it (aclRules): nil stays nil, because a direction the declaration does not
// give is a different thing from one it gives empty.
func ruleList(rules []api.NetworkACLRule) []applied.Rule {
	if rules == nil {
		return nil
	}
	out := make([]applied.Rule, 0, len(rules))
	for _, r := range aclRules(rules) {
		out = append(out, applied.Rule(r))
	}
	return out
}

// appliedRecordFor is the record r would be applied with, as the text that is stored, and whether there is one: a kind without a record, a stack with no
// name (nothing is stamped, so nothing is owned) and a record too long to write have none. The report says what was kept short.
func appliedRecordFor(r Resource, stack string) (string, applied.Report, bool) {
	in, ok := appliedInput(r)
	if !ok || stack == "" {
		return "", applied.Report{}, false
	}
	rec, rep := applied.Build(in)
	if rep.None {
		return "", rep, false
	}
	return applied.Encode(rec), rep, true
}

// stampedConfig is config as a creation request carries it for stack: both stamps and, for a kind that has one, the applied record of r. The map it is given
// is not changed.
func stampedConfig(config map[string]string, r Resource, stack string) map[string]string {
	out := ownership.With(config, stack, addressOf(r))
	if rec, _, ok := appliedRecordFor(r, stack); ok {
		out = withStamps(out, map[string]string{ownership.AppliedKey: rec})
	}
	return out
}

// declaredState is what r declares, in the shape the record is compared in.
func declaredState(r Resource) applied.State {
	return applied.State{Config: r.Config, Devices: r.Devices, Ingress: ruleList(r.Ingress), Egress: ruleList(r.Egress)}
}

// staleFor is the question every plan and every apply asks of a live object: what did this stack apply to it that the declaration no longer has. live is the
// object as it was just read (its own config, never the expanded one). The record is used only where it is true of the object, which is where its stamps say
// it is this stack's, made for this very object. It returns the record too, for the names it lists.
func staleFor(r Resource, stack string, live applied.State) (applied.Stale, applied.Record) {
	state, _ := ownership.Classify(live.Config, stack, addressOf(r))
	rec, reading := applied.Trusted(live.Config, state)
	if reading != applied.Present {
		return applied.Stale{}, applied.Record{}
	}
	return applied.StaleApplied(live, rec, declaredState(r)), rec
}

// staleChanges are the plan lines for the removals staleFor finds: one per key, per device and per rule. A value tink would not print elsewhere (a name that
// looks like a secret, a value the record keeps by name only) is not printed here either.
func staleChanges(r Resource, stack string, live applied.State) []string {
	st, rec := staleFor(r, stack, live)
	if st.Empty() {
		return nil
	}
	byName := map[string]bool{}
	for _, k := range rec.ConfigNames {
		byName[k] = true
	}
	var out []string
	for _, k := range st.Keys {
		if byName[k] || secrets.SensitiveKey(k) {
			out = append(out, fmt.Sprintf("config.%s: %s -> (unset) (%s)", k, secrets.HiddenValue, staleWhy))
			continue
		}
		out = append(out, fmt.Sprintf("config.%s: %q -> (unset) (%s)", k, live.Config[k], staleWhy))
	}
	held := map[string]bool{}
	for _, n := range rec.DeviceNames {
		held[n] = true
	}
	for _, name := range st.Devices {
		if held[name] {
			out = append(out, fmt.Sprintf("device.%s: removed (%s)", name, staleWhy))
			continue
		}
		out = append(out, fmt.Sprintf("device.%s: removed (%s; %s)", name, deviceFields(live.Devices[name]), staleWhy))
	}
	for dir, rules := range map[string][]applied.Rule{"ingress": st.Ingress, "egress": st.Egress} {
		for _, rule := range rules {
			out = append(out, fmt.Sprintf("%s rule (in the live list): remove (%s; %s)", dir, aclRuleSummary(api.NetworkACLRule(rule)), staleWhy))
		}
	}
	sort.Strings(out)
	return out
}

// staleAppliedOf is staleFor as the instance engine asks it (instance.Spec.StaleApplied): from the config and devices ApplyConfig has just read.
func staleAppliedOf(r Resource, stack string) func(map[string]string, map[string]map[string]string) ([]string, []string) {
	if stack == "" {
		return nil
	}
	return func(config map[string]string, devices map[string]map[string]string) ([]string, []string) {
		st, _ := staleFor(r, stack, applied.State{Config: config, Devices: devices})
		return st.Keys, st.Devices
	}
}

// withoutRules is live without the rules in drop, in live's order.
func withoutRules(live []api.NetworkACLRule, drop []applied.Rule) []api.NetworkACLRule {
	var out []api.NetworkACLRule
	for _, rule := range live {
		gone := false
		for _, d := range drop {
			if applied.Rule(rule) == d {
				gone = true
				break
			}
		}
		if !gone {
			out = append(out, rule)
		}
	}
	return out
}

// removeStale deletes the stale keys of st from config and the stale devices from devices, in place.
func removeStale(st applied.Stale, config map[string]string, devices map[string]map[string]string) {
	for _, k := range st.Keys {
		delete(config, k)
	}
	for _, name := range st.Devices {
		delete(devices, name)
	}
}

// recordState reads the applied record of a live object that the plan has classified: whether the one it carries is the one this declaration would write.
func recordState(r Resource, stack string, state ownership.State, live map[string]string) RecordState {
	if state != ownership.Here || !recordedKind(r.Kind) {
		return RecordNone
	}
	want, _, ok := appliedRecordFor(r, stack)
	if !ok {
		return RecordNone
	}
	raw, has := live[ownership.AppliedKey]
	switch {
	case !has:
		return RecordMissing
	case raw == want:
		return RecordCurrent
	}
	if _, err := applied.Parse(raw); err != nil {
		return RecordUnreadable
	}
	return RecordChanged
}

// withRecordNotes adds what the record has to say about its own size to the plan of an object this stack creates or owns: a value over the limit is recorded
// by name only and is not put back, a record over its limit keeps names and nothing else, and one that is too long even so is not written. Notes and
// warnings only: they change no action.
func (p PlannedResource) withRecordNotes(stack string) PlannedResource {
	if stack == "" || !recordedKind(p.Resource.Kind) || p.Action == ActionBlocked {
		return p
	}
	if p.Action != ActionCreate && p.Ownership.State != ownership.Here && p.Ownership.State != ownership.Unbound {
		return p
	}
	in, _ := appliedInput(p.Resource)
	_, rep := applied.Build(in)
	if len(rep.Large) > 0 {
		p.Findings = append(p.Findings, finding.Notef(reasonRecordLarge, "", "%s: over %d bytes, so the applied record lists them by name only and they are never put back", strings.Join(rep.Large, ", "), applied.MaxValue))
	}
	switch {
	case rep.None:
		p.Findings = append(p.Findings, finding.Warnf(reasonRecordNone, "", "the applied record is over %d bytes even as names, so none is written and nothing is removed because of it", applied.MaxRecord))
	case rep.Cut:
		p.Findings = append(p.Findings, finding.Warnf(reasonRecordCut, "", "the applied record is over %d bytes, so it lists names and no values, profiles or rules: nothing is put back from it", applied.MaxRecord))
	}
	return p
}

// upkeepJob is the narrow write an ordinary apply makes to keep the stamps of an object this stack owns current when it has nothing else to change: the
// resource stamp an unbound object lacks, and the applied record when the object has none or an out-of-date one. record is "" for a kind with none.
func upkeepJob(r Resource, record string) stampJob {
	return stampJob{decide: func(was ownership.State, live map[string]string) map[string]string {
		write := map[string]string{}
		switch was {
		case ownership.Unbound:
			write[ownership.ResourceKey] = addressOf(r)
			fallthrough
		case ownership.Here:
			if record != "" && live[ownership.AppliedKey] != record {
				write[ownership.AppliedKey] = record
			}
		}
		if len(write) == 0 {
			return nil
		}
		return write
	}}
}

// ownedConfig is r with the stamps an update of an object this stack owns carries in the same write as its changes: the resource stamp an unbound object
// lacks and the applied record. An object that is not this stack's is left as it is.
func ownedConfig(r Resource, state ownership.State, stack string) Resource {
	if state != ownership.Here && state != ownership.Unbound {
		return r
	}
	add := map[string]string{}
	if state == ownership.Unbound {
		add[ownership.ResourceKey] = addressOf(r)
	}
	if rec, _, ok := appliedRecordFor(r, stack); ok {
		add[ownership.AppliedKey] = rec
	}
	if len(add) > 0 {
		r.Config = withStamps(r.Config, add)
	}
	return r
}
