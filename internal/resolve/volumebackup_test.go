package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

func snapVol(name, schedule, retain string) Resource {
	return Resource{Kind: KindStorageVolume, Name: name, Backup: &VolumeBackup{
		Snapshots: &SnapshotPolicy{Schedule: schedule, Retain: retain},
	}}
}

func TestValidateBackup(t *testing.T) {
	ok := func(r Resource) {
		t.Helper()
		if err := Validate(r); err != nil {
			t.Errorf("%+v: unexpected error %v", r.Backup, err)
		}
	}
	bad := func(r Resource, want string) {
		t.Helper()
		if err := Validate(r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: error = %v, want it to contain %q", r.Backup, err, want)
		}
	}

	// A missing block is not a load error: it is a plan-time warning.
	ok(Resource{Kind: KindStorageVolume, Name: "v"})
	ok(snapVol("v", "0 3 * * *", "14d"))
	ok(snapVol("v", "@daily", "1w 3d"))
	ok(snapVol("v", "@daily,@weekly", "6m"))
	ok(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "regenerable cache"}})

	bad(snapVol("v", "", "14d"), "schedule: required")
	bad(snapVol("v", "daily", "14d"), "5-field cron")
	bad(snapVol("v", "0 3 * *", "14d"), "5-field cron")
	bad(snapVol("v", "@sometimes", "14d"), "@sometimes")
	bad(snapVol("v", "@daily", ""), "retain: required")
	bad(snapVol("v", "@daily", "forever"), "expiry syntax")
	bad(snapVol("v", "@daily", "14 d"), "expiry syntax")
	bad(snapVol("v", "@daily", "0d"), "expiry syntax") // zero means "never expires" to Incus
	bad(snapVol("v", "@daily", "1d 2d"), "repeats the unit")
	bad(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{}}, "snapshots (schedule + retain) and/or copies, or none")
	bad(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "  "}}, "needs a reason")
	bad(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{
		None: "x", Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "1d"},
	}}, "mutually exclusive")
	bad(Resource{Kind: KindInstance, Name: "i", Backup: &VolumeBackup{None: "x"}}, "does not use field")
}

func TestDecideVolume(t *testing.T) {
	policy := snapVol("v", "0 3 * * *", "14d")
	bare := Resource{Kind: KindStorageVolume, Name: "v"}
	live := func(cfg map[string]string) *api.StorageVolume {
		return &api.StorageVolume{StorageVolumePut: api.StorageVolumePut{Config: cfg}}
	}
	synced := live(map[string]string{"snapshots.schedule": "0 3 * * *", "snapshots.expiry": "14d", "size": "10GiB"})
	optOut := Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "regenerable"}}

	tests := []struct {
		name       string
		r          Resource
		current    *api.StorageVolume
		want       Action
		wantChange string
		wantWarn   string
	}{
		{"no block, absent: still created, with a warning", bare, nil, ActionCreate, "", "no backup declared"},
		{"no block, exists: unchanged, with a warning", bare, live(nil), ActionNone, "", "will become an error"},
		{"no block never touches live config", bare, synced, ActionNone, "", "no backup declared"},
		{"policy, absent: create carries the config", policy, nil, ActionCreate, "snapshots.schedule", ""},
		{"policy, exists without it: update", policy, live(map[string]string{"size": "10GiB"}), ActionUpdate, "snapshots.expiry", ""},
		{"policy, expiry drifted: update", policy, live(map[string]string{"snapshots.schedule": "0 3 * * *", "snapshots.expiry": "30d"}), ActionUpdate, `"30d" -> "14d"`, ""},
		{"policy, already converged (foreign keys ignored)", policy, synced, ActionNone, "", ""},
		{"opt-out, absent: plain create", optOut, nil, ActionCreate, "", ""},
		{"opt-out, exists: nothing to do", optOut, live(nil), ActionNone, "", ""},
		{"opt-out but live schedule still set: warns, does not remove", optOut, synced, ActionNone, "", "does not remove it"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := decideVolume(tc.r, tc.current)
			if p.Action != tc.want {
				t.Fatalf("action = %d, want %d (changes=%v)", p.Action, tc.want, p.Changes)
			}
			// Missing backup is a warning for now, never a block; flip this
			// assertion together with decideVolume when it becomes an error.
			if len(p.Blocked) != 0 {
				t.Errorf("a volume must not be blocked yet, got %v", p.Blocked)
			}
			if got := strings.Join(p.Changes, " | "); !strings.Contains(got, tc.wantChange) || (tc.wantChange == "" && got != "") {
				t.Errorf("changes = %q, want to contain %q", got, tc.wantChange)
			}
			if got := strings.Join(p.Warnings, " | "); !strings.Contains(got, tc.wantWarn) || (tc.wantWarn == "" && got != "") {
				t.Errorf("warnings = %q, want to contain %q", got, tc.wantWarn)
			}
		})
	}
}

func TestBackupVolumeConfig(t *testing.T) {
	got := backupVolumeConfig(&VolumeBackup{Snapshots: &SnapshotPolicy{Schedule: " @daily ", Retain: "14d "}})
	if len(got) != 2 || got["snapshots.schedule"] != "@daily" || got["snapshots.expiry"] != "14d" {
		t.Errorf("config = %v", got)
	}
	if backupVolumeConfig(nil) != nil || backupVolumeConfig(&VolumeBackup{None: "x"}) != nil {
		t.Error("no block or an opt-out must not set any Incus keys")
	}
}

func TestLoadFileParsesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tink.yaml")
	doc := `kind: storage-volume
name: lib
backup:
  snapshots:
    schedule: "0 3 * * *"
    retain: 14d
---
kind: storage-volume
name: cache
backup:
  none: regenerable
---
kind: storage-volume
name: bare
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 {
		t.Fatalf("parsed %d resources", len(rs))
	}
	if s := rs[0].Backup.Snapshots; s == nil || s.Schedule != "0 3 * * *" || s.Retain != "14d" {
		t.Errorf("lib backup = %+v", rs[0].Backup)
	}
	if rs[1].Backup == nil || rs[1].Backup.None != "regenerable" || rs[1].Backup.Snapshots != nil {
		t.Errorf("cache backup = %+v", rs[1].Backup)
	}
	if rs[2].Backup != nil {
		t.Errorf("bare volume must have nil Backup, got %+v", rs[2].Backup)
	}
}

func TestLoadFileRejectsBadBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tink.yaml")
	doc := "kind: storage-volume\nname: v\nbackup:\n  snapshots:\n    schedule: \"@daily\"\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "retain: required") {
		t.Errorf("err = %v, want a retain-required error", err)
	}
}
