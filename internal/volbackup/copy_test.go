package volbackup

import (
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

func tm(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func bkVol(name, copyOf, at string) api.StorageVolume {
	cfg := map[string]string{}
	if copyOf != "" {
		cfg[resolve.MarkerCopyOf] = copyOf
	}
	if at != "" {
		cfg[resolve.MarkerCopyAt] = at
	}
	return api.StorageVolume{Name: name, Type: "custom", StorageVolumePut: api.StorageVolumePut{Config: cfg}}
}

func TestRestorePointsOnlyCountVolumesMarkedForThisVolume(t *testing.T) {
	mine := resolve.CopyOf("", "default", "lib")
	vols := []api.StorageVolume{
		bkVol("lib-bk-1", mine, tm("2026-10-01 04:00").Format(time.RFC3339)),
		bkVol("lib-bk-3", mine, tm("2026-10-03 04:00").Format(time.RFC3339)),
		bkVol("lib-bk-2", mine, tm("2026-10-02 04:00").Format(time.RFC3339)),
		bkVol("other-bk-9", resolve.CopyOf("", "default", "other"), tm("2026-10-09 04:00").Format(time.RFC3339)), // another volume's
		bkVol("lib-bk-wrong-project", resolve.CopyOf("p", "default", "lib"), tm("2026-10-09 04:00").Format(time.RFC3339)),
		bkVol("unrelated", "", ""),                     // not made by tink: must never be listed
		bkVol("lib-bk-badstamp", mine, "last tuesday"), // marker present, age unreadable: leave alone
		{Name: "lib-bk-image", Type: "image", StorageVolumePut: api.StorageVolumePut{Config: map[string]string{resolve.MarkerCopyOf: mine, resolve.MarkerCopyAt: tm("2026-10-09 04:00").Format(time.RFC3339)}}},
	}
	got := restorePointsOf(vols, mine)
	var names []string
	for _, p := range got {
		names = append(names, p.Volume)
	}
	if strings.Join(names, ",") != "lib-bk-3,lib-bk-2,lib-bk-1" {
		t.Errorf("restore points = %v; want exactly this volume's three, newest first (nothing tink did not make for it, no unreadable ages, no non-custom volumes)", names)
	}
}

func TestExpiredNeverTouchesTheNewestOrTheFreshOne(t *testing.T) {
	now := tm("2026-12-01 12:00")
	pts := []RestorePoint{ // newest first
		{Volume: "d", At: tm("2026-11-30 04:00")},
		{Volume: "c", At: tm("2026-11-10 04:00")},
		{Volume: "b", At: tm("2026-10-20 04:00")},
		{Volume: "a", At: tm("2026-09-01 04:00")},
	}
	got, err := expired(pts, "30d", now, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "b,a" {
		t.Errorf("expired = %v; want b and a (older than 30 days), not c (21 days) or d (the newest)", got)
	}
	// every restore point older than retain: the newest is still kept
	got, _ = expired(pts, "1d", tm("2027-06-01 00:00"), "")
	if strings.Join(got, ",") != "c,b,a" {
		t.Errorf("expired = %v; the newest restore point must survive even when it is past retain, so a backup is never pruned to nothing", got)
	}
	// the one just made is protected even if it is not the newest by timestamp (clock skew)
	got, _ = expired(pts, "1d", tm("2027-06-01 00:00"), "c")
	if strings.Join(got, ",") != "b,a" {
		t.Errorf("expired = %v; the `keep` volume must be protected", got)
	}
	// boundary: exactly at the cutoff is not yet expired
	got, _ = expired([]RestorePoint{{Volume: "n", At: tm("2026-11-30 04:00")}, {Volume: "x", At: tm("2026-11-01 12:00")}}, "30d", tm("2026-12-01 12:00"), "")
	if len(got) != 0 {
		t.Errorf("expired = %v; a point exactly retain old is not older than retain", got)
	}
	if _, err := expired(pts, "forever", now, ""); err == nil {
		t.Error("an invalid retain must be an error, not 'prune nothing' or 'prune everything'")
	}
	if got, _ := expired(nil, "30d", now, ""); len(got) != 0 {
		t.Errorf("no restore points, nothing to prune: %v", got)
	}
}

func TestPickRestorePoint(t *testing.T) {
	pts := []RestorePoint{{Volume: "lib-bk-20261003-040000"}, {Volume: "lib-bk-20261002-040000"}}
	for name, tc := range map[string]struct{ wanted, want, wantErr string }{
		"newest by default":         {"", "lib-bk-20261003-040000", ""},
		"by full volume name":       {"lib-bk-20261002-040000", "lib-bk-20261002-040000", ""},
		"by the stamp inside it":    {"20261002-040000", "lib-bk-20261002-040000", ""},
		"unknown lists what exists": {"20250101-000000", "", "have: lib-bk-20261003-040000, lib-bk-20261002-040000"},
	} {
		got, err := pickRestorePoint(pts, tc.wanted)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.Volume != tc.want {
			t.Errorf("%s: got %q, %v; want %q", name, got.Volume, err, tc.want)
		}
	}
	if _, err := pickRestorePoint(nil, ""); err == nil || !strings.Contains(err.Error(), "no restore points") {
		t.Errorf("an empty target must say there are no restore points: %v", err)
	}
}

func TestCopyRefusesWhatCannotBeABackup(t *testing.T) {
	srv := newFake("tron", "default", "fast")
	srv.add("default", "lib", nil)
	v := Volume{Name: "lib"}
	if _, err := Copy(srv, v, Target{Name: "x"}, CopyOptions{}); err == nil || !strings.Contains(err.Error(), "no pool") {
		t.Errorf("a target without a pool: %v", err)
	}
	if _, err := Copy(srv, v, Target{Name: "same", Pool: "default"}, CopyOptions{}); err == nil || !strings.Contains(err.Error(), "same failure domain") {
		t.Errorf("a copy to the volume's own pool is not a backup: %v", err)
	}
	if _, err := Copy(srv, Volume{Name: "lib", Pool: "fast"}, Target{Name: "same", Pool: "fast"}, CopyOptions{}); err == nil {
		t.Error("the volume's own pool is whatever the volume's pool is, not just 'default'")
	}
}

func TestNamesForCopies(t *testing.T) {
	at := time.Date(2026, 10, 7, 4, 5, 6, 0, time.UTC)
	if got := "lib-bk-" + stamped(at); got != "lib-bk-20261007-040506" {
		t.Errorf("restore point name = %q", got)
	}
}
