package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// loadStackText loads one stack from text.
func loadStackText(t *testing.T, text string) []Resource {
	t.Helper()
	rs, err := LoadFile(writeTemp(t, text))
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func applied(t *testing.T, text string) []Resource {
	t.Helper()
	out, err := ApplyDefaults(loadStackText(t, text))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func find(t *testing.T, rs []Resource, name string) Resource {
	t.Helper()
	for _, r := range rs {
		if r.Name == name && r.Kind != KindStack {
			return r
		}
	}
	t.Fatalf("no resource %q in %d resources", name, len(rs))
	return Resource{}
}

const defaultsHeader = `kind: stack
name: site
defaults:
  instance:
    config: {boot.autostart: "true", limits.memory: 128MiB}
    devices: {nic: {name: eth0}}
  storage-volume:
    backup:
      snapshots: {schedule: "0 3 * * *", retain: 14d}
---
`

// Each kind that has a section receives it: an instance its config keys and its NIC name, a volume its backup block. A device of another type, a
// resource of another kind and a document that is not in the graph are left as they were.
func TestDefaultsAreFilledIntoEachKind(t *testing.T) {
	rs := applied(t, defaultsHeader+`kind: instance
name: app
image: docker-oci:library/alpine:3
devices:
  eth0: {type: nic, network: tink0}
  data: {type: disk, pool: default, source: vol, path: /data}
---
kind: storage-volume
name: vol
pool: default
---
kind: profile
name: base
devices:
  eth0: {type: nic, network: tink0}
---
kind: file
name: f
instance: app
path: /x
content: hi
`)
	app := find(t, rs, "app")
	if want := map[string]string{"boot.autostart": "true", "limits.memory": "128MiB"}; !reflect.DeepEqual(app.Config, want) {
		t.Errorf("instance config = %v, want %v", app.Config, want)
	}
	if got := app.Devices["eth0"]["name"]; got != "eth0" {
		t.Errorf("the NIC's name = %q, want eth0", got)
	}
	if _, ok := app.Devices["data"]["name"]; ok {
		t.Errorf("a disk is not a NIC: %v", app.Devices["data"])
	}
	vol := find(t, rs, "vol")
	if vol.Backup == nil || vol.Backup.Snapshots == nil || vol.Backup.Snapshots.Retain != "14d" {
		t.Errorf("volume backup = %+v, want the default snapshots", vol.Backup)
	}
	if p := find(t, rs, "base"); p.Devices["eth0"]["name"] != "" {
		t.Errorf("a profile takes no defaults: %v", p.Devices)
	}
	if f := find(t, rs, "f"); f.Config != nil || f.Backup != nil {
		t.Errorf("a file takes no defaults: %+v", f)
	}
	if len(rs) != 5 {
		t.Errorf("defaults add no resource: %d resources, want the 5 written", len(rs))
	}
}

// A value a resource sets always wins, one key at a time for config and devices, and as a whole for a backup block.
func TestAValueTheResourceSetsWins(t *testing.T) {
	rs := applied(t, defaultsHeader+`kind: instance
name: app
image: docker-oci:library/alpine:3
config:
  limits.memory: 1GiB
  boot.autostart: "false"
devices:
  eth0: {type: nic, name: wan0, network: tink0}
---
kind: storage-volume
name: opted-out
backup:
  none: regenerable
---
kind: storage-volume
name: own-policy
backup:
  snapshots: {schedule: "0 5 * * *", retain: 3d}
`)
	app := find(t, rs, "app")
	if want := map[string]string{"boot.autostart": "false", "limits.memory": "1GiB"}; !reflect.DeepEqual(app.Config, want) {
		t.Errorf("config = %v, want what the instance wrote (%v)", app.Config, want)
	}
	if app.Devices["eth0"]["name"] != "wan0" {
		t.Errorf("NIC name = %q, want wan0", app.Devices["eth0"]["name"])
	}
	if b := find(t, rs, "opted-out").Backup; b == nil || b.None != "regenerable" || b.Snapshots != nil {
		t.Errorf("a volume that says none keeps it, and gets nothing merged in: %+v", b)
	}
	if b := find(t, rs, "own-policy").Backup; b == nil || b.Snapshots.Schedule != "0 5 * * *" || b.Snapshots.Retain != "3d" {
		t.Errorf("a volume's own block is not merged with the default: %+v", b)
	}
}

// A key the resource sets to the empty string is set: the default does not fill it.
func TestAnEmptyValueIsStillAValue(t *testing.T) {
	rs := applied(t, defaultsHeader+"kind: instance\nname: app\nimage: docker-oci:library/alpine:3\nconfig:\n  limits.memory: \"\"\n")
	if got := find(t, rs, "app").Config["limits.memory"]; got != "" {
		t.Errorf("limits.memory = %q, want the empty string the instance wrote", got)
	}
}

// A stack with no defaults comes back as it was: the same slice, so every existing stack plans exactly as before.
func TestNoDefaultsLeavesTheStackUntouched(t *testing.T) {
	in := loadStackText(t, "kind: stack\nname: site\n---\nkind: instance\nname: app\nimage: docker-oci:library/alpine:3\n---\nkind: storage-volume\nname: v\nbackup:\n  none: x\n")
	before := make([]Resource, len(in))
	copy(before, in)
	out, err := ApplyDefaults(in)
	if err != nil {
		t.Fatal(err)
	}
	if &out[0] != &in[0] || !reflect.DeepEqual(out, before) {
		t.Error("a stack without defaults must come back untouched")
	}
	// the stack files already in the repository are such stacks
	for _, f := range []string{"testdata/spike.yaml", "../join/testdata/platform.yaml", "../join/testdata/canary.yaml"} {
		rs, err := LoadFiles([]string{f})
		if err != nil {
			t.Fatal(err)
		}
		got, err := ApplyDefaults(rs)
		if err != nil || &got[0] != &rs[0] {
			t.Errorf("%s: must come back untouched (%v)", f, err)
		}
	}
	// and so does a stack that declares an empty section, or one for a kind it does not have
	out = applied(t, "kind: stack\nname: site\ndefaults:\n  storage-volume: {}\n---\nkind: instance\nname: app\nimage: docker-oci:library/alpine:3\n")
	if app := find(t, out, "app"); app.Config != nil || app.Devices != nil {
		t.Errorf("an empty section fills nothing: %+v", app)
	}
}

// The input is never written to, and two resources never share a map or a backup block that came from the defaults.
func TestDefaultsDoNotShareOrMutate(t *testing.T) {
	in := loadStackText(t, defaultsHeader+`kind: instance
name: a
image: docker-oci:library/alpine:3
devices:
  eth0: {type: nic, network: tink0}
---
kind: instance
name: b
image: docker-oci:library/alpine:3
devices:
  eth0: {type: nic, network: tink0}
---
kind: storage-volume
name: v1
---
kind: storage-volume
name: v2
`)
	before := make([]Resource, len(in))
	copy(before, in)
	out, err := ApplyDefaults(in)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, before) {
		t.Error("ApplyDefaults changed its input")
	}
	find(t, out, "a").Config["boot.autostart"] = "changed"
	if find(t, out, "b").Config["boot.autostart"] != "true" {
		t.Error("two instances share a config map")
	}
	find(t, out, "v1").Backup.Snapshots.Retain = "1d"
	if find(t, out, "v2").Backup.Snapshots.Retain != "14d" {
		t.Error("two volumes share a backup block")
	}
	// running it again finds nothing left to fill
	again, err := ApplyDefaults(out)
	if err != nil || !reflect.DeepEqual(again, out) {
		t.Errorf("a second run changed something: %v", err)
	}
}

// Defaults never change the shape of the graph: the levels of a stack are what they are with every default written out on the resources.
func TestDefaultsDoNotChangeTheGraph(t *testing.T) {
	rs := applied(t, defaultsHeader+`kind: network
name: tink0
---
kind: instance
name: app
image: docker-oci:library/alpine:3
devices:
  eth0: {type: nic, network: tink0}
  data: {type: disk, pool: default, source: vol, path: /data}
---
kind: storage-volume
name: vol
`)
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	var shape []string
	for i, l := range levels {
		for _, r := range l {
			shape = append(shape, string(rune('0'+i))+" "+string(r.Kind)+" "+r.Name)
		}
	}
	if want := []string{"0 network tink0", "0 storage-volume vol", "1 instance app"}; !reflect.DeepEqual(shape, want) {
		t.Errorf("levels = %v, want %v", shape, want)
	}
}

// The helper stack, written long and written with defaults, resolves to exactly the same resources and the same levels. This is the proof that the
// shorter file says the same thing.
func TestTheShortHelperStackResolvesToTheLongOne(t *testing.T) {
	load := func(name string) ([]Resource, [][]Resource) {
		t.Helper()
		rs, err := LoadFiles([]string{filepath.Join("testdata", "defaults", name)})
		if err != nil {
			t.Fatal(err)
		}
		rs, err = ApplyDefaults(rs)
		if err != nil {
			t.Fatal(err)
		}
		levels, err := Levels(rs)
		if err != nil {
			t.Fatal(err)
		}
		return rs, levels
	}
	longRs, longLevels := load("helpers-long.yaml")
	shortRs, shortLevels := load("helpers-short.yaml")
	if !reflect.DeepEqual(longLevels, shortLevels) {
		t.Errorf("the levels differ:\nlong:  %+v\nshort: %+v", longLevels, shortLevels)
	}
	// the stack declaration is the one document allowed to differ: it carries the defaults
	strip := func(rs []Resource) []Resource {
		var out []Resource
		for _, r := range rs {
			if r.Kind == KindStack {
				r.Defaults = nil
			}
			out = append(out, r)
		}
		return out
	}
	if !reflect.DeepEqual(strip(longRs), strip(shortRs)) {
		t.Errorf("the resources differ:\nlong:  %+v\nshort: %+v", strip(longRs), strip(shortRs))
	}
	count := func(name string) int {
		b, err := os.ReadFile(filepath.Join("testdata", "defaults", name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(b), "\n")
	}
	if long, short := count("helpers-long.yaml"), count("helpers-short.yaml"); short >= long {
		t.Errorf("the file with defaults (%d lines) is not shorter than the one without (%d)", short, long)
	}
}

func loadErr(t *testing.T, text string) string {
	t.Helper()
	_, err := LoadFile(writeTemp(t, text))
	if err == nil {
		t.Fatalf("expected a load error for:\n%s", text)
	}
	return err.Error()
}

// The loader is as strict about defaults as about the rest: a key it does not know is an error at every level, and so is a default the same value
// could not be given on a resource.
func TestBadDefaultsAreLoadErrors(t *testing.T) {
	cases := []struct{ name, defaults, want string }{
		{"unknown section", "  instances: {}\n", "not found"},
		{"a kind with nothing to default", "  profile: {}\n", "not found"},
		{"unknown instance key", "  instance:\n    restart: true\n", "not found"},
		{"unknown volume key", "  storage-volume:\n    pool: default\n", "not found"},
		{"unknown backup key", "  storage-volume:\n    backup:\n      snapshot: {schedule: \"0 3 * * *\", retain: 7d}\n", "not found"},
		{"a device type that is not one", "  instance:\n    devices: {nik: {name: eth0}}\n", `"nik" is not a device type`},
		{"a device type with no key", "  instance:\n    devices: {nic: {}}\n", "names no key"},
		{"a device default that names a network", "  instance:\n    devices: {nic: {network: tink0}}\n", "another resource"},
		{"a device default that names a source", "  instance:\n    devices: {disk: {source: vol}}\n", "another resource"},
		{"a device default that names an ACL", "  instance:\n    devices: {nic: {security.acls: web-in}}\n", "another resource"},
		{"a device default that changes the type", "  instance:\n    devices: {nic: {type: disk}}\n", "another resource or change what a device is"},
		{"a secret outside environment", "  instance:\n    config: {limits.memory: \"${secret:x}\"}\n", "secret reference is not allowed"},
		{"a secret in a device", "  instance:\n    devices: {nic: {ipv4.address: \"${secret:x}\"}}\n", "secret reference is not allowed"},
		{"a secret in a backup block", "  storage-volume:\n    backup:\n      none: \"${secret:x}\"\n", "secret reference is not allowed"},
		{"a secret in a backup check", "  storage-volume:\n    backup:\n      snapshots: {schedule: \"0 3 * * *\", retain: 7d}\n      verify: {every: weekly, check: {image: x, command: [\"${secret:x}\"]}}\n", "secret reference is not allowed"},
		{"a malformed reference in environment", "  instance:\n    config: {environment.A: \"${secret:}\"}\n", "environment.A"},
		{"the tink-owned prefix", "  instance:\n    config: {user.tink.publish.web: \"{}\"}\n", "tink owns"},
		{"a backup block that is not one", "  storage-volume:\n    backup:\n      verify: weekly\n", "give snapshots"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := loadErr(t, "kind: stack\nname: site\ndefaults:\n"+c.defaults)
			if !strings.Contains(got, c.want) {
				t.Errorf("error = %q, want it to contain %q", got, c.want)
			}
		})
	}
	t.Run("defaults on a resource that is not a stack", func(t *testing.T) {
		got := loadErr(t, "kind: instance\nname: app\nimage: x\ndefaults:\n  instance: {}\n")
		if !strings.Contains(got, `does not use field "Defaults"`) {
			t.Errorf("error = %q", got)
		}
	})
}

// A secret reference is accepted in an instance's environment.* default, the one place it is accepted anywhere, and the stack then needs the store
// exactly as if every instance had written it.
func TestASecretReferenceInAnEnvironmentDefaultIsExpanded(t *testing.T) {
	text := "kind: stack\nname: site\ndefaults:\n  instance:\n    config: {environment.TOKEN: \"${secret:token}\"}\n---\nkind: instance\nname: app\nimage: docker-oci:library/alpine:3\n"
	in := loadStackText(t, text)
	if NeedsSecrets(in) {
		t.Fatal("nothing in the loaded documents is an instance's reference yet")
	}
	out, err := ApplyDefaults(in)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsSecrets(out) {
		t.Fatal("the filled instance holds the reference, so the store is needed")
	}
	if got := find(t, out, "app").Config["environment.TOKEN"]; got != "${secret:token}" {
		t.Errorf("the reference is carried as written, for the next pass to expand: %q", got)
	}
}

// A volume's secret rule is not bypassed by the filled result either: the resource is re-validated after it receives a default.
func TestAFilledResourceIsValidatedLikeAWrittenOne(t *testing.T) {
	d := &StackDefaults{Instance: &InstanceDefaults{Config: map[string]string{"user.tink.publish.web": "{}"}}}
	_, err := ApplyDefaults([]Resource{{Kind: KindStack, Name: "s", Defaults: d}, {Kind: KindInstance, Name: "app"}})
	if err == nil || !strings.Contains(err.Error(), "after the stack's defaults") {
		t.Errorf("error = %v", err)
	}
}

// Defaults belong to the one stack: two declarations of it is the same error as everywhere else.
func TestDefaultsOfAStackNamedTwiceAreAnError(t *testing.T) {
	_, err := ApplyDefaults([]Resource{
		{Kind: KindStack, Name: "a", Defaults: &StackDefaults{}},
		{Kind: KindStack, Name: "b"},
	})
	if err == nil || !strings.Contains(err.Error(), "named once") {
		t.Errorf("error = %v", err)
	}
}
