package resolve

import (
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextRun(t *testing.T) {
	tests := []struct {
		schedule, after, want string
	}{
		{"0 3 * * *", "2026-10-06 12:00", "2026-10-07 03:00"},
		{"0 3 * * *", "2026-10-06 02:59", "2026-10-06 03:00"},
		{"0 3 * * *", "2026-10-06 03:00", "2026-10-07 03:00"}, // strictly after
		{"*/15 * * * *", "2026-10-06 12:07", "2026-10-06 12:15"},
		{"@daily", "2026-10-06 12:00", "2026-10-07 00:00"},
		{"@hourly", "2026-10-06 12:30", "2026-10-06 13:00"},
		{"@weekly", "2026-10-06 12:00", "2026-10-11 00:00"},        // the next Sunday
		{"@daily,@hourly", "2026-10-06 12:30", "2026-10-06 13:00"}, // the earliest of a list
		{" 0 3 * * * ", "2026-10-06 12:00", "2026-10-07 03:00"},
	}
	for _, tc := range tests {
		got, err := NextRun(tc.schedule, at(tc.after))
		if err != nil || !got.Equal(at(tc.want)) {
			t.Errorf("NextRun(%q, %s) = %v, %v; want %s", tc.schedule, tc.after, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "daily", "0 3 * *", "@sometimes", "99 99 * * *"} {
		if _, err := NextRun(bad, at("2026-10-06 12:00")); err == nil {
			t.Errorf("NextRun(%q) must reject it", bad)
		}
	}
}

func TestExpiryAfter(t *testing.T) {
	from := at("2026-01-31 10:00")
	tests := []struct{ expr, want string }{
		{"14d", "2026-02-14 10:00"},
		{"1w 3d", "2026-02-10 10:00"},
		{"1y", "2027-01-31 10:00"},
		{"12H", "2026-01-31 22:00"},
		{"90M", "2026-01-31 11:30"},
	}
	for _, tc := range tests {
		got, err := ExpiryAfter(from, tc.expr)
		if err != nil || !got.Equal(at(tc.want)) {
			t.Errorf("ExpiryAfter(%q) = %v, %v; want %s", tc.expr, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "forever", "0d", "1d 2d"} {
		if _, err := ExpiryAfter(from, bad); err == nil {
			t.Errorf("ExpiryAfter(%q) must reject it", bad)
		}
	}
}

func TestCopyIsDue(t *testing.T) {
	cfg := func(last string) map[string]string {
		if last == "" {
			return nil
		}
		return map[string]string{CopyStampAt("nas"): at(last).Format(time.RFC3339)}
	}
	tests := []struct {
		name, last, now string
		want            bool
	}{
		{"never ran", "", "2026-10-06 12:00", true},
		{"ran today after the daily slot", "2026-10-06 04:05", "2026-10-06 23:00", false},
		{"the next slot has passed", "2026-10-06 04:05", "2026-10-07 04:01", true},
		{"exactly the next slot", "2026-10-06 04:05", "2026-10-07 04:00", true},
		{"just before the next slot", "2026-10-06 04:05", "2026-10-07 03:59", false},
	}
	for _, tc := range tests {
		got, err := CopyIsDue("0 4 * * *", cfg(tc.last), "nas", at(tc.now))
		if err != nil || got != tc.want {
			t.Errorf("%s: CopyIsDue = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	// a stamp tink did not write is as good as none
	if due, _ := CopyIsDue("0 4 * * *", map[string]string{CopyStampAt("nas"): "yesterday-ish"}, "nas", at("2026-10-06 12:00")); !due {
		t.Error("an unreadable stamp must not suppress a copy")
	}
}

func TestCopyWarnings(t *testing.T) {
	now := at("2026-10-10 12:00")
	vol := func(schedule string) Resource {
		return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
			Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"},
			Copies:    []BackupCopy{{Target: "nas", Schedule: schedule, Retain: "30d"}},
		}}
	}
	stamp := func(s string) map[string]string { return map[string]string{CopyStampAt("nas"): s} }
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
	if w := copyWarnings(Resource{Kind: KindStorageVolume, Name: "x", Backup: &VolumeBackup{None: "n"}}, nil, now); w != nil {
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
	r := Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
		Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"},
		Copies:    []BackupCopy{{Target: "nas", Schedule: "@daily", Retain: "30d"}}}}
	if w := strings.Join(decideVolume(r, nil, nasTarget).Warnings, "|"); strings.Contains(w, "has never run") {
		t.Errorf("a volume that does not exist yet cannot have missed a copy, got %q", w)
	}
	existing := &api.StorageVolume{StorageVolumePut: api.StorageVolumePut{Config: map[string]string{}}}
	if w := strings.Join(decideVolume(r, existing, nasTarget).Warnings, "|"); !strings.Contains(w, "the copy to nas has never run") {
		t.Errorf("an existing volume whose copy never ran must say so, got %q", w)
	}
}

func TestCopyOf(t *testing.T) {
	if got := CopyOf("", "", "lib"); got != "default/default/lib" {
		t.Errorf("CopyOf = %q", got)
	}
	if got := CopyOf("immich", "fast", "lib"); got != "immich/fast/lib" {
		t.Errorf("CopyOf = %q", got)
	}
}
