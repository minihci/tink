package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestVolumesAreStampedWithTheStackThatAppliedThem(t *testing.T) {
	env := volumeEnv{targets: policyTargets(), stack: "immich"}
	optOut := Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "x"}}

	if p := decideVolume(optOut, nil, env); p.Action != ActionCreate || !strings.Contains(strings.Join(p.Changes, "|"), StackKey) {
		t.Errorf("every volume is stamped, even one that opts out of backup: %v %v", p.Action, p.Changes)
	}
	if p := decideVolume(optOut, liveVolume(map[string]string{}), env); p.Action != ActionUpdate {
		t.Errorf("a volume applied before the stack was named is updated with the pointer: %v %v", p.Action, p.Changes)
	}
	if p := decideVolume(optOut, liveVolume(map[string]string{StackKey: "immich"}), env); p.Action != ActionNone {
		t.Errorf("already stamped is converged: %v %v", p.Action, p.Changes)
	}
	// an unnamed stack never writes the pointer, and never removes one
	if p := decideVolume(optOut, liveVolume(map[string]string{StackKey: "immich"}), volumeEnv{}); p.Action != ActionNone {
		t.Errorf("an unnamed stack leaves another's pointer alone: %v %v", p.Action, p.Changes)
	}
	if set, _, _ := volumeBackupConfig(optOut, volumeEnv{}); set[StackKey] != "" {
		t.Errorf("an unnamed stack writes no pointer: %v", set)
	}
}

func TestApplyingAVolumeAnotherStackOwnsWarnsAndSaysWhoseItWas(t *testing.T) {
	r := Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "x"}}
	p := decideVolume(r, liveVolume(map[string]string{StackKey: "nextcloud"}), volumeEnv{stack: "immich"})
	if p.Action != ActionUpdate {
		t.Errorf("it is taken over: %v", p.Action)
	}
	if w := strings.Join(p.Warnings, "|"); !strings.Contains(w, `"nextcloud"`) || !strings.Contains(w, `"immich"`) {
		t.Errorf("the warning names both stacks: %q", w)
	}
	if p := decideVolume(r, liveVolume(map[string]string{StackKey: "immich"}), volumeEnv{stack: "immich"}); len(p.Warnings) != 0 {
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
