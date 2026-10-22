package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/backupmeta"
)

func TestAStackNamesItselfInYAMLAndIsNotAResource(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	// the name may sit in any file of the stack, and may be the name of something in it
	os.WriteFile(a, []byte("kind: stack\nname: immich\n---\nkind: storage-volume\nname: immich-library\nbackup:\n  none: test\n"), 0o644)
	os.WriteFile(b, []byte("kind: instance\nname: immich\nimage: docker-oci:library/alpine:3\n"), 0o644)
	rs, err := LoadFiles([]string{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if name, err := StackName(rs); name != "immich" || err != nil {
		t.Errorf("StackName = %q, %v", name, err)
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatalf("a stack named after an instance in it must not collide with it: %v", err)
	}
	n := 0
	for _, l := range levels {
		for _, r := range l {
			n++
			if r.Kind == KindStack {
				t.Error("the stack's name is metadata, it must not be planned")
			}
		}
	}
	if n != 2 {
		t.Errorf("the volume and the instance are still planned, got %d", n)
	}
}

func TestAStackIsNamedOnceAndWithAUsableName(t *testing.T) {
	two := []Resource{{Kind: KindStack, Name: "a"}, {Kind: KindStack, Name: "b"}}
	if _, err := StackName(two); err == nil || !strings.Contains(err.Error(), "named once") {
		t.Errorf("two names: %v", err)
	}
	if _, err := Levels(two); err == nil {
		t.Error("plan and apply must refuse a stack that names itself twice")
	}
	if name, err := StackName([]Resource{{Kind: KindInstance, Name: "x"}}); name != "" || err != nil {
		t.Errorf("an unnamed stack is fine: %q %v", name, err)
	}
	for _, bad := range []string{"Immich", "has space", "-x", "a/b", strings.Repeat("a", 64)} {
		if err := Validate(Resource{Kind: KindStack, Name: bad}); err == nil {
			t.Errorf("%q must not be a stack name", bad)
		}
	}
	for _, ok := range []string{"immich", "home.lab_1", "a"} {
		if err := Validate(Resource{Kind: KindStack, Name: ok}); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if err := Validate(Resource{Kind: KindStack, Name: "x", Pool: "p"}); err == nil {
		t.Error("a stack declaration is only a name")
	}
}

// The stamp of a volume is the ownership stamp (ownership.go): written when the volume is created, outside the diff, so a volume made before stamps is
// not a "would update" and an unnamed stack never touches the pointer. Creating one with the stamp is tested in ownership_test.go.
func TestAVolumeStampIsOutsideTheDiff(t *testing.T) {
	env := volumeEnv{targets: policyTargets(), stack: "immich"}
	optOut := Resource{Kind: KindStorageVolume, Name: "v", Backup: &backupmeta.VolumeBackup{None: "x"}}

	if p := decideVolume(optOut, nil, env); p.Action != ActionCreate || strings.Contains(strings.Join(p.Changes, "|"), backupmeta.StackKey) {
		t.Errorf("a create is planned without listing the stamp: %v %v", p.Action, p.Changes)
	}
	if p := decideVolume(optOut, liveVolume(map[string]string{}), env); p.Action != ActionNone {
		t.Errorf("a volume applied before the stack was named is not an update: %v %v", p.Action, p.Changes)
	}
	if p := decideVolume(optOut, liveVolume(map[string]string{backupmeta.StackKey: "immich"}), env); p.Action != ActionNone {
		t.Errorf("already stamped is converged: %v %v", p.Action, p.Changes)
	}
	// an unnamed stack never writes the pointer, and never removes one
	if p := decideVolume(optOut, liveVolume(map[string]string{backupmeta.StackKey: "immich"}), volumeEnv{}); p.Action != ActionNone {
		t.Errorf("an unnamed stack leaves another's pointer alone: %v %v", p.Action, p.Changes)
	}
	if set, _, _ := volumeBackupConfig(optOut, env); set[backupmeta.StackKey] != "" {
		t.Errorf("the stamp is not part of what the declaration converges to: %v", set)
	}
}

func TestAVolumeAnotherStackOwnsWarnsAndSaysWhoseItWas(t *testing.T) {
	r := Resource{Kind: KindStorageVolume, Name: "v", Backup: &backupmeta.VolumeBackup{None: "x"}}
	p := decideVolume(r, liveVolume(map[string]string{backupmeta.StackKey: "nextcloud"}), volumeEnv{stack: "immich"})
	if p.Action != ActionNone {
		t.Errorf("applying does not take it over (the stamp is not part of what apply converges): %v %v", p.Action, p.Changes)
	}
	if w := strings.Join(p.Warnings, "|"); !strings.Contains(w, `"nextcloud"`) || !strings.Contains(w, "does not change the stamp") {
		t.Errorf("the warning names the owner and says the stamp stays: %q", w)
	}
	if p := decideVolume(r, liveVolume(map[string]string{backupmeta.StackKey: "immich"}), volumeEnv{stack: "immich"}); len(p.Warnings) != 0 {
		t.Errorf("no warning for its own volume: %v", p.Warnings)
	}
}

func TestPlanOptionsCarryTheStackNameFromTheFullStack(t *testing.T) {
	rs := []Resource{{Kind: KindStack, Name: "immich"}, {Kind: KindBackupTarget, Name: "nas", Location: LocationSameHost, Engine: EngineIncus, Pool: "p"}}
	if o := (PlanOptions{}).ForResources(rs); o.stack != "immich" || o.targets["nas"].Name != "nas" {
		t.Errorf("%+v", o)
	}
	// a subset (one level, which no longer holds the declaration) must not replace what the full stack gave
	full := (PlanOptions{}).ForResources(rs)
	if o := full.withTargets([]Resource{{Kind: KindInstance, Name: "x"}}); o.stack != "immich" {
		t.Errorf("a level planned on its own keeps the stack's name: %+v", o)
	}
}

func TestLoadFile_StackNetwork(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, "kind: stack\nname: blog\nnetwork: own\nnetwork_pool: 203.0.113.0/24\nnetwork_prefix: 27\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Network != StackNetworkOwn || rs[0].NetworkPool != "203.0.113.0/24" || rs[0].NetworkPrefix != 27 {
		t.Errorf("got %+v", rs)
	}
	for name, c := range map[string]struct{ yaml, want string }{
		"a value that is not own":        {"kind: stack\nname: a\nnetwork: lan0\n", `only value is "own"`},
		"a pool with no network: own":    {"kind: stack\nname: a\nnetwork_pool: 10.0.0.0/8\n", "need network: own"},
		"a prefix with no network: own":  {"kind: stack\nname: a\nnetwork_prefix: 24\n", "need network: own"},
		"a pool that is not a range":     {"kind: stack\nname: a\nnetwork: own\nnetwork_pool: nowhere\n", "not an IPv4 range"},
		"a pool with host bits":          {"kind: stack\nname: a\nnetwork: own\nnetwork_pool: 10.1.2.3/8\n", "host bits"},
		"a prefix longer than a /29":     {"kind: stack\nname: a\nnetwork: own\nnetwork_prefix: 30\n", "between"},
		"a prefix shorter than the pool": {"kind: stack\nname: a\nnetwork: own\nnetwork_pool: 10.0.0.0/16\nnetwork_prefix: 8\n", "between"},
		"network_pool on an instance":    {"kind: instance\nname: a\nimage: x\nnetwork_pool: 10.0.0.0/8\n", "NetworkPool"},
	} {
		if _, err := LoadFile(writeTemp(t, c.yaml)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}
