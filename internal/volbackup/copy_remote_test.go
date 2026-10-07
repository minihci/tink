package volbackup

import (
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// fakeIncus is just enough of an Incus server to run the copy engine against: storage volumes and their
// snapshots in memory. Methods the engine does not use panic (nil embedded interface), so a test fails
// loudly if the engine starts doing something these tests do not know about.
type fakeIncus struct {
	incus.InstanceServer
	name  string
	pools map[string]bool
	vols  map[string]*api.StorageVolume // "pool/name"
	snaps map[string][]string           // "pool/name" -> snapshot names

	failCopy    error // the next copy into this server fails part way, leaving a partial volume
	failDelete  bool  // deleting a volume fails (e.g. the connection died)
	modesSeen   []string
	copiesFrom  []string
	deletedVols []string
}

func newFake(name string, pools ...string) *fakeIncus {
	f := &fakeIncus{name: name, pools: map[string]bool{}, vols: map[string]*api.StorageVolume{}, snaps: map[string][]string{}}
	for _, p := range pools {
		f.pools[p] = true
	}
	return f
}

func (f *fakeIncus) add(pool, name string, cfg map[string]string) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	f.vols[pool+"/"+name] = &api.StorageVolume{Name: name, Type: "custom", ContentType: "filesystem", StorageVolumePut: api.StorageVolumePut{Config: cfg}}
}

func (f *fakeIncus) names(pool string) []string {
	var out []string
	for k := range f.vols {
		if strings.HasPrefix(k, pool+"/") {
			out = append(out, strings.TrimPrefix(k, pool+"/"))
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeIncus) UseProject(string) incus.InstanceServer { return f }

func (f *fakeIncus) GetStoragePool(name string) (*api.StoragePool, string, error) {
	if !f.pools[name] {
		return nil, "", errors.New("storage pool not found")
	}
	return &api.StoragePool{Name: name}, "", nil
}

func (f *fakeIncus) GetStoragePoolVolume(pool, _, name string) (*api.StorageVolume, string, error) {
	v, ok := f.vols[pool+"/"+name]
	if !ok {
		return nil, "", errors.New("storage volume not found")
	}
	c := *v
	c.Config = map[string]string{}
	for k, val := range v.Config {
		c.Config[k] = val
	}
	return &c, "etag", nil
}

func (f *fakeIncus) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	var out []api.StorageVolume
	for _, n := range f.names(pool) {
		out = append(out, *f.vols[pool+"/"+n])
	}
	return out, nil
}

func (f *fakeIncus) UpdateStoragePoolVolume(pool, _, name string, put api.StorageVolumePut, _ string) error {
	v, ok := f.vols[pool+"/"+name]
	if !ok {
		return errors.New("storage volume not found")
	}
	v.StorageVolumePut = put
	return nil
}

func (f *fakeIncus) DeleteStoragePoolVolume(pool, _, name string) error {
	if f.failDelete {
		return errors.New("connection lost")
	}
	delete(f.vols, pool+"/"+name)
	f.deletedVols = append(f.deletedVols, pool+"/"+name)
	return nil
}

type localOp struct {
	incus.Operation
	err error
}

func (o localOp) Wait() error { return o.err }

type remoteOp struct {
	incus.RemoteOperation
	err error
}

func (o remoteOp) Wait() error { return o.err }

func (f *fakeIncus) CreateStoragePoolVolumeSnapshot(pool, _, name string, snap api.StorageVolumeSnapshotsPost) (incus.Operation, error) {
	f.snaps[pool+"/"+name] = append(f.snaps[pool+"/"+name], snap.Name)
	return localOp{}, nil
}

func (f *fakeIncus) DeleteStoragePoolVolumeSnapshot(pool, _, name, snap string) (incus.Operation, error) {
	k := pool + "/" + name
	var keep []string
	for _, s := range f.snaps[k] {
		if s != snap {
			keep = append(keep, s)
		}
	}
	f.snaps[k] = keep
	return localOp{}, nil
}

// CopyStoragePoolVolume is called on the DESTINATION, as in the Incus client.
func (f *fakeIncus) CopyStoragePoolVolume(pool string, src incus.InstanceServer, srcPool string, vol api.StorageVolume, args *incus.StoragePoolVolumeCopyArgs) (incus.RemoteOperation, error) {
	f.modesSeen = append(f.modesSeen, args.Mode)
	f.copiesFrom = append(f.copiesFrom, srcPool+"/"+vol.Name)
	f.add(pool, args.Name, vol.Config) // like Incus, a copy takes the config it is given
	if f.failCopy != nil {
		err := f.failCopy
		f.failCopy = nil
		return remoteOp{err: err}, nil // the volume is left behind, as if the stream was cut
	}
	return remoteOp{}, nil
}

func useRemote(t *testing.T, name string, r *fakeIncus) {
	t.Helper()
	old := connectRemote
	connectRemote = func(n string) (incus.InstanceServer, error) {
		if n != name {
			return nil, errors.New("remote not configured")
		}
		return r, nil
	}
	t.Cleanup(func() { connectRemote = old })
}

var remoteNow = time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)

func remoteTarget() Target { return Target{Name: "vps", Remote: "vps"} }

func TestCopyToARemoteServer(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)

	owner := resolve.CopyOf("", "default", "lib")
	old := time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	older := time.Date(2026, 7, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20260925-040000", map[string]string{resolve.MarkerCopyOf: owner, resolve.MarkerCopyAt: old})
	remote.add("default", "lib-bk-20260701-040000", map[string]string{resolve.MarkerCopyOf: owner, resolve.MarkerCopyAt: older})
	remote.add("default", "lib-bk-20200101-000000", nil) // looks like ours, carries no marker
	remote.add("default", "other-bk-20260701-040000", map[string]string{resolve.MarkerCopyOf: resolve.CopyOf("", "default", "other"), resolve.MarkerCopyAt: older})

	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Retain: "30d", Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}

	// the data went to the remote, relayed through tink, and nowhere else
	if got := remote.modesSeen; len(got) != 1 || got[0] != "relay" {
		t.Errorf("a remote must be relayed through tink, transfer modes = %v", got)
	}
	if len(local.modesSeen) != 0 {
		t.Errorf("nothing may be copied into the source server: %v", local.copiesFrom)
	}
	if want := "default/lib/" + res.Snapshot; len(remote.copiesFrom) != 1 || remote.copiesFrom[0] != want {
		t.Errorf("must copy the snapshot, got %v want %s", remote.copiesFrom, want)
	}
	// the new restore point has its markers; the temporary snapshot is gone
	rp := remote.vols["default/"+res.Volume]
	if rp == nil || rp.Config[resolve.MarkerCopyOf] != owner || rp.Config[resolve.MarkerCopyTarget] != "vps" {
		t.Fatalf("the restore point must carry its markers: %+v", rp)
	}
	if got := local.snaps["default/lib"]; len(got) != 0 {
		t.Errorf("temporary snapshot left on the source: %v", got)
	}
	// the source is stamped
	if local.vols["default/lib"].Config[resolve.CopyStampAt("vps")] == "" || local.vols["default/lib"].Config[resolve.CopyStampVolume("vps")] != res.Volume {
		t.Errorf("source not stamped: %v", local.vols["default/lib"].Config)
	}
	// pruning removed exactly the older marked point on the remote, not the newest, not look-alikes
	if len(res.Pruned) != 1 || res.Pruned[0] != "lib-bk-20260701-040000" {
		t.Errorf("pruned %v, want only the oldest marked restore point", res.Pruned)
	}
	for _, keep := range []string{"lib-bk-20260925-040000", "lib-bk-20200101-000000", "other-bk-20260701-040000", res.Volume} {
		if remote.vols["default/"+keep] == nil {
			t.Errorf("%s must survive pruning", keep)
		}
	}
	if len(local.deletedVols) != 0 {
		t.Errorf("pruning must act on the target, not the source: %v", local.deletedVols)
	}
}

func TestCopyToARemoteThatIsCutOffLeavesNoRestorePoint(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	remote.failCopy = errors.New("tunnel dropped")
	useRemote(t, "vps", remote)

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err == nil || !strings.Contains(err.Error(), "tunnel dropped") {
		t.Fatalf("a cut-off copy must fail with the reason: %v", err)
	}
	if got := remote.names("default"); len(got) != 0 {
		t.Errorf("the partial volume must be removed: %v", got)
	}
	if got := local.snaps["default/lib"]; len(got) != 0 {
		t.Errorf("temporary snapshot left on the source: %v", got)
	}
	if local.vols["default/lib"].Config[resolve.CopyStampAt("vps")] != "" {
		t.Error("a failed copy must not stamp the source as copied")
	}
}

func TestCutOffCopyThatCannotBeCleanedUpIsNeverARestorePoint(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	remote.failCopy = errors.New("tunnel dropped")
	remote.failDelete = true // the connection is gone, so the cleanup fails too
	useRemote(t, "vps", remote)

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err == nil || !strings.Contains(err.Error(), "delete it by hand") {
		t.Fatalf("an uncleaned partial volume must be named for the user: %v", err)
	}
	if got := remote.names("default"); len(got) != 1 {
		t.Fatalf("expected the partial volume to be left behind: %v", got)
	}
	// the partial volume has no markers, so nothing can ever list it as a backup
	pts, err := ListRestorePoints(local, Volume{Name: "lib"}, remoteTarget())
	if err != nil || len(pts) != 0 {
		t.Errorf("a partial volume must never be a restore point: %v %v", pts, err)
	}
}

func TestUnreachableRemoteFailsBeforeTouchingTheSource(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	useRemote(t, "elsewhere", newFake("x")) // "vps" is not configured

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{})
	if err == nil || !strings.Contains(err.Error(), `remote "vps"`) {
		t.Fatalf("the error must name the remote: %v", err)
	}
	if len(local.snaps["default/lib"]) != 0 || len(local.vols["default/lib"].Config) != 0 {
		t.Error("an unreachable target must not leave a snapshot or stamp on the source")
	}
}

func TestRemoteDryRunChangesNothing(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)

	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{DryRun: true, Retain: "30d", Now: func() time.Time { return remoteNow }})
	if err != nil || len(res.Planned) == 0 {
		t.Fatalf("dry run: %v %+v", err, res)
	}
	if !strings.Contains(strings.Join(res.Planned, "\n"), "vps:default/") {
		t.Errorf("the plan must say the copy goes to the remote: %v", res.Planned)
	}
	if len(remote.vols) != 0 || len(local.snaps["default/lib"]) != 0 || len(remote.modesSeen) != 0 {
		t.Error("a dry run changed something")
	}
}

func TestRestoreFromARemoteScrubsMarkersAndNeedsNoSourceVolume(t *testing.T) {
	local := newFake("tron", "default") // the source volume is GONE
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	owner := resolve.CopyOf("", "default", "lib")
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20261001-040000", map[string]string{resolve.MarkerCopyOf: owner, resolve.MarkerCopyAt: at, resolve.MarkerCopyTarget: "vps"})

	res, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "lib-recovered"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Volume != "lib-recovered" || local.vols["default/lib-recovered"] == nil {
		t.Fatalf("the volume was not restored locally: %+v %v", res, local.names("default"))
	}
	if got := local.modesSeen; len(got) != 1 || got[0] != "relay" {
		t.Errorf("restore from a remote must relay too: %v", got)
	}
	for k := range local.vols["default/lib-recovered"].Config {
		if strings.HasPrefix(k, "user.tink.backup.copy-") {
			t.Errorf("the restored volume must not look like a restore point, found %s", k)
		}
	}
}

func TestRestoreFromARemoteThatFailsLeavesNoPartialVolume(t *testing.T) {
	local := newFake("tron", "default")
	local.failCopy = errors.New("tunnel dropped")
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20261001-040000", map[string]string{resolve.MarkerCopyOf: resolve.CopyOf("", "default", "lib"), resolve.MarkerCopyAt: at})

	_, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "lib-recovered"})
	if err == nil || !strings.Contains(err.Error(), "tunnel dropped") {
		t.Fatalf("a failed restore must say why: %v", err)
	}
	if local.vols["default/lib-recovered"] != nil {
		t.Error("a partial restored volume was left behind")
	}
}

func TestPoolTargetsAreUnchangedByRemoteSupport(t *testing.T) {
	local := newFake("tron", "default", "nas")
	local.add("default", "lib", nil)
	res, err := Copy(local, Volume{Name: "lib"}, Target{Name: "nas", Pool: "nas"}, CopyOptions{Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}
	if local.vols["nas/"+res.Volume] == nil || local.modesSeen[0] != "" {
		t.Errorf("a pool target copies within the server in the default mode: %v %v", local.names("nas"), local.modesSeen)
	}
	if (Target{Remote: "r"}).pool() != "default" || (Target{Remote: "r", Pool: "tank"}).pool() != "tank" || (Target{Pool: "nas"}).pool() != "nas" {
		t.Error("a remote target without a pool uses its default pool; an explicit pool wins")
	}
}
