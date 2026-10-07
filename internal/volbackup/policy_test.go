package volbackup

import (
	"testing"
	"time"

	"github.com/minihci/tink/internal/resolve"
)

const somePolicy = `{"proto":1,"copies":[{"target":{"name":"vps","remote":"vps"},"schedule":"@daily","retain":"7d"}]}`

// A volume's copy policy says what is to happen to that volume. Everything made by copying it (a restore point, a
// restored volume, a verify scratch volume) is a different volume, and must not carry the policy: a scheduler that
// discovers work by listing volumes would otherwise copy the copies.

func TestARestorePointDoesNotCarryTheSourcesPolicy(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", map[string]string{resolve.PolicyKey: somePolicy})
	remote := newFake("vps", "default")
	remote.inherit = true // even if Incus copies the snapshot's config onto the new volume
	useRemote(t, "vps", remote)

	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := remote.vols["default/"+res.Volume].Config
	if _, has := cfg[resolve.PolicyKey]; has {
		t.Errorf("a restore point must not carry the policy: %v", cfg)
	}
	if cfg[resolve.MarkerCopyOf] == "" {
		t.Errorf("it is still a restore point: %v", cfg)
	}
	if _, has := local.vols["default/lib"].Config[resolve.PolicyKey]; !has {
		t.Error("the SOURCE keeps its policy")
	}
}

func TestARestoredVolumeDoesNotCarryThePolicyFromARestorePoint(t *testing.T) {
	local := newFake("tron", "default") // the source is gone
	remote := newFake("vps", "default")
	remote.inherit = true
	useRemote(t, "vps", remote)
	owner := resolve.CopyOf("", "default", "lib")
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20261001-040000", map[string]string{
		resolve.MarkerCopyOf: owner, resolve.MarkerCopyAt: at, resolve.MarkerCopyTarget: "vps", resolve.PolicyKey: somePolicy})
	local.inherit = true

	if _, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "lib-recovered"}); err != nil {
		t.Fatal(err)
	}
	if _, has := local.vols["default/lib-recovered"].Config[resolve.PolicyKey]; has {
		t.Errorf("the restored volume carries the policy: %v", local.vols["default/lib-recovered"].Config)
	}
}

func TestARestoreFromALocalSnapshotDoesNotCarryThePolicy(t *testing.T) {
	local := newFake("tron", "default")
	local.inherit = true
	local.add("default", "lib", map[string]string{resolve.PolicyKey: somePolicy, "snapshots.schedule": "@daily"})
	local.snaps["default/lib"] = []string{"snap0"}

	if _, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{As: "lib-restored"}); err != nil {
		t.Fatal(err)
	}
	cfg := local.vols["default/lib-restored"].Config
	if _, has := cfg[resolve.PolicyKey]; has {
		t.Errorf("the restored volume carries the policy: %v", cfg)
	}
	if cfg["snapshots.schedule"] == "" {
		t.Errorf("only the policy is scrubbed, not the rest of the config: %v", cfg)
	}
}
