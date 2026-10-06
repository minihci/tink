package volbackup

import (
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

func snap(name string, created time.Time) api.StorageVolumeSnapshot {
	return api.StorageVolumeSnapshot{Name: name, CreatedAt: created}
}

func TestPickSnapshot(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	snaps := []api.StorageVolumeSnapshot{
		snap("snap0", t0),
		snap("snap2", t0.Add(48*time.Hour)),
		snap("snap1", t0.Add(24*time.Hour)),
	}
	tests := []struct {
		name    string
		snaps   []api.StorageVolumeSnapshot
		wanted  string
		want    string
		wantErr string
	}{
		{"most recent by creation time, not by name or position", snaps, "", "snap2", ""},
		{"a named snapshot", snaps, "snap1", "snap1", ""},
		{"the volume/ prefix is accepted", snaps, "vol/snap0", "snap0", ""},
		{"listings that carry the volume/ prefix still work", []api.StorageVolumeSnapshot{snap("v/a", t0), snap("v/b", t0.Add(time.Hour))}, "", "b", ""},
		{"unknown name lists what exists", snaps, "nope", "", "have: snap0, snap1, snap2"},
		{"no snapshots at all", nil, "", "", "no snapshots to restore from"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pickSnapshot(tc.snaps, tc.wanted)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestNames(t *testing.T) {
	at := time.Date(2026, 10, 6, 15, 4, 5, 0, time.FixedZone("MDT", -6*3600))
	if got := restoreName("immich-library", at); got != "immich-library-restore-20261006-210405" {
		t.Errorf("restoreName = %q (the stamp is UTC, whatever zone the clock is in)", got)
	}
	if got := scratchName("immich-library", at); got != "immich-library-verify-20261006-210405" {
		t.Errorf("scratchName = %q", got)
	}
}

func TestInstanceNameIsValidForIncus(t *testing.T) {
	at := time.Date(2026, 10, 6, 21, 4, 5, 0, time.UTC)
	for _, vol := range []string{
		"immich-library",
		"has_underscores.and dots",
		strings.Repeat("a-very-long-volume-name-", 5),
		"-leading-and-trailing-",
	} {
		got := instanceName(vol, at)
		if len(got) > 63 {
			t.Errorf("instanceName(%q) = %q: %d chars, Incus allows 63", vol, got, len(got))
		}
		if strings.ContainsAny(got, "_. ") || strings.HasSuffix(got, "-") || !strings.HasPrefix(got, "tink-verify-") {
			t.Errorf("instanceName(%q) = %q: must be hostname-safe and recognisable as tink's", vol, got)
		}
		if !strings.HasSuffix(got, "-20261006-210405") {
			t.Errorf("instanceName(%q) = %q: must keep the timestamp even when the name is truncated", vol, got)
		}
	}
}
