package resolve

import (
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCopyWarnings(t *testing.T) {
	now := at("2026-10-10 12:00")
	vol := func(schedule string) Resource {
		return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &backupmeta.VolumeBackup{
			Snapshots: &backupmeta.SnapshotPolicy{Schedule: "@daily", Retain: "7d"},
			Copies:    []backupmeta.BackupCopy{{Target: "nas", Schedule: schedule, Retain: "30d"}},
		}}
	}
	stamp := func(s string) map[string]string { return map[string]string{backupmeta.CopyStampAt("nas"): s} }
	tests := []struct {
		name    string
		r       Resource
		current map[string]string
		want    string // substring; "" means no warning
	}{
		{"never ran", vol("0 4 * * *"), nil, "the copy to nas has never run -- `tink backup run lib`"},
		{"ran on schedule", vol("0 4 * * *"), stamp(at("2026-10-10 04:05").Format(time.RFC3339)), ""},
		{"missed today's slot, and the 6h grace cap has passed", vol("0 4 * * *"), stamp(at("2026-10-09 04:05").Format(time.RFC3339)), "is overdue"}, // due 04:00; now 12:00
		{"overdue by days", vol("0 4 * * *"), stamp(at("2026-10-05 04:05").Format(time.RFC3339)), "is overdue: last ran 5d ago"},
		{"a stamp tink did not write", vol("0 4 * * *"), stamp("whenever"), "not a timestamp tink wrote"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(copyWarnings(tc.r, tc.current, now), "\n")
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("warnings = %q, want it to contain %q", got, tc.want)
			}
		})
	}
	if w := copyWarnings(Resource{Kind: KindStorageVolume, Name: "x", Backup: &backupmeta.VolumeBackup{None: "n"}}, nil, now); w != nil {
		t.Errorf("an opt-out has no copies to warn about, got %v", w)
	}
	// a weekly copy that ran 3 days ago is fine; 10 days ago is not
	weekly := vol("@weekly")
	if w := copyWarnings(weekly, stamp(at("2026-10-07 00:30").Format(time.RFC3339)), now); len(w) != 0 {
		t.Errorf("a weekly copy 3 days old must not be overdue: %v", w)
	}
}

func TestDecideVolumeWarnsAboutCopiesOnlyForExistingVolumes(t *testing.T) {
	nasTarget := map[string]Resource{"nas": {Kind: KindBackupTarget, Name: "nas", Location: LocationSameHost, Engine: EngineIncus, Pool: "nas"}}
	r := Resource{Kind: KindStorageVolume, Name: "lib", Backup: &backupmeta.VolumeBackup{
		Snapshots: &backupmeta.SnapshotPolicy{Schedule: "@daily", Retain: "7d"},
		Copies:    []backupmeta.BackupCopy{{Target: "nas", Schedule: "@daily", Retain: "30d"}}}}
	if w := strings.Join(decideVolume(r, nil, volumeEnv{targets: nasTarget}).Warnings, "|"); strings.Contains(w, "has never run") {
		t.Errorf("a volume that does not exist yet cannot have missed a copy, got %q", w)
	}
	existing := &api.StorageVolume{StorageVolumePut: api.StorageVolumePut{Config: map[string]string{}}}
	if w := strings.Join(decideVolume(r, existing, volumeEnv{targets: nasTarget}).Warnings, "|"); !strings.Contains(w, "the copy to nas has never run") {
		t.Errorf("an existing volume whose copy never ran must say so, got %q", w)
	}
}
