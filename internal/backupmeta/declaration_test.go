package backupmeta

import (
	"testing"
)

func TestSnapshotConfig(t *testing.T) {
	got := SnapshotConfig(&VolumeBackup{Snapshots: &SnapshotPolicy{Schedule: " @daily ", Retain: "14d "}})
	if len(got) != 2 || got["snapshots.schedule"] != "@daily" || got["snapshots.expiry"] != "14d" {
		t.Errorf("config = %v", got)
	}
	if SnapshotConfig(nil) != nil || SnapshotConfig(&VolumeBackup{None: "x"}) != nil {
		t.Error("no block or an opt-out must not set any Incus keys")
	}
}
