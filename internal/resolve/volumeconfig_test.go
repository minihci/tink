package resolve

import (
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

func volumeWithConfig(config map[string]string) Resource {
	return Resource{Kind: KindStorageVolume, Name: "data", Config: config, Backup: &backupmeta.VolumeBackup{None: "test"}}
}

func TestAVolumesDeclaredConfigIsWhatItIsCreatedWith(t *testing.T) {
	r := volumeWithConfig(map[string]string{"initial.uid": "1000", "size": "1GiB"})
	set, _, err := volumeBackupConfig(r, volumeEnv{})
	if err != nil || set["initial.uid"] != "1000" || set["size"] != "1GiB" {
		t.Fatalf("%v %v", set, err)
	}
	p := decideVolume(r, nil, volumeEnv{})
	if p.Action != ActionCreate || !strings.Contains(strings.Join(p.Changes, "\n"), "config.initial.uid") {
		t.Errorf("a new volume is created with it: %+v", p)
	}
}

func TestCreationOnlyKeysAreAWarningOnAnExistingVolumeNotAChange(t *testing.T) {
	r := volumeWithConfig(map[string]string{"initial.uid": "1000", "size": "1GiB"})
	cur := &api.StorageVolume{Name: "data", StorageVolumePut: api.StorageVolumePut{Config: map[string]string{"size": "1GiB"}}} // made without the owner
	p := decideVolume(r, cur, volumeEnv{})
	if p.Action != ActionNone {
		t.Errorf("initial.uid cannot be converged on a volume that exists, so it is no change: %+v", p)
	}
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "initial.uid") || !strings.Contains(w, "only applies when a volume is created") {
		t.Errorf("but the difference is said: %q", w)
	}

	// the ordinary key still converges
	cur.Config["size"] = "2GiB"
	p = decideVolume(r, cur, volumeEnv{})
	if p.Action != ActionUpdate || !strings.Contains(strings.Join(p.Changes, "\n"), `config.size: "2GiB" -> "1GiB"`) {
		t.Errorf("size should be an update: %+v", p)
	}
	if strings.Contains(strings.Join(p.Changes, "\n"), "initial") {
		t.Errorf("a creation-only key is never listed as a change: %v", p.Changes)
	}

	// when they agree, nothing at all
	cur = &api.StorageVolume{Name: "data", StorageVolumePut: api.StorageVolumePut{Config: map[string]string{"size": "1GiB", "initial.uid": "1000"}}}
	if p := decideVolume(r, cur, volumeEnv{}); p.Action != ActionNone || len(p.Warnings) != 0 {
		t.Errorf("%+v", p)
	}
}

func TestSplitCreationOnlyTakesThemOutOfTheMap(t *testing.T) {
	m := map[string]string{"initial.uid": "1", "initial.mode": "0750", "size": "1GiB"}
	got := splitCreationOnly(m)
	if len(got) != 2 || len(m) != 1 || m["size"] != "1GiB" {
		t.Errorf("got %v left %v", got, m)
	}
}

func TestVolumeConfigThatTinkOwnsOrIncusOwnsIsRefused(t *testing.T) {
	snap := &backupmeta.VolumeBackup{Snapshots: &backupmeta.SnapshotPolicy{Schedule: "0 3 * * *", Retain: "7d"}}
	for name, c := range map[string]struct {
		r    Resource
		want string
	}{
		"volatile":        {volumeWithConfig(map[string]string{"volatile.idmap.last": "[]"}), "belongs to Incus"},
		"copy policy":     {volumeWithConfig(map[string]string{backupmeta.PolicyKey: "{}"}), "written by tink itself"},
		"copy stamp":      {volumeWithConfig(map[string]string{"user.tink.backup.copy.nas.at": "x"}), "written by tink itself"},
		"stack pointer":   {volumeWithConfig(map[string]string{backupmeta.StackKey: "s"}), "written by tink itself"},
		"snapshots twice": {Resource{Kind: KindStorageVolume, Name: "data", Backup: snap, Config: map[string]string{"snapshots.schedule": "@daily"}}, "use one or the other"},
	} {
		err := Validate(c.r)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, c.want)
		}
	}
	// snapshots.* alone, with no backup: block saying the same, is the person's own and allowed
	if err := Validate(volumeWithConfig(map[string]string{"snapshots.schedule": "@daily", "initial.uid": "1000", "user.note": "x"})); err != nil {
		t.Errorf("ordinary volume config is allowed: %v", err)
	}
}
