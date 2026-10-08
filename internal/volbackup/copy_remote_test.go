package volbackup

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
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

	failCopy   error         // the next copy into this server fails part way, leaving a partial volume
	entered    chan struct{} // if set, a copy signals here when it starts, then waits for release
	release    chan struct{}
	failDelete bool // deleting a volume fails (e.g. the connection died)
	// inherit makes a copy carry the source volume's own config under the config it was given: the worst case for
	// what Incus may do with a copy from a snapshot, which tink must not depend on.
	inherit  bool
	failPool map[string]error // listing this pool fails
	// getFails makes reading one volume ("pool/name") fail with err (a 403, say, not a 404) from the call after the first `after` ones.
	getFails    map[string]getFail
	getCalls    map[string]int
	modesSeen   []string
	copiesFrom  []string
	deletedVols []string
	// contentTypes are the content types a copy was asked to carry, one per copy.
	contentTypes []string

	// More ways to fail, for the branches of a copy that a clean server never reaches.
	failSnapCreate error            // taking a snapshot fails at once
	failSnapWait   error            // taking a snapshot fails when it is waited for
	failSnapDelete error            // removing a snapshot fails
	failServer     error            // reading the server's name fails
	failUpdate     map[string]error // updating one volume ("pool/name") fails
	failList       map[string]error // listing this pool's volumes (the project's own) fails
	failListOnce   bool             // ... but only the first time, so what comes after it can succeed
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

func (f *fakeIncus) GetServer() (*api.Server, string, error) {
	if f.failServer != nil {
		return nil, "", f.failServer
	}
	return &api.Server{Environment: api.ServerEnvironment{ServerName: f.name}}, "", nil
}

func (f *fakeIncus) GetStoragePool(name string) (*api.StoragePool, string, error) {
	if !f.pools[name] {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "storage pool not found")
	}
	return &api.StoragePool{Name: name}, "", nil
}

type getFail struct {
	err   error
	after int
}

func (f *fakeIncus) GetStoragePoolVolume(pool, _, name string) (*api.StorageVolume, string, error) {
	k := pool + "/" + name
	if f.getCalls == nil {
		f.getCalls = map[string]int{}
	}
	f.getCalls[k]++
	if g, ok := f.getFails[k]; ok && f.getCalls[k] > g.after {
		return nil, "", g.err
	}
	v, ok := f.vols[k]
	if !ok {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "storage volume not found")
	}
	c := *v
	c.Config = map[string]string{}
	for k, val := range v.Config {
		c.Config[k] = val
	}
	return &c, "etag", nil
}

func (f *fakeIncus) GetStoragePoolNames() ([]string, error) {
	var out []string
	for p := range f.pools {
		out = append(out, p)
	}
	return out, nil
}

// GetStoragePoolVolumesAllProjects is every volume of the pool; a volume's Project is whatever the test set on it.
func (f *fakeIncus) GetStoragePoolVolumesAllProjects(pool string) ([]api.StorageVolume, error) {
	if f.failPool[pool] != nil {
		return nil, f.failPool[pool]
	}
	return f.GetStoragePoolVolumes(pool)
}

func (f *fakeIncus) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	if err := f.failList[pool]; err != nil {
		if f.failListOnce {
			delete(f.failList, pool)
		}
		return nil, err
	}
	var out []api.StorageVolume
	for _, n := range f.names(pool) {
		out = append(out, *f.vols[pool+"/"+n])
	}
	return out, nil
}

func (f *fakeIncus) GetStoragePoolVolumeSnapshots(pool, _, name string) ([]api.StorageVolumeSnapshot, error) {
	var out []api.StorageVolumeSnapshot
	for _, n := range f.snaps[pool+"/"+name] {
		out = append(out, api.StorageVolumeSnapshot{Name: name + "/" + n})
	}
	return out, nil
}

func (f *fakeIncus) UpdateStoragePoolVolume(pool, _, name string, put api.StorageVolumePut, _ string) error {
	if err := f.failUpdate[pool+"/"+name]; err != nil {
		return err
	}
	v, ok := f.vols[pool+"/"+name]
	if !ok {
		return api.StatusErrorf(http.StatusNotFound, "storage volume not found")
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
	if f.failSnapCreate != nil {
		return nil, f.failSnapCreate
	}
	f.snaps[pool+"/"+name] = append(f.snaps[pool+"/"+name], snap.Name)
	return localOp{err: f.failSnapWait}, nil
}

func (f *fakeIncus) DeleteStoragePoolVolumeSnapshot(pool, _, name, snap string) (incus.Operation, error) {
	if f.failSnapDelete != nil {
		return nil, f.failSnapDelete
	}
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
	// like Incus, the new volume exists, with the config it was given, from the moment the copy starts
	cfg := map[string]string{}
	if sf, ok := src.(*fakeIncus); ok && f.inherit {
		if from, ok := sf.vols[srcPool+"/"+strings.SplitN(vol.Name, "/", 2)[0]]; ok {
			for k, v := range from.Config {
				cfg[k] = v
			}
		}
	}
	for k, v := range vol.Config {
		cfg[k] = v
	}
	f.add(pool, args.Name, cfg)
	if f.entered != nil {
		// bounded, so a regression fails the test instead of hanging it
		select {
		case f.entered <- struct{}{}:
		case <-time.After(2 * time.Second):
			return nil, errors.New("test: nobody was waiting for this copy to start")
		}
		select {
		case <-f.release:
		case <-time.After(10 * time.Second):
			return nil, errors.New("test: this copy was never released")
		}
	}
	f.modesSeen = append(f.modesSeen, args.Mode)
	f.copiesFrom = append(f.copiesFrom, srcPool+"/"+vol.Name)
	f.contentTypes = append(f.contentTypes, vol.ContentType)
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

	owner := backupmeta.CopyOf("", "default", "lib")
	old := time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	older := time.Date(2026, 7, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20260925-040000", map[string]string{backupmeta.MarkerCopyOf: owner, backupmeta.MarkerCopyAt: old})
	remote.add("default", "lib-bk-20260701-040000", map[string]string{backupmeta.MarkerCopyOf: owner, backupmeta.MarkerCopyAt: older})
	remote.add("default", "lib-bk-20200101-000000", nil) // looks like ours, carries no marker
	remote.add("default", "other-bk-20260701-040000", map[string]string{backupmeta.MarkerCopyOf: backupmeta.CopyOf("", "default", "other"), backupmeta.MarkerCopyAt: older})

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
	if rp == nil || rp.Config[backupmeta.MarkerCopyOf] != owner || rp.Config[backupmeta.MarkerCopyTarget] != "vps" {
		t.Fatalf("the restore point must carry its markers: %+v", rp)
	}
	if got := local.snaps["default/lib"]; len(got) != 0 {
		t.Errorf("temporary snapshot left on the source: %v", got)
	}
	// the source is stamped
	if local.vols["default/lib"].Config[backupmeta.CopyStampAt("vps")] == "" || local.vols["default/lib"].Config[backupmeta.CopyStampVolume("vps")] != res.Volume {
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
	if local.vols["default/lib"].Config[backupmeta.CopyStampAt("vps")] != "" {
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
	if len(local.snaps["default/lib"]) != 0 {
		t.Error("an unreachable target must not leave a snapshot on the source")
	}
	cfg := local.vols["default/lib"].Config
	if cfg[backupmeta.CopyStampAt("vps")] != "" {
		t.Error("an unreachable target must not stamp the copy as done")
	}
	if cfg[backupmeta.CopyFailCount("vps")] != "1" {
		t.Errorf("it must record exactly one failed attempt, and nothing else: %v", cfg)
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
	owner := backupmeta.CopyOf("", "default", "lib")
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20261001-040000", map[string]string{backupmeta.MarkerCopyOf: owner, backupmeta.MarkerCopyAt: at, backupmeta.MarkerCopyTarget: "vps"})

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
	remote.add("default", "lib-bk-20261001-040000", map[string]string{backupmeta.MarkerCopyOf: backupmeta.CopyOf("", "default", "lib"), backupmeta.MarkerCopyAt: at})

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
