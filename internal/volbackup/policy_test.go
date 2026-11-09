package volbackup

import (
	"testing"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/ownership"
)

const somePolicy = `{"proto":1,"copies":[{"target":{"name":"vps","remote":"vps"},"schedule":"@daily","retain":"7d"}]}`

// A volume's copy policy says what is to happen to that volume. Everything made by copying it (a restore point, a
// restored volume, a verify scratch volume) is a different volume, and must not carry the policy: a scheduler that
// discovers work by listing volumes would otherwise copy the copies.

func TestARestorePointDoesNotCarryTheSourcesPolicy(t *testing.T) {
	local := newFake("homelab", "default")
	local.add("default", "lib", map[string]string{backupmeta.PolicyKey: somePolicy})
	remote := newFake("vps", "default")
	remote.inherit = true // even if Incus copies the snapshot's config onto the new volume
	useRemote(t, "vps", remote)

	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := remote.vols["default/"+res.Volume].Config
	if _, has := cfg[backupmeta.PolicyKey]; has {
		t.Errorf("a restore point must not carry the policy: %v", cfg)
	}
	if cfg[backupmeta.MarkerCopyOf] == "" {
		t.Errorf("it is still a restore point: %v", cfg)
	}
	if _, has := local.vols["default/lib"].Config[backupmeta.PolicyKey]; !has {
		t.Error("the SOURCE keeps its policy")
	}
}

func TestARestoredVolumeDoesNotCarryThePolicyFromARestorePoint(t *testing.T) {
	local := newFake("homelab", "default") // the source is gone
	remote := newFake("vps", "default")
	remote.inherit = true
	useRemote(t, "vps", remote)
	owner := backupmeta.CopyOf("", "default", "lib")
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20261001-040000", map[string]string{
		backupmeta.MarkerCopyOf: owner, backupmeta.MarkerCopiedAt: at, backupmeta.MarkerCopyTarget: "vps", backupmeta.PolicyKey: somePolicy})
	local.inherit = true

	if _, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "lib-recovered"}); err != nil {
		t.Fatal(err)
	}
	if _, has := local.vols["default/lib-recovered"].Config[backupmeta.PolicyKey]; has {
		t.Errorf("the restored volume carries the policy: %v", local.vols["default/lib-recovered"].Config)
	}
}

func TestARestoreFromALocalSnapshotDoesNotCarryThePolicy(t *testing.T) {
	local := newFake("homelab", "default")
	local.inherit = true
	local.add("default", "lib", map[string]string{backupmeta.PolicyKey: somePolicy, "snapshots.schedule": "@daily"})
	local.snaps["default/lib"] = []string{"snap0"}

	if _, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{As: "lib-restored"}); err != nil {
		t.Fatal(err)
	}
	cfg := local.vols["default/lib-restored"].Config
	if _, has := cfg[backupmeta.PolicyKey]; has {
		t.Errorf("the restored volume carries the policy: %v", cfg)
	}
	if cfg["snapshots.schedule"] == "" {
		t.Errorf("only the policy is scrubbed, not the rest of the config: %v", cfg)
	}
}

// What tink's own copies keep and drop is decided by the class of the key (internal/ownership), not by a list of its own: what the object IS (the owner
// and resource stamps, and the person's own keys) is kept, so a copy reads as a copy of that object; what happened to the volume it was copied from (every
// outcome), the policy, and the record of what was applied to it, is dropped.
func sourceWithEverything() map[string]string {
	return map[string]string{
		ownership.Key:                     "photos",
		ownership.ResourceKey:             "storage-volume/lib",
		ownership.AppliedKey:              "{}",
		backupmeta.PolicyKey:              somePolicy,
		backupmeta.CopiedAt("vps"):        "2026-10-01T04:00:00Z",
		backupmeta.CopyStampVolume("vps"): "lib-bk-20261001-040000",
		backupmeta.FailedAt("vps"):        "2026-10-02T04:00:00Z",
		backupmeta.StampVerifiedAt:        "2026-10-01T05:00:00Z",
		"snapshots.schedule":              "@daily",
		"user.note":                       "mine",
	}
}

func checkACopyKeptWhatItIsAndDroppedWhatHappened(t *testing.T, what string, cfg map[string]string, restorePoint bool) {
	t.Helper()
	for _, kept := range []string{ownership.Key, ownership.ResourceKey, "snapshots.schedule", "user.note"} {
		if cfg[kept] == "" {
			t.Errorf("%s lost %s: %v", what, kept, cfg)
		}
	}
	for k := range cfg {
		if ownership.ClassOf(k) == ownership.Outcome && !(restorePoint && isMarker(k)) {
			t.Errorf("%s carries the outcome %s of the volume it was copied from: %v", what, k, cfg)
		}
	}
	for _, gone := range []string{backupmeta.PolicyKey, ownership.AppliedKey} {
		if _, has := cfg[gone]; has {
			t.Errorf("%s carries %s: %v", what, gone, cfg)
		}
	}
}

// isMarker is a key a restore point is made to carry: its own outcome markers, written after the copy.
func isMarker(k string) bool {
	switch k {
	case backupmeta.MarkerCopyOf, backupmeta.MarkerCopiedAt, backupmeta.MarkerCopyTarget, backupmeta.MarkerCopySnap, backupmeta.MarkerCopyServer:
		return true
	}
	return false
}

func TestACopyKeepsWhatTheVolumeIsAndDropsWhatHappenedToTheSource(t *testing.T) {
	t.Run("restore point", func(t *testing.T) {
		local := newFake("homelab", "default")
		local.add("default", "lib", sourceWithEverything())
		remote := newFake("vps", "default")
		remote.inherit = true
		useRemote(t, "vps", remote)
		res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
		if err != nil {
			t.Fatal(err)
		}
		cfg := remote.vols["default/"+res.Volume].Config
		checkACopyKeptWhatItIsAndDroppedWhatHappened(t, "the restore point", cfg, true)
		if cfg[backupmeta.MarkerCopyOf] == "" {
			t.Errorf("it is still a restore point: %v", cfg)
		}
		if _, has := cfg[backupmeta.MarkerPartialOf]; has {
			t.Errorf("a finished restore point does not carry the in-progress mark: %v", cfg)
		}
	})
	t.Run("restored from a restore point", func(t *testing.T) {
		local := newFake("homelab", "default")
		local.inherit = true
		remote := newFake("vps", "default")
		remote.inherit = true
		useRemote(t, "vps", remote)
		cfg := sourceWithEverything()
		cfg[backupmeta.MarkerCopyOf] = backupmeta.CopyOf("", "default", "lib")
		cfg[backupmeta.MarkerCopiedAt] = time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
		cfg[backupmeta.MarkerCopyTarget] = "vps"
		remote.add("default", "lib-bk-20261001-040000", cfg)
		if _, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "lib-recovered"}); err != nil {
			t.Fatal(err)
		}
		got := local.vols["default/lib-recovered"].Config
		checkACopyKeptWhatItIsAndDroppedWhatHappened(t, "the restored volume", got, false)
		if _, has := got[backupmeta.MarkerCopyOf]; has {
			t.Errorf("a restored volume is not a restore point: %v", got)
		}
	})
	t.Run("restored from a local snapshot", func(t *testing.T) {
		local := newFake("homelab", "default")
		local.inherit = true
		local.add("default", "lib", sourceWithEverything())
		local.snaps["default/lib"] = []string{"snap0"}
		if _, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{As: "lib-restored"}); err != nil {
			t.Fatal(err)
		}
		checkACopyKeptWhatItIsAndDroppedWhatHappened(t, "the restored volume", local.vols["default/lib-restored"].Config, false)
	})
}
