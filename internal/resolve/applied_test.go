package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/applied"
	"github.com/minihci/tink/internal/ownership"
)

// The applied record (issue #312): what tink applied to an object, kept on the object, in the same write as the config. These tests run every kind that
// has one against the fake server of ownership_test.go.

var recordedKinds = []Kind{KindProfile, KindStorageVolume, KindInstance, KindNetwork, KindNetworkACL}

func quiet(string, ...any) {}

// declare is the declaration of an object named "p" of the kind with the given config, devices and (for an ACL) ingress rules.
func declare(kind Kind, config map[string]string, devices map[string]map[string]string, ingress []api.NetworkACLRule) Resource {
	r := stampedResource(kind)
	r.Config = config
	if kind == KindInstance || kind == KindProfile {
		r.Devices = devices
	}
	if kind == KindNetworkACL {
		r.Ingress = ingress
	}
	return r
}

// leftBy puts an object of the kind into the fake the way an earlier apply of before left it (stamped here, with before's record), plus what a person added
// by hand afterwards: config keys, devices and, for an ACL, ingress rules.
func (f *ownFake) leftBy(before Resource, hand map[string]string, handDevices map[string]map[string]string, handRules []api.NetworkACLRule) {
	cfg := stampFor(stampedHere, before.Config, addressOf(before))
	if rec, _, ok := appliedRecordFor(before, "lab"); ok {
		cfg[ownership.AppliedKey] = rec
	}
	for k, v := range hand {
		cfg[k] = v
	}
	devices := api.DevicesMap{}
	for n, d := range before.Devices {
		devices[n] = d
	}
	for n, d := range handDevices {
		devices[n] = d
	}
	switch before.Kind {
	case KindProfile:
		f.profiles["p"] = &api.Profile{Name: "p", ProfilePut: api.ProfilePut{Config: cfg, Devices: devices}}
	case KindStorageVolume:
		f.volumes["default/p"] = &api.StorageVolume{Name: "p", Type: "custom", StorageVolumePut: api.StorageVolumePut{Config: cfg}}
	case KindInstance:
		f.instances["p"] = &api.Instance{Name: "p", Type: "container", Status: "Running", StatusCode: api.Running, InstancePut: api.InstancePut{Config: cfg, Devices: devices, Profiles: []string{"default"}}}
	case KindNetwork:
		f.networks["p"] = &api.Network{Name: "p", Type: "bridge", Managed: true, Status: "Created", NetworkPut: api.NetworkPut{Config: cfg}}
	case KindNetworkACL:
		f.acls["p"] = &api.NetworkACL{NetworkACLPost: api.NetworkACLPost{Name: "p"}, NetworkACLPut: api.NetworkACLPut{Config: cfg, Ingress: aclRules(append(append([]api.NetworkACLRule(nil), before.Ingress...), handRules...))}}
	}
}

func (f *ownFake) devicesOf(kind Kind) map[string]map[string]string {
	switch kind {
	case KindProfile:
		return f.profiles["p"].Devices
	case KindInstance:
		return f.instances["p"].Devices
	}
	return nil
}

func hasLine(lines []string, parts ...string) bool {
	for _, l := range lines {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(l, p)
		}
		if ok {
			return true
		}
	}
	return false
}

// A creation request carries the record of what it applies, in the one call that carries the config: the request of a profile, a volume, a network and an
// ACL, and for an instance the one PUT that sets its config (the creation request holds none of it, so a crash between the two cannot leave a record of
// config that was never applied). A stack with no name writes none, because it owns nothing. A kind that is create-only has none.
func TestTheRecordIsWrittenInTheSameCallAsTheConfigOfEveryCreate(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	for _, kind := range stampedKinds {
		for _, stack := range []string{"lab", ""} {
			t.Run(string(kind)+"/"+stack, func(t *testing.T) {
				f := &creationFake{ownFake: newOwnFake()}
				f.aliases["local-image"] = "fp0"
				f.images["fp0"] = &api.Image{Fingerprint: "fp0"}
				r := declare(kind, map[string]string{"user.note": "x", "limits.cpu": "2"}, map[string]map[string]string{"eth0": {"type": "nic", "network": "br0"}}, []api.NetworkACLRule{{Action: "allow", Source: "192.0.2.0/24", State: "enabled"}})
				if kind == KindInstance {
					r.Image = "local-image"
				}
				if kind == KindImage {
					r.Source = filepath.Join(t.TempDir(), "disk.img")
					r.Config = nil
					if err := os.WriteFile(r.Source, []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := createOne(f, r, volumeEnv{targets: map[string]Resource{}, stack: stack}); err != nil {
					t.Fatal(err)
				}
				var got string
				var wrote bool
				if kind == KindInstance {
					if _, in := f.requested[ownership.AppliedKey]; in {
						t.Error("the creation request of an instance carries the record, but not the config it describes")
					}
					got, wrote = f.instances["p"].Config[ownership.AppliedKey]
					if len(f.updated) != 1 {
						t.Errorf("want the one PUT that carries the config, got %v", f.updated)
					}
				} else {
					got, wrote = f.createdConfig[f.created[0]][ownership.AppliedKey]
				}
				want, _, ok := appliedRecordFor(r, stack)
				switch {
				case !recordedKind(kind) || stack == "":
					if wrote {
						t.Errorf("a record was written for a %s in a stack named %q: %q", kind, stack, got)
					}
				case !wrote || !ok || got != want:
					t.Errorf("the record written is %q (written %v), want %q", got, wrote, want)
				default:
					parsed, err := applied.Parse(got)
					if err != nil || parsed.Config["limits.cpu"] != "2" {
						t.Errorf("the record does not say what was applied: %q, %v", got, err)
					}
					if kind == KindInstance && !reflect.DeepEqual(parsed.Devices["eth0"], map[string]string{"type": "nic", "network": "br0"}) {
						t.Errorf("the record does not hold the device block: %q", got)
					}
					if kind == KindNetworkACL && len(parsed.Ingress) != 1 {
						t.Errorf("the record does not hold the rules: %q", got)
					}
				}
			})
		}
	}
}

// creationFake keeps the config of the creation request of an instance as it was sent: the fake's own copy is the very map the next PUT changes.
type creationFake struct {
	*ownFake
	requested map[string]string
}

func (f *creationFake) UseProject(string) incus.InstanceServer { return f }

func (f *creationFake) CreateInstanceFromImage(img incus.ImageServer, info api.Image, req api.InstancesPost) (incus.RemoteOperation, error) {
	f.requested = map[string]string{}
	for k, v := range req.Config {
		f.requested[k] = v
	}
	return f.ownFake.CreateInstanceFromImage(img, info, req)
}

// A real change to an object this stack owns carries the new record in the same write, for every kind that has one.
func TestTheRecordIsWrittenInTheSameWriteAsEveryUpdate(t *testing.T) {
	for _, kind := range recordedKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			before := declare(kind, map[string]string{"user.note": "old", "user.gone": "g"}, map[string]map[string]string{"eth0": {"type": "nic"}}, nil)
			f.leftBy(before, nil, nil, nil)
			now := declare(kind, map[string]string{"user.note": "new"}, nil, nil)
			if kind == KindNetwork {
				now.AllowDisruption = true
			}
			out, err := applyOne(f, now, ownOpts("lab"), quiet)
			if err != nil || out != outChanged {
				t.Fatalf("outcome %v, %v", out, err)
			}
			want, _, _ := appliedRecordFor(now, "lab")
			if got := f.configOf(kind)[ownership.AppliedKey]; got != want || want == "" {
				t.Errorf("the object carries the record %q, want %q", got, want)
			}
			if len(f.updated) != 1 || len(f.patches) != 0 {
				t.Errorf("want one write, got updates %v patches %v", f.updated, f.patches)
			}
		})
	}
}

// An ordinary apply on an object this stack owns that has no record, or a stale one, and nothing else to change, writes the record and restarts nothing:
// the record is a stamp, and a stamp is not a change.
func TestAnOrdinaryApplyOnAnOwnedObjectWritesAMissingOrStaleRecordAndRestartsNothing(t *testing.T) {
	for _, state := range []string{hereNoRecord, "stale record"} {
		for _, kind := range recordedKinds {
			t.Run(state+"/"+string(kind), func(t *testing.T) {
				f := newOwnFake()
				r := f.seed(kind, hereNoRecord)
				r.Restart = true // the record is not worth a restart, and does not read this
				if state != hereNoRecord {
					f.configOf(kind)[ownership.AppliedKey] = applied.Encode(applied.Record{Proto: applied.Proto, Config: map[string]string{"user.old": "v"}})
				}
				p, err := planOne(f, r, ownOpts("lab"))
				if err != nil || p.Action != ActionNone || len(p.Changes) != 0 {
					t.Fatalf("a missing or out-of-date record must not be a change: %v %v %v", p.Action, p.Changes, err)
				}
				if want := map[string]RecordState{hereNoRecord: RecordMissing, "stale record": RecordChanged}[state]; p.Ownership.Record != want {
					t.Errorf("the record reads %v, want %v", p.Ownership.Record, want)
				}
				out, err := applyOne(f, r, ownOpts("lab"), quiet)
				if err != nil || out != outConverged {
					t.Fatalf("outcome %v, %v", out, err)
				}
				want, _, _ := appliedRecordFor(r, "lab")
				if got := f.configOf(kind)[ownership.AppliedKey]; got != want || want == "" {
					t.Errorf("the record is %q, want %q", got, want)
				}
				if len(f.stateChanges) != 0 {
					t.Errorf("writing the record restarted %v", f.stateChanges)
				}
				if kind == KindInstance && (len(f.updated) != 0 || len(f.patches) != 1) {
					t.Errorf("an instance's record is a PATCH of the key, not a whole-instance write: updated %v patches %v", f.updated, f.patches)
				}
				if len(f.created) != 0 {
					t.Errorf("created %v", f.created)
				}
				before := f.writes()
				if p, _ := planOne(f, r, ownOpts("lab")); p.Ownership.Record != RecordCurrent {
					t.Errorf("after the write the record reads %v", p.Ownership.Record)
				}
				if _, err := applyOne(f, r, ownOpts("lab"), quiet); err != nil || f.writes() != before {
					t.Errorf("a second apply wrote again: %v", err)
				}
			})
		}
	}
}

// A record is only ever written for an object that is this stack's, and never over another stack's, a copy's or an unstamped object's.
func TestARecordIsNotWrittenOverObjectsThatAreNotThisStacks(t *testing.T) {
	for _, state := range []string{unstamped, stampedElsewh, stampedCopy} {
		for _, kind := range recordedKinds {
			t.Run(state+"/"+string(kind), func(t *testing.T) {
				f := newOwnFake()
				r := f.seed(kind, state)
				r.Config["user.note"] = "changed" // a real change, so that the update path runs
				if kind == KindNetworkACL {
					r.Config = map[string]string{"user.note": "changed"}
				}
				if kind == KindNetwork {
					r.AllowDisruption = true
				}
				if _, err := applyOne(f, r, ownOpts("lab"), quiet); err != nil {
					t.Fatal(err)
				}
				if _, in := f.configOf(kind)[ownership.AppliedKey]; in {
					t.Errorf("a record was written on an object that is %s: %v", state, f.configOf(kind))
				}
			})
		}
	}
}

// Removing a declared key or device shows as a planned removal, and apply makes exactly those removals: what a person added by hand stays, and is
// reported as an extra, as before.
func TestARemovedKeyOrDeviceIsPlannedAndAppliedAndWhatWasAddedByHandStays(t *testing.T) {
	nic := map[string]string{"type": "nic", "network": "br0"}
	for _, kind := range recordedKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			before := declare(kind, map[string]string{"user.keep": "1", "user.gone": "2"}, map[string]map[string]string{"keep": nic, "gone": nic}, nil)
			f.leftBy(before, map[string]string{"user.hand": "3"}, map[string]map[string]string{"hand": {"type": "usb"}}, nil)
			now := declare(kind, map[string]string{"user.keep": "1"}, map[string]map[string]string{"keep": nic}, nil)
			if kind == KindNetwork {
				now.AllowDisruption = true
			}

			opts := ownOpts("lab").WithExtras()
			p, err := planOne(f, now, opts)
			if err != nil || p.Action != ActionUpdate {
				t.Fatalf("plan = %v %v %v", p.Action, p.Changes, err)
			}
			if !hasLine(p.Changes, "config.user.gone", `"2" -> (unset)`, staleWhy) {
				t.Errorf("the removed key is not a planned removal: %q", p.Changes)
			}
			hasDevices := kind == KindInstance || kind == KindProfile
			if hasDevices && !hasLine(p.Changes, "device.gone: removed", staleWhy) {
				t.Errorf("the removed device is not a planned removal: %q", p.Changes)
			}
			if hasLine(p.Changes, "user.hand") || hasLine(p.Changes, "device.hand") || hasLine(p.Changes, "device.keep") {
				t.Errorf("something that was not applied before, or is still declared, is planned for removal: %q", p.Changes)
			}
			if kind != KindNetworkACL && p.Extras == nil {
				t.Errorf("no extras report")
			} else if p.Extras != nil && !reflect.DeepEqual(p.Extras.Config, []string{"user.hand"}) {
				t.Errorf("the key added by hand is reported as an extra, as today: %v", p.Extras.Config)
			}
			if hasLine(p.Changes, ownership.AppliedKey) {
				t.Errorf("the record is never a change line: %q", p.Changes)
			}

			out, err := applyOne(f, now, ownOpts("lab"), quiet)
			if err != nil || out != outChanged {
				t.Fatalf("outcome %v, %v", out, err)
			}
			got := f.configOf(kind)
			if _, in := got["user.gone"]; in || got["user.keep"] != "1" || got["user.hand"] != "3" {
				t.Errorf("after the apply the object carries %v: want gone removed, keep and the hand-made key kept", got)
			}
			if hasDevices {
				devs := f.devicesOf(kind)
				if _, in := devs["gone"]; in || devs["keep"] == nil || devs["hand"] == nil {
					t.Errorf("after the apply the devices are %v: want gone removed, keep and the hand-made device kept", devs)
				}
			}
			rec, _, _ := appliedRecordFor(now, "lab")
			if got[ownership.AppliedKey] != rec {
				t.Errorf("the record was not rewritten with the removal")
			}
			// a plan after it is quiet
			if p, _ := planOne(f, now, ownOpts("lab")); p.Action != ActionNone || p.Ownership.Record != RecordCurrent {
				t.Errorf("a second plan: %v %v %v", p.Action, p.Changes, p.Ownership.Record)
			}
		})
	}
}

// A direction of an ACL the stack no longer gives: the rules tink applied there are taken back, a rule added by hand is not.
func TestAnACLDirectionNoLongerDeclaredLosesWhatTinkAppliedThereAndNothingElse(t *testing.T) {
	a := api.NetworkACLRule{Action: "allow", Source: "192.0.2.0/24", State: "enabled"}
	hand := api.NetworkACLRule{Action: "allow", Source: "198.51.100.0/24", State: "enabled"}
	f := newOwnFake()
	f.leftBy(declare(KindNetworkACL, map[string]string{}, nil, []api.NetworkACLRule{a}), nil, nil, []api.NetworkACLRule{hand})
	now := declare(KindNetworkACL, map[string]string{}, nil, nil)
	p, err := planOne(f, now, ownOpts("lab"))
	if err != nil || p.Action != ActionUpdate || !hasLine(p.Changes, "ingress rule", "remove", "action=allow source=192.0.2.0/24", staleWhy) || hasLine(p.Changes, "198.51.100.0/24") {
		t.Fatalf("plan = %v %q %v", p.Action, p.Changes, err)
	}
	if _, err := applyOne(f, now, ownOpts("lab"), quiet); err != nil {
		t.Fatal(err)
	}
	if got := f.acls["p"].Ingress; !reflect.DeepEqual(got, []api.NetworkACLRule{hand}) {
		t.Errorf("the live ingress rules are %v, want only the one added by hand", got)
	}
}

// Without a record there is nothing to remove, which is what makes a first apply on an existing object safe: a key that is not declared is an extra.
func TestWithoutARecordNothingIsRemoved(t *testing.T) {
	for _, kind := range recordedKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			before := declare(kind, map[string]string{"user.keep": "1", "user.gone": "2"}, nil, nil)
			f.leftBy(before, nil, nil, nil)
			delete(f.configOf(kind), ownership.AppliedKey)
			now := declare(kind, map[string]string{"user.keep": "1"}, nil, nil)
			p, err := planOne(f, now, ownOpts("lab"))
			if err != nil || p.Action != ActionNone || len(p.Changes) != 0 {
				t.Fatalf("plan = %v %q %v", p.Action, p.Changes, err)
			}
			if _, err := applyOne(f, now, ownOpts("lab"), quiet); err != nil {
				t.Fatal(err)
			}
			if f.configOf(kind)["user.gone"] != "2" {
				t.Errorf("a key was removed with no record to say tink applied it: %v", f.configOf(kind))
			}
			if got := f.configOf(kind)[ownership.AppliedKey]; got == "" {
				t.Error("the first apply writes the record, for the next one to use")
			}
		})
	}
}

// The record is true of the object it was written for: on a copy, an object with no resource stamp, an unstamped one and another stack's, it is ignored,
// in a plan and in an apply.
func TestARecordIsIgnoredOnAnObjectWhoseResourceStampDoesNotMatch(t *testing.T) {
	for _, state := range []string{stampedCopy, unbound, unstamped, stampedElsewh} {
		for _, kind := range recordedKinds {
			t.Run(state+"/"+string(kind), func(t *testing.T) {
				f := newOwnFake()
				before := declare(kind, map[string]string{"user.keep": "1", "user.gone": "2"}, map[string]map[string]string{"gone": {"type": "nic"}}, nil)
				f.leftBy(before, nil, nil, nil)
				// the same record, on an object that is not what it was written for
				cfg := f.configOf(kind)
				switch state {
				case stampedCopy:
					cfg[ownership.ResourceKey] += "-of-another"
				case unbound:
					delete(cfg, ownership.ResourceKey)
				case unstamped:
					delete(cfg, ownership.Key)
					delete(cfg, ownership.ResourceKey)
				case stampedElsewh:
					cfg[ownership.Key] = "other"
				}
				now := declare(kind, map[string]string{"user.keep": "1"}, nil, nil)
				if kind == KindNetwork {
					now.AllowDisruption = true
				}
				p, err := planOne(f, now, ownOpts("lab"))
				if err != nil {
					t.Fatal(err)
				}
				if hasLine(p.Changes, staleWhy) || p.Ownership.Record != RecordNone {
					t.Errorf("the record was used: %q, reads %v", p.Changes, p.Ownership.Record)
				}
				if p.Action == ActionUpdate {
					t.Errorf("an ignored record caused an update: %q", p.Changes)
				}
				before2 := f.configOf(kind)["user.gone"]
				if _, err := applyOne(f, now, ownOpts("lab"), quiet); err != nil {
					t.Fatal(err)
				}
				if f.configOf(kind)["user.gone"] != before2 || before2 != "" && before2 != "2" {
					t.Errorf("apply removed through a record that is not true of the object")
				}
				if state != unbound && state != stampedElsewh && state != unstamped && f.writes() != 0 {
					t.Errorf("a copy was written: %v", f.updated)
				}
			})
		}
	}
}

// A record that cannot be read is treated as absent, said in one fixed phrase, and replaced by the next apply; nothing is removed because of it.
func TestARecordThatCannotBeReadIsAbsentAndSaidSo(t *testing.T) {
	for _, raw := range []string{"not json", `{"proto":99,"config":{"user.gone":"2"}}`, "{}"} {
		t.Run(raw, func(t *testing.T) {
			f := newOwnFake()
			before := declare(KindProfile, map[string]string{"user.keep": "1", "user.gone": "2"}, nil, nil)
			f.leftBy(before, nil, nil, nil)
			f.profiles["p"].Config[ownership.AppliedKey] = raw
			now := declare(KindProfile, map[string]string{"user.keep": "1"}, nil, nil)
			p, err := planOne(f, now, ownOpts("lab"))
			if err != nil || p.Action != ActionNone || p.Ownership.Record != RecordUnreadable {
				t.Fatalf("plan = %v %q %v reads %v", p.Action, p.Changes, err, p.Ownership.Record)
			}
			report := NewOwnershipReport("lab", false)
			report.Add(p)
			lines := report.Lines()
			if len(lines) != 1 || !strings.Contains(lines[0], reasonRecordUnreadable) || strings.Contains(lines[0], raw) {
				t.Errorf("lines = %q", lines)
			}
			if _, err := applyOne(f, now, ownOpts("lab"), quiet); err != nil {
				t.Fatal(err)
			}
			want, _, _ := appliedRecordFor(now, "lab")
			if cfg := f.profiles["p"].Config; cfg["user.gone"] != "2" || cfg[ownership.AppliedKey] != want {
				t.Errorf("after the apply: %v", cfg)
			}
		})
	}
}

// Incus does not store an empty value (lab, Incus 7.5.1, 2026-11-09): a key declared "" is absent from the object after an apply, and that is neither drift nor a
// hand removal, and the record does not list it.
func TestAnEmptyValueIsNeverDriftOrAStaleKeyOrInTheRecord(t *testing.T) {
	for _, kind := range recordedKinds {
		t.Run(string(kind), func(t *testing.T) {
			r := declare(kind, map[string]string{"user.empty": "", "user.keep": "1"}, nil, nil)
			rec, _, _ := appliedRecordFor(r, "lab")
			if strings.Contains(rec, "user.empty") {
				t.Fatalf("the record lists a key that has no value: %s", rec)
			}
			// the object as Incus holds it after that apply: no such key
			f := newOwnFake()
			f.leftBy(declare(kind, map[string]string{"user.keep": "1"}, nil, nil), nil, nil, nil)
			p, err := planOne(f, r, ownOpts("lab"))
			if err != nil || p.Action != ActionNone || len(p.Changes) != 0 || p.Ownership.Record != RecordCurrent {
				t.Errorf("an empty declared value reads as drift: %v %q record %v %v", p.Action, p.Changes, p.Ownership.Record, err)
			}
			if _, err := applyOne(f, r, ownOpts("lab"), quiet); err != nil || f.writes() != 0 {
				t.Errorf("an apply wrote for an empty value: %v %v", err, f.updated)
			}
			// a value that was applied and is now declared empty: the change is the existing one (the key is unset), and the key is not also stale
			f2 := newOwnFake()
			f2.leftBy(declare(kind, map[string]string{"user.empty": "was", "user.keep": "1"}, nil, nil), nil, nil, nil)
			p, err = planOne(f2, r, ownOpts("lab"))
			if err != nil || hasLine(p.Changes, staleWhy) {
				t.Errorf("a key declared empty is declared, not stale: %q %v", p.Changes, err)
			}
		})
	}
}

// A value that held a secret reference, or whose name looks like a secret, is recorded by name only. The record, the plan and the apply never carry the value,
// and a removal of such a key is reported without it.
func TestSecretValuedAndSensitiveNamedKeysAreRecordedByNameOnly(t *testing.T) {
	r := declare(KindInstance, map[string]string{"environment.DB_PASSWORD": "value-one-hunter2", "environment.API_TOKEN": "value-two-hunter2", "environment.NAME": "value-three", "limits.cpu": "2"}, nil, nil)
	r.SecretKeys = map[string]bool{"environment.DB_PASSWORD": true}
	r.Config["environment.PLAIN_REF"] = "value-four-hunter2" // came from a ${secret:} reference
	r.SecretKeys["environment.PLAIN_REF"] = true
	rec, _, _ := appliedRecordFor(r, "lab")
	for _, leak := range []string{"value-one-hunter2", "value-two-hunter2", "value-four-hunter2"} {
		if strings.Contains(rec, leak) {
			t.Errorf("the record holds %q: %s", leak, rec)
		}
	}
	parsed, _ := applied.Parse(rec)
	if !reflect.DeepEqual(parsed.ConfigNames, []string{"environment.API_TOKEN", "environment.DB_PASSWORD", "environment.PLAIN_REF"}) || parsed.Config["environment.NAME"] != "value-three" {
		t.Errorf("record = %s", rec)
	}

	// created through the whole path, no value of a secret is in the config of the creation or of the record
	t.Setenv("INCUS_CONF", t.TempDir())
	f := newOwnFake()
	f.aliases["local-image"] = "fp0"
	f.images["fp0"] = &api.Image{Fingerprint: "fp0"}
	r.Image = "local-image"
	r.SecretsExpanded = true
	if _, err := applyOne(f, r, ownOpts("lab"), quiet); err != nil {
		t.Fatal(err)
	}
	if got := f.instances["p"].Config[ownership.AppliedKey]; got != rec {
		t.Errorf("the instance carries %q", got)
	}

	// removing one of them: planned, reported without its value, and performed
	g := newOwnFake()
	g.leftBy(r, map[string]string{"user.hand": "h"}, nil, nil)
	g.instances["p"].Config["environment.DB_PASSWORD"] = "value-one-hunter2"
	g.instances["p"].Config["environment.API_TOKEN"] = "value-two-hunter2"
	now := declare(KindInstance, map[string]string{"limits.cpu": "2"}, nil, nil)
	p, err := planOne(g, now, ownOpts("lab"))
	if err != nil || p.Action != ActionUpdate {
		t.Fatalf("plan = %v %q %v", p.Action, p.Changes, err)
	}
	text := strings.Join(p.Changes, "\n")
	if strings.Contains(text, "hunter2") || !hasLine(p.Changes, "environment.DB_PASSWORD", "(value hidden) -> (unset)") || !hasLine(p.Changes, "environment.API_TOKEN", "(value hidden)") {
		t.Errorf("changes = %s", text)
	}
	if hasLine(p.Changes, "environment.NAME", "(value hidden)") {
		t.Errorf("a plain key is shown as hidden: %s", text)
	}
	if _, err := applyOne(g, now, ownOpts("lab"), quiet); err != nil {
		t.Fatal(err)
	}
	cfg := g.instances["p"].Config
	if _, in := cfg["environment.DB_PASSWORD"]; in || cfg["user.hand"] != "h" {
		t.Errorf("config after = %v", cfg)
	}
}

// The size rule: a value over 2 KiB is recorded by name only and a note says so; the by-name key is still tink's, so removing it works.
func TestAnOversizedValueIsRecordedByNameOnlyAndReported(t *testing.T) {
	big := strings.Repeat("x", applied.MaxValue+1)
	r := declare(KindProfile, map[string]string{"user.big": big, "user.small": "s"}, nil, nil)
	rec, _, ok := appliedRecordFor(r, "lab")
	if !ok || strings.Contains(rec, big) {
		t.Fatalf("record (written %v) holds the value", ok)
	}
	f := newOwnFake()
	f.leftBy(r, nil, nil, nil)
	p, err := planOne(f, r, ownOpts("lab"))
	if err != nil || len(p.Notes()) != 1 || p.Notes()[0].Reason != reasonRecordLarge || !strings.Contains(p.Notes()[0].Detail, "config.user.big") {
		t.Fatalf("notes = %v %v", p.Notes(), err)
	}
	// the note is on a create as well
	if p, _ := planOne(newOwnFake(), r, ownOpts("lab")); len(p.Notes()) != 1 {
		t.Errorf("a create says nothing about the size: %v", p.Findings)
	}
	now := declare(KindProfile, map[string]string{"user.small": "s"}, nil, nil)
	f.profiles["p"].Config["user.big"] = big
	p, _ = planOne(f, now, ownOpts("lab"))
	if !hasLine(p.Changes, "config.user.big", "(value hidden) -> (unset)") || strings.Contains(strings.Join(p.Changes, ""), big) {
		t.Errorf("changes = %q: the removal of a by-name key is shown without its value", p.Changes)
	}
}

func TestARecordOverTheLimitIsCutToNamesAndOneThatCannotBeWrittenIsNot(t *testing.T) {
	cfg := map[string]string{}
	for i := 0; i < 40; i++ {
		cfg["user.k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = strings.Repeat("v", applied.MaxValue)
	}
	r := declare(KindProfile, cfg, nil, nil)
	rec, _, ok := appliedRecordFor(r, "lab")
	parsed, err := applied.Parse(rec)
	if !ok || err != nil || !parsed.Cut || len(parsed.Config) != 0 || len(parsed.ConfigNames) != 40 {
		t.Fatalf("record = %.200s", rec)
	}
	p, _ := planOne(newOwnFake(), r, ownOpts("lab"))
	var found bool
	for _, w := range p.Warnings() {
		found = found || w.Reason == reasonRecordCut
	}
	if !found {
		t.Errorf("findings = %v", p.Findings)
	}

	huge := map[string]string{}
	for i := 0; i < 4000; i++ {
		huge["user.key-"+strings.Repeat("n", 20)+"-"+string(rune('a'+i%26))+string(rune('a'+i/26%26))+string(rune('a'+i/676))] = "v"
	}
	r = declare(KindProfile, huge, nil, nil)
	if _, rep, ok := appliedRecordFor(r, "lab"); ok || !rep.None {
		t.Fatalf("a record over the limit even as names was written (ok %v, %+v)", ok, rep)
	}
	f := newOwnFake()
	f.seed(KindProfile, hereNoRecord)
	if _, err := applyOne(f, r, ownOpts("lab"), quiet); err != nil {
		t.Fatal(err)
	}
	if _, in := f.profiles["p"].Config[ownership.AppliedKey]; in {
		t.Error("a record that cannot be written was written")
	}
}

// The record is shown as a derived stamp: a grouped line says it is missing or changed, and never what it holds.
func TestPlanSaysTheRecordIsMissingOrChangedInGroupedLinesAndNeverItsValue(t *testing.T) {
	f := newOwnFake()
	r := f.seed(KindProfile, hereNoRecord)
	g := newOwnFake()
	r2 := g.seed(KindInstance, stampedHere)
	g.instances["p"].Config[ownership.AppliedKey] = applied.Encode(applied.Record{Proto: applied.Proto, Config: map[string]string{"user.secret-looking": "TOPVALUE"}})
	report := NewOwnershipReport("lab", false)
	for _, pair := range []struct {
		f *ownFake
		r Resource
	}{{f, r}, {g, r2}} {
		p, err := planOne(pair.f, pair.r, ownOpts("lab"))
		if err != nil || p.Action != ActionNone {
			t.Fatalf("%v %v", p.Action, err)
		}
		report.Add(p)
	}
	lines := report.Lines()
	if len(lines) != 2 || !strings.Contains(lines[0], "no record of what was applied yet: profile/p") || !strings.Contains(lines[1], "changed") || !strings.Contains(lines[1], "instance/p") {
		t.Fatalf("lines = %q", lines)
	}
	if strings.Contains(strings.Join(lines, ""), "TOPVALUE") || strings.Contains(strings.Join(lines, ""), "proto") {
		t.Errorf("a line shows the record: %q", lines)
	}
}

// A bridge that instances use is not changed by removing what the stack applied to it without allow_disruption: true, unless it is the ACL attachment.
func TestRemovingAKeyFromABridgeThatInstancesUseNeedsAllowDisruption(t *testing.T) {
	before := declare(KindNetwork, map[string]string{"ipv4.nat": "true", "security.acls": "lan"}, nil, nil)
	for _, c := range []struct {
		name    string
		now     map[string]string
		blocked bool
	}{
		{"a bridge key", map[string]string{"security.acls": "lan"}, true},
		{"the ACL attachment", map[string]string{"ipv4.nat": "true"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOwnFake()
			f.leftBy(before, nil, nil, nil)
			f.networks["p"].UsedBy = []string{"/1.0/instances/app"}
			now := declare(KindNetwork, c.now, nil, nil)
			p, err := planOne(f, now, ownOpts("lab"))
			if err != nil {
				t.Fatal(err)
			}
			if (p.Action == ActionBlocked) != c.blocked || (!c.blocked && p.Action != ActionUpdate) {
				t.Errorf("action = %v %q", p.Action, p.Changes)
			}
			if c.blocked {
				if out, _ := applyOne(f, now, ownOpts("lab"), quiet); out != outBlocked || f.writes() != 0 {
					t.Errorf("apply changed a bridge that instances use: %v %v", out, f.updated)
				}
			}
		})
	}
}

// A volume's snapshot keys belong to its backup block, and a volume's creation-only keys are not applied after creation: neither is in the record, so
// neither is ever taken back by it.
func TestAVolumesBackupKeysAndCreationOnlyKeysAreNotInItsRecord(t *testing.T) {
	r := declare(KindStorageVolume, map[string]string{"size": "1GiB", "initial.uid": "1000"}, nil, nil)
	r.Backup = nil
	rec, _, _ := appliedRecordFor(r, "lab")
	parsed, _ := applied.Parse(rec)
	if !reflect.DeepEqual(parsed.Config, map[string]string{"size": "1GiB"}) {
		t.Errorf("config = %v", parsed.Config)
	}
}

// By its prefix an intent key is refused in a declaration, dropped by export, ignored as an extra and never a stale owned key.
func TestTheRecordIsTreatedAsAnIntentStampEverywhere(t *testing.T) {
	for _, kind := range stampedKinds {
		r := stampedResource(kind)
		if kind == KindImage {
			r.Properties = map[string]string{ownership.AppliedKey: "{}"}
		} else {
			r.Config = map[string]string{ownership.AppliedKey: "{}"}
		}
		if err := Validate(r); err == nil || !strings.Contains(err.Error(), ownership.AppliedKey) {
			t.Errorf("%s: declaring the record must be refused: %v", kind, err)
		}
	}
	e := undeclaredExtras(Resource{Config: map[string]string{ownership.AppliedKey: "{}", "user.mine": "y"}}, Resource{})
	if !reflect.DeepEqual(e.Config, []string{"user.mine"}) {
		t.Errorf("extras = %v, want only the key a person set", e.Config)
	}
	if got := undeclaredConfig(map[string]string{ownership.AppliedKey: "{}"}, nil, nil); len(got) != 0 {
		t.Errorf("the record on a profile, volume, network or project is an extra: %v", got)
	}
	// export drops it
	inst := kumaInstance()
	inst.Config[ownership.AppliedKey] = `{"proto":1,"config":{"user.x":"y"}}`
	s := &stampedExportServer{exportServer: exportServer{inst: inst, volCfg: map[string]string{ownership.AppliedKey: `{"proto":1}`, "size": "1GiB"}}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.YAML, "applied") || !res.Verified {
		t.Errorf("the export carries the record or does not plan clean (%v):\n%s", res.Verified, res.YAML)
	}
}
