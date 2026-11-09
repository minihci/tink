// Package applied is the record of what tink applied to one Incus object: the config keys it set, the devices it gave, the profiles it named and, for a
// network ACL, the rules it wrote, as they were when it last wrote them. It is stored on the object itself, in one intent key (ownership.AppliedKey),
// in the same call as the config it describes, so the record and the config cannot disagree unless somebody edited the object by hand. The design is
// issue #312 (it supersedes layer 2 of #138); the notes are in docs/ownership.md.
//
// What it is for. A declaration only ever says what an object should have. Without a record, a key or a device that the stack used to declare and no
// longer does is indistinguishable from one a person added by hand, so tink can neither take it back nor tell. With one, the difference is exact: what
// the record lists and the declaration no longer has is tink's own leftover (StaleApplied), and everything else is somebody else's and is never touched.
// The same record is what a daemon that heals declared drift will compare live state against.
//
// What it is not. It is never a snapshot: a value that looks like a secret is recorded by its name only (the caller says which, Input.Withhold), and so is
// a value over MaxValue; a record over MaxRecord is cut to names. Nothing here puts a value back that it does not hold. An empty value is not recorded at
// all, because Incus does not store one: a key declared "" is absent from the object after an apply (measured on Incus 7.5.1, 2026-11-09), so
// listing it would make its absence look like a hand removal.
//
// The package imports nothing of tink's but internal/ownership, so that the resolver and the daemon can both use it and neither imports the other. It
// does not know what a Resource is: the caller turns a declaration into an Input (resolve/applied.go).
package applied

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/ownership"
)

const (
	// Proto is the version of the document this package writes and reads. A record of another protocol is unreadable, which is the same as absent.
	Proto = 1
	// MaxValue is the longest config value, or device field value, the record keeps. A longer one is recorded by name only. Incus has no limit of its
	// own below the request body (1 MiB, shared by everything in one write, measured on 7.5.1), so this is tink's: cloud-init user data and long
	// environment values are the realistic large ones, and they are better reported than copied.
	MaxValue = 2 << 10
	// MaxRecord is the longest record written. A longer one is cut to names (Record.Cut); one that is too long even so is not written.
	MaxRecord = 32 << 10
)

// Rule is one network ACL rule as the record keeps it. The fields are those of Incus's rule, in its order and under its names, so the caller converts
// with a plain type conversion.
type Rule struct {
	Action          string `json:"action"`
	Source          string `json:"source,omitempty"`
	Destination     string `json:"destination,omitempty"`
	Protocol        string `json:"protocol,omitempty"`
	SourcePort      string `json:"source-port,omitempty"`
	DestinationPort string `json:"destination-port,omitempty"`
	ICMPType        string `json:"icmp-type,omitempty"`
	ICMPCode        string `json:"icmp-code,omitempty"`
	Description     string `json:"description,omitempty"`
	State           string `json:"state"`
}

// Record is what tink applied. Every list is sorted or in declared order, and a map is written with its keys sorted, so the same declaration always
// encodes to the same text and "is the record current?" is a string comparison.
type Record struct {
	Proto int `json:"proto"`
	// Config is the config keys applied, with their values. A key that is not here but is in ConfigNames was applied too, and its value is not kept.
	Config      map[string]string `json:"config,omitempty"`
	ConfigNames []string          `json:"config-names,omitempty"`
	// Devices are the devices applied, as whole blocks (Incus replaces a device by name, as a block). One whose block holds a value that is withheld is
	// in DeviceNames instead.
	Devices     map[string]map[string]string `json:"devices,omitempty"`
	DeviceNames []string                     `json:"device-names,omitempty"`
	// Profiles is the profile list an instance was created with, as declared.
	Profiles []string `json:"profiles,omitempty"`
	// Ingress and Egress are the rules of a network ACL, in the order they were written, for each direction the declaration gave.
	Ingress []Rule `json:"ingress,omitempty"`
	Egress  []Rule `json:"egress,omitempty"`
	// Cut says the record was over MaxRecord, so it keeps names and nothing else: no values, no profiles, no rules.
	Cut bool `json:"cut,omitempty"`
}

// State is what an object holds, or what a declaration asks it to hold, in the plain shape the two are compared in. For an ACL, a direction whose
// slice is nil is a direction the declaration does not give (as opposed to one it gives empty).
type State struct {
	Config  map[string]string
	Devices map[string]map[string]string
	Ingress []Rule
	Egress  []Rule
}

// Input is what a record is made from: what the declaration applied, in plain values.
type Input struct {
	Config   map[string]string
	Devices  map[string]map[string]string
	Profiles []string
	Ingress  []Rule
	Egress   []Rule
	// Withhold says that the value of a key (a config key, or a device field's name) must not be stored: it held a secret, or its name looks like one. Nil
	// withholds every value, so a caller that forgets it records names and no more.
	Withhold func(key string) bool
}

// Report says what Build did to keep the record small. It is for a plan to say, once, and carries names only.
type Report struct {
	// Large lists the config keys and devices that were recorded by name only for being over MaxValue, sorted ("config.KEY", "device.NAME").
	Large []string
	// Cut says the record was over MaxRecord and was cut to names.
	Cut bool
	// None says no record can be written: it is over MaxRecord even as names.
	None bool
}

// Empty reports that Build kept everything as it was given.
func (r Report) Empty() bool { return len(r.Large) == 0 && !r.Cut && !r.None }

// Build makes the record of in. Keys tink writes itself (intent and outcome, ownership.WrittenByTink) are not part of it: they are stamps and derived
// state, written by passes of their own. A key with an empty value is left out, because Incus would not have stored it.
func Build(in Input) (Record, Report) {
	rec := Record{Proto: Proto}
	var rep Report
	withhold := in.Withhold
	if withhold == nil {
		withhold = func(string) bool { return true }
	}
	for _, k := range sortedKeys(in.Config) {
		v := in.Config[k]
		if v == "" || ownership.WrittenByTink(k) {
			continue
		}
		switch {
		case withhold(k):
			rec.ConfigNames = append(rec.ConfigNames, k)
		case len(v) > MaxValue:
			rec.ConfigNames = append(rec.ConfigNames, k)
			rep.Large = append(rep.Large, "config."+k)
		default:
			if rec.Config == nil {
				rec.Config = map[string]string{}
			}
			rec.Config[k] = v
		}
	}
	for _, name := range sortedKeys(in.Devices) {
		dev := in.Devices[name]
		held, large := false, false
		for k, v := range dev {
			if withhold(k) {
				held = true
			}
			if len(v) > MaxValue {
				held, large = true, true
			}
		}
		switch {
		case held:
			rec.DeviceNames = append(rec.DeviceNames, name)
			if large {
				rep.Large = append(rep.Large, "device."+name)
			}
		default:
			if rec.Devices == nil {
				rec.Devices = map[string]map[string]string{}
			}
			rec.Devices[name] = copyMap(dev)
		}
	}
	rec.Profiles = append([]string(nil), in.Profiles...)
	rec.Ingress = append([]Rule(nil), in.Ingress...)
	rec.Egress = append([]Rule(nil), in.Egress...)

	if len(Encode(rec)) > MaxRecord {
		rep.Cut = true
		rec = Record{Proto: Proto, Cut: true, ConfigNames: namesOf(rec.Config, rec.ConfigNames), DeviceNames: namesOf(rec.Devices, rec.DeviceNames)}
		if len(Encode(rec)) > MaxRecord {
			rep.None = true
		}
	}
	return rec, rep
}

// Encode is the text a record is stored as: one line of JSON, with its keys in order.
func Encode(r Record) string {
	b, err := json.Marshal(r)
	if err != nil { // a Record holds strings, maps of strings and lists: it cannot fail
		panic(fmt.Sprintf("applied: encoding a record: %v", err))
	}
	return string(b)
}

// ErrUnreadable is why a stored record could not be used. It says nothing of the content, which is untrusted text.
var ErrUnreadable = errors.New("the applied record cannot be read")

// Parse reads a stored record. Text that is not a record of this protocol is ErrUnreadable.
func Parse(s string) (Record, error) {
	var r Record
	if err := json.Unmarshal([]byte(s), &r); err != nil || r.Proto < 1 || r.Proto > Proto {
		return Record{}, ErrUnreadable
	}
	return r, nil
}

// Reading is what a look at an object's record found.
type Reading int

const (
	// Absent: the object carries no record, or its record is not for this reader to use (Trusted says when).
	Absent Reading = iota
	// Unreadable: the object carries a record that cannot be read. It is treated as absent, and said so.
	Unreadable
	// Present: the object carries a record that can be read and that was written for it.
	Present
)

// Trusted reads the record on an object's config, and only if the object is one the record is true of: state is what ownership.Classify said of it for the
// stack asking, and a record is used on ownership.Here and on nothing else. On a copy, an object that carries no resource stamp, one that is another
// stack's or one nobody stamped, the record (if there is one) says what was applied to some other object, and it is ignored.
func Trusted(config map[string]string, state ownership.State) (Record, Reading) {
	raw, ok := config[ownership.AppliedKey]
	if !ok || state != ownership.Here {
		return Record{}, Absent
	}
	rec, err := Parse(raw)
	if err != nil {
		return Record{}, Unreadable
	}
	return rec, Present
}

// Stale is what the record lists, the object still has and the declaration no longer gives: exactly what tink applied before and may take back.
type Stale struct {
	// Keys are config keys, sorted. Devices are device names, sorted.
	Keys    []string
	Devices []string
	// Ingress and Egress are the live rules to remove, in the live order. Only for a direction the declaration does not give: a direction it does give
	// is the whole list for that direction, and an apply already replaces it.
	Ingress []Rule
	Egress  []Rule
}

// Empty reports that nothing is stale.
func (s Stale) Empty() bool {
	return len(s.Keys) == 0 && len(s.Devices) == 0 && len(s.Ingress) == 0 && len(s.Egress) == 0
}

// StaleApplied is the one answer to "what did tink apply, that the stack no longer declares?", asked by plan (to show it) and by apply (to do it), so the
// two cannot disagree. live is the object as it was just read, rec its record (from Trusted: the caller asks only for one that is true of the object) and
// declared what the stack declares now.
//
// Only what rec lists can be returned. A key or a device that the object has and the record does not list was put there by someone else, and is never
// returned, however absent from the declaration it is; plan reports it as an extra. A key the declaration gives, even with an empty value, is declared, so
// it is not returned. A key must also be one a person's declaration could have set: tink's own keys (intent and outcome) and Incus's volatile.* are never
// returned, even from a record that lists them.
func StaleApplied(live State, rec Record, declared State) Stale {
	var out Stale
	listed := map[string]bool{}
	for k := range rec.Config {
		listed[k] = true
	}
	for _, k := range rec.ConfigNames {
		listed[k] = true
	}
	for _, k := range sortedKeys(live.Config) {
		if _, kept := declared.Config[k]; kept || !listed[k] || !removable(k) {
			continue
		}
		out.Keys = append(out.Keys, k)
	}
	isListed := func(name string) bool {
		if _, ok := rec.Devices[name]; ok {
			return true
		}
		for _, n := range rec.DeviceNames {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, name := range sortedKeys(live.Devices) {
		if _, kept := declared.Devices[name]; !kept && isListed(name) {
			out.Devices = append(out.Devices, name)
		}
	}
	if declared.Ingress == nil {
		out.Ingress = liveRulesIn(live.Ingress, rec.Ingress)
	}
	if declared.Egress == nil {
		out.Egress = liveRulesIn(live.Egress, rec.Egress)
	}
	return out
}

// removable says that a key is one tink may take back: not one it writes itself, and not Incus's own.
func removable(k string) bool {
	return !ownership.WrittenByTink(k) && !strings.HasPrefix(k, "volatile.")
}

// liveRulesIn is the rules of live that listed also holds, in live's order. A rule that is in live twice and in listed once is returned twice: Incus
// refuses a repeated rule, so the question does not arise on a real server.
func liveRulesIn(live, listed []Rule) []Rule {
	var out []Rule
	for _, r := range live {
		for _, l := range listed {
			if r == l {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// namesOf is the names of a map's keys and of a list that is already names, sorted and without repeats.
func namesOf[V any](m map[string]V, names []string) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	for _, n := range names {
		set[n] = true
	}
	return sortedKeys(set)
}

func sortedKeys[V any](m map[string]V) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
