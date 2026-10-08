package backupmeta

import (
	"strings"
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

// A block can be wrong in several ways at once, and ValidateBackup reports one: what the block is, then verification, then copies, then
// snapshots. Pinned so that splitting or reordering the checks cannot quietly change which problem a person is told about first.
func TestValidateBackupReportsTheFirstProblemInTheDocumentedOrder(t *testing.T) {
	badCopy := []BackupCopy{{Target: "nas", Schedule: "whenever", Retain: "30d"}}
	twice := []BackupCopy{{Target: "nas", Schedule: "@daily", Retain: "30d"}, {Target: "nas", Schedule: "@daily", Retain: "30d"}}
	badSnap := &SnapshotPolicy{Schedule: "whenever", Retain: "7d"}
	tests := map[string]struct {
		b    VolumeBackup
		want string
	}{
		"the shape before the cadence":      {VolumeBackup{None: "regenerable", Verify: "hourly"}, "mutually exclusive"},
		"the shape before the copies":       {VolumeBackup{None: "regenerable", Copies: badCopy}, "mutually exclusive"},
		"the cadence before the copies":     {VolumeBackup{Copies: badCopy, Verify: "hourly"}, "daily, weekly or monthly"},
		"the check before the copies":       {VolumeBackup{Copies: twice, VerifyCheck: &VerifyCheck{Command: []string{"true"}}}, "needs image and command"},
		"the copies before the snapshots":   {VolumeBackup{Snapshots: badSnap, Copies: badCopy}, "backup.copies[0] (nas) schedule"},
		"a duplicate before a bad snapshot": {VolumeBackup{Snapshots: badSnap, Copies: twice}, "names target"},
		"the snapshots when nothing else":   {VolumeBackup{Snapshots: badSnap}, "backup.snapshots.schedule"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateBackup("vol", &tc.b); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
