package applied

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/ownership"
)

func secretWords(k string) bool {
	return strings.Contains(k, "PASSWORD") || strings.Contains(k, "token")
}

func TestARecordRoundTripsForEachShapeOfObject(t *testing.T) {
	rule := Rule{Action: "allow", Source: "192.0.2.0/24", Protocol: "tcp", DestinationPort: "443", State: "enabled"}
	for name, in := range map[string]Input{
		"instance": {Config: map[string]string{"limits.cpu": "2", "user.note": "a b"}, Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "br0"}}, Profiles: []string{"default", "web"}},
		"profile":  {Config: map[string]string{"limits.memory": "1GiB"}, Devices: map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}},
		"volume":   {Config: map[string]string{"size": "10GiB"}},
		"network":  {Config: map[string]string{"ipv4.address": "192.0.2.1/24", "ipv4.nat": "true"}},
		"acl":      {Config: map[string]string{}, Ingress: []Rule{rule}, Egress: []Rule{}},
	} {
		in.Withhold = secretWords
		rec, rep := Build(in)
		if !rep.Empty() {
			t.Errorf("%s: report %+v", name, rep)
		}
		got, err := Parse(Encode(rec))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if Encode(got) != Encode(rec) {
			t.Errorf("%s: %s -> %s", name, Encode(rec), Encode(got))
		}
		if got.Proto != Proto {
			t.Errorf("%s: proto %d", name, got.Proto)
		}
	}
	// what is in it
	rec, _ := Build(Input{Config: map[string]string{"limits.cpu": "2"}, Devices: map[string]map[string]string{"eth0": {"type": "nic"}}, Profiles: []string{"default"}, Withhold: secretWords})
	if rec.Config["limits.cpu"] != "2" || rec.Devices["eth0"]["type"] != "nic" || !reflect.DeepEqual(rec.Profiles, []string{"default"}) {
		t.Errorf("record = %s", Encode(rec))
	}
}

func TestTheSameInputAlwaysEncodesToTheSameText(t *testing.T) {
	in := Input{Config: map[string]string{"b": "2", "a": "1", "c": "3"}, Devices: map[string]map[string]string{"z": {"y": "1", "x": "2"}, "a": {"type": "nic"}}, Withhold: func(string) bool { return false }}
	want := ""
	for i := 0; i < 20; i++ {
		rec, _ := Build(in)
		if got := Encode(rec); want == "" {
			want = got
		} else if got != want {
			t.Fatalf("encoding changed: %s vs %s", got, want)
		}
	}
}

// A value that holds a secret, or whose name looks like one, is listed by name and the value is nowhere in the text.
func TestWithheldValuesAreRecordedByNameOnly(t *testing.T) {
	in := Input{
		Config:   map[string]string{"environment.DB_PASSWORD": "hunter2-value", "environment.API_token": "tok-value", "environment.MODE": "fast", "limits.cpu": "2"},
		Devices:  map[string]map[string]string{"vault": {"type": "disk", "source": "x", "PASSWORD": "dev-secret-value"}, "eth0": {"type": "nic"}},
		Withhold: secretWords,
	}
	rec, _ := Build(in)
	text := Encode(rec)
	for _, leak := range []string{"hunter2-value", "tok-value", "dev-secret-value"} {
		if strings.Contains(text, leak) {
			t.Errorf("the record holds %q: %s", leak, text)
		}
	}
	if !reflect.DeepEqual(rec.ConfigNames, []string{"environment.API_token", "environment.DB_PASSWORD"}) {
		t.Errorf("names = %v", rec.ConfigNames)
	}
	if rec.Config["environment.MODE"] != "fast" || rec.Config["limits.cpu"] != "2" {
		t.Errorf("plain keys are kept with their values: %v", rec.Config)
	}
	if !reflect.DeepEqual(rec.DeviceNames, []string{"vault"}) || rec.Devices["eth0"] == nil {
		t.Errorf("devices = %v names %v", rec.Devices, rec.DeviceNames)
	}
}

func TestACallerThatSaysNothingWithholdsEveryValue(t *testing.T) {
	rec, _ := Build(Input{Config: map[string]string{"limits.cpu": "2"}})
	if len(rec.Config) != 0 || !reflect.DeepEqual(rec.ConfigNames, []string{"limits.cpu"}) {
		t.Errorf("record = %s", Encode(rec))
	}
}

// Incus does not store an empty value (measured on 7.5.1): the key is absent after the write, so the record never lists it.
func TestAnEmptyValueIsNotRecordedAndNeverStale(t *testing.T) {
	rec, _ := Build(Input{Config: map[string]string{"limits.cpu": "", "user.keep": "x"}, Withhold: func(string) bool { return false }})
	if _, ok := rec.Config["limits.cpu"]; ok || len(rec.ConfigNames) != 0 {
		t.Fatalf("an empty value was recorded: %s", Encode(rec))
	}
	// an earlier record that listed it (a value applied before), a declaration that now says "" and an object that no longer has it: nothing stale
	old := Record{Proto: Proto, Config: map[string]string{"limits.cpu": "2"}}
	st := StaleApplied(State{Config: map[string]string{}}, old, State{Config: map[string]string{"limits.cpu": ""}})
	if !st.Empty() {
		t.Errorf("stale = %+v", st)
	}
	// and the declared "" keeps the key out of the stale list even when the object still has the old value
	st = StaleApplied(State{Config: map[string]string{"limits.cpu": "2"}}, old, State{Config: map[string]string{"limits.cpu": ""}})
	if !st.Empty() {
		t.Errorf("a key declared empty is declared: %+v", st)
	}
}

func TestTheSizeRule(t *testing.T) {
	big := strings.Repeat("x", MaxValue+1)
	ok := strings.Repeat("x", MaxValue)
	rec, rep := Build(Input{
		Config:   map[string]string{"user.big": big, "user.ok": ok},
		Devices:  map[string]map[string]string{"d": {"type": "disk", "source": big}},
		Withhold: func(string) bool { return false },
	})
	if rec.Config["user.ok"] != ok {
		t.Error("a value of exactly MaxValue is kept")
	}
	if _, in := rec.Config["user.big"]; in || !reflect.DeepEqual(rec.ConfigNames, []string{"user.big"}) || !reflect.DeepEqual(rec.DeviceNames, []string{"d"}) {
		t.Errorf("record = names %v %v", rec.ConfigNames, rec.DeviceNames)
	}
	if !reflect.DeepEqual(rep.Large, []string{"config.user.big", "device.d"}) || rep.Cut || rep.None {
		t.Errorf("report = %+v", rep)
	}
	if strings.Contains(Encode(rec), big) {
		t.Error("an oversized value is in the record")
	}
}

func TestARecordOverTheLimitIsCutToNames(t *testing.T) {
	cfg := map[string]string{}
	for i := 0; i < 40; i++ {
		cfg[fmt.Sprintf("user.k%02d", i)] = strings.Repeat("v", MaxValue)
	}
	rec, rep := Build(Input{Config: cfg, Profiles: []string{"p"}, Ingress: []Rule{{Action: "allow", State: "enabled"}}, Withhold: func(string) bool { return false }})
	if !rep.Cut || rep.None || !rec.Cut {
		t.Fatalf("report = %+v cut %v", rep, rec.Cut)
	}
	if len(rec.Config) != 0 || len(rec.ConfigNames) != 40 || len(rec.Profiles) != 0 || len(rec.Ingress) != 0 {
		t.Errorf("a cut record keeps names only: %d values, %d names", len(rec.Config), len(rec.ConfigNames))
	}
	if len(Encode(rec)) > MaxRecord {
		t.Error("the cut record is still too long")
	}
	// too many names even so: nothing can be written
	huge := map[string]string{}
	for i := 0; i < 4000; i++ {
		huge[fmt.Sprintf("user.key-%05d-%s", i, strings.Repeat("n", 20))] = "v"
	}
	if _, rep := Build(Input{Config: huge, Withhold: func(string) bool { return false }}); !rep.None || !rep.Cut {
		t.Errorf("report = %+v, want none", rep)
	}
}

func TestTinkOwnKeysAreNotPartOfTheRecord(t *testing.T) {
	rec, _ := Build(Input{Config: map[string]string{
		ownership.Key: "lab", ownership.ResourceKey: "x", ownership.AppliedKey: "y", "user.tink.intent.publish.web": "g", "user.tink.outcome.exec.a": "h", "user.tink.role": "proxy", "limits.cpu": "1",
	}, Withhold: func(string) bool { return false }})
	if len(rec.Config) != 2 || rec.Config["user.tink.role"] != "proxy" || rec.Config["limits.cpu"] != "1" {
		t.Errorf("config = %v (a fact is a person's declaration and is recorded; intent and outcome are not)", rec.Config)
	}
}

func TestAnUnreadableRecordIsAnErrorAndAbsent(t *testing.T) {
	for _, s := range []string{"", "not json", "{}", `{"proto":0}`, `{"proto":99,"config":{"a":"b"}}`, `[1]`, `{"proto":"1"}`} {
		if _, err := Parse(s); err != ErrUnreadable {
			t.Errorf("Parse(%q) = %v", s, err)
		}
	}
	here := func(raw string) map[string]string { return map[string]string{ownership.AppliedKey: raw} }
	if _, r := Trusted(here("garbage"), ownership.Here); r != Unreadable {
		t.Errorf("reading = %v", r)
	}
	if _, r := Trusted(map[string]string{}, ownership.Here); r != Absent {
		t.Errorf("reading = %v", r)
	}
	if rec, r := Trusted(here(Encode(Record{Proto: Proto, Config: map[string]string{"a": "b"}})), ownership.Here); r != Present || rec.Config["a"] != "b" {
		t.Errorf("reading = %v %v", r, rec)
	}
}

// The record is true of the object it was written for and of nothing else.
func TestARecordIsTrustedOnlyOnAnObjectStampedHere(t *testing.T) {
	cfg := map[string]string{ownership.AppliedKey: Encode(Record{Proto: Proto, Config: map[string]string{"a": "b"}})}
	for _, st := range []ownership.State{ownership.Unknown, ownership.Unstamped, ownership.Elsewhere, ownership.Copy, ownership.Unbound} {
		if rec, r := Trusted(cfg, st); r != Absent || rec.Config != nil {
			t.Errorf("state %v: reading %v %v", st, r, rec)
		}
	}
	if _, r := Trusted(cfg, ownership.Here); r != Present {
		t.Errorf("here: %v", r)
	}
}

func TestStaleAppliedReturnsOnlyWhatTheRecordListsAndTheDeclarationLacks(t *testing.T) {
	rec := Record{Proto: Proto,
		Config:      map[string]string{"limits.cpu": "2", "limits.memory": "1GiB", "user.kept": "k"},
		ConfigNames: []string{"environment.DB_PASSWORD"},
		Devices:     map[string]map[string]string{"eth1": {"type": "nic"}, "eth0": {"type": "nic"}}, DeviceNames: []string{"vault"},
	}
	live := State{
		Config:  map[string]string{"limits.cpu": "2", "limits.memory": "1GiB", "user.kept": "k", "environment.DB_PASSWORD": "x", "user.by-hand": "h", "limits.processes": "9", "volatile.eth0.hwaddr": "aa"},
		Devices: map[string]map[string]string{"eth0": {"type": "nic"}, "eth1": {"type": "nic"}, "vault": {"type": "disk"}, "hand": {"type": "usb"}},
	}
	declared := State{Config: map[string]string{"user.kept": "k"}, Devices: map[string]map[string]string{"eth0": {"type": "nic"}}}
	st := StaleApplied(live, rec, declared)
	if !reflect.DeepEqual(st.Keys, []string{"environment.DB_PASSWORD", "limits.cpu", "limits.memory"}) {
		t.Errorf("keys = %v (by-hand keys and Incus's own are never returned)", st.Keys)
	}
	if !reflect.DeepEqual(st.Devices, []string{"eth1", "vault"}) {
		t.Errorf("devices = %v (a device added by hand is never returned)", st.Devices)
	}
	// no record, nothing
	if st := StaleApplied(live, Record{}, declared); !st.Empty() {
		t.Errorf("without a record: %+v", st)
	}
	// a key the record lists that the object no longer has is not stale (nothing to take back)
	if st := StaleApplied(State{Config: map[string]string{}}, rec, State{}); !st.Empty() {
		t.Errorf("absent keys: %+v", st)
	}
}

func TestStaleAppliedNeverReturnsTinksOwnOrIncusKeysEvenFromARecordThatListsThem(t *testing.T) {
	rec := Record{Proto: Proto, Config: map[string]string{ownership.Key: "lab", ownership.AppliedKey: "x", "user.tink.intent.publish.a": "g", "user.tink.outcome.x": "y", "volatile.base_image": "z"}}
	live := State{Config: map[string]string{ownership.Key: "lab", ownership.AppliedKey: "x", "user.tink.intent.publish.a": "g", "user.tink.outcome.x": "y", "volatile.base_image": "z"}}
	if st := StaleApplied(live, rec, State{}); !st.Empty() {
		t.Errorf("stale = %+v", st)
	}
}

func TestStaleAppliedRulesOnlyForADirectionTheDeclarationDoesNotGive(t *testing.T) {
	a := Rule{Action: "allow", Source: "192.0.2.0/24", State: "enabled"}
	b := Rule{Action: "drop", Source: "198.51.100.0/24", State: "enabled"}
	hand := Rule{Action: "allow", Source: "203.0.113.0/24", State: "enabled"}
	rec := Record{Proto: Proto, Ingress: []Rule{a, b}, Egress: []Rule{a}}
	live := State{Ingress: []Rule{a, hand, b}, Egress: []Rule{a, hand}}
	// ingress is declared (empty: "none"), egress is not given
	st := StaleApplied(live, rec, State{Ingress: []Rule{}, Egress: nil})
	if len(st.Ingress) != 0 {
		t.Errorf("a declared direction is replaced as a whole by an apply, not removed rule by rule: %v", st.Ingress)
	}
	if !reflect.DeepEqual(st.Egress, []Rule{a}) {
		t.Errorf("egress = %v (the rule added by hand stays)", st.Egress)
	}
	st = StaleApplied(live, rec, State{})
	if !reflect.DeepEqual(st.Ingress, []Rule{a, b}) {
		t.Errorf("ingress = %v", st.Ingress)
	}
}
