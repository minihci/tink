package volbackup

import (
	"net/http"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

var forbidden = api.StatusErrorf(http.StatusForbidden, "not authorized")

// A read that fails says nothing about whether the volume is there. These pin what each use of volumeExists does about that.

func TestVolumeExistsSeparatesAbsentFromFailed(t *testing.T) {
	f := newFake("tron", "default")
	f.add("default", "lib", nil)
	if ok, err := volumeExists(f, "default", "lib"); !ok || err != nil {
		t.Errorf("present: %v %v", ok, err)
	}
	if ok, err := volumeExists(f, "default", "nope"); ok || err != nil {
		t.Errorf("a 404 is absent, and not an error: %v %v", ok, err)
	}
	f.getFails = map[string]getFail{"default/lib": {err: forbidden}}
	if ok, err := volumeExists(f, "default", "lib"); ok || err == nil {
		t.Errorf("a 403 is a failure, never 'absent': %v %v", ok, err)
	}
}

func TestRestoreWillNotGoAheadWhenItCannotTellWhetherTheNewVolumeExists(t *testing.T) {
	f := newFake("tron", "default")
	f.add("default", "lib", nil)
	f.snaps["default/lib"] = []string{"snap0"}
	f.getFails = map[string]getFail{"default/new": {err: forbidden}}

	_, err := Restore(f, Volume{Name: "lib"}, RestoreOptions{As: "new"})
	if err == nil || !strings.Contains(err.Error(), "checking whether volume default/new already exists") {
		t.Fatalf("err = %v", err)
	}
	if len(f.copiesFrom) != 0 || f.vols["default/new"] != nil {
		t.Errorf("a guard that proceeds on a failed read overwrites or double-creates: copied from %v", f.copiesFrom)
	}
}

func TestACopyThatFailsAndCannotCheckForItsPartialVolumeSaysSo(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	remote.failCopy = api.StatusErrorf(http.StatusInternalServerError, "the tunnel dropped")
	useRemote(t, "vps", remote)
	name := "default/lib-bk-" + stamped(remoteNow)
	// the first look-up (the guard against a name clash) passes; the one in the cleanup after the failed copy does not
	remote.getFails = map[string]getFail{name: {err: forbidden, after: 1}}

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err == nil || !strings.Contains(err.Error(), "the tunnel dropped") {
		t.Fatalf("the copy's own error comes first: %v", err)
	}
	if !strings.Contains(err.Error(), "could not check whether a partial volume") || !strings.Contains(err.Error(), "delete it by hand") {
		t.Errorf("a cleanup that cannot look must say so, not stay silent: %v", err)
	}
	if len(remote.deletedVols) != 0 {
		t.Errorf("nothing is deleted on a failed look-up: %v", remote.deletedVols)
	}
}

func TestACopyWillNotStartWhenItCannotTellWhetherTheRestorePointNameIsTaken(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.getFails = map[string]getFail{"default/lib-bk-" + stamped(remoteNow): {err: forbidden}}

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err == nil || !strings.Contains(err.Error(), "checking whether") {
		t.Fatalf("err = %v", err)
	}
	if len(remote.copiesFrom) != 0 || len(local.snaps["default/lib"]) != 0 {
		t.Errorf("nothing may be snapshotted or copied: %v %v", remote.copiesFrom, local.snaps)
	}
}

// instanceOnly answers GetInstance and nothing else: any other call (a stop, a delete) panics, so a test fails loudly if
// deleteInstance goes on to act when it should not.
type instanceOnly struct {
	incus.InstanceServer
	err error
}

func (s instanceOnly) GetInstance(n string) (*api.Instance, string, error) {
	return &api.Instance{Name: n}, "", s.err
}

func TestDeleteInstanceDoesNotSilentlySkipWhenItCannotLook(t *testing.T) {
	if err := deleteInstance(instanceOnly{err: api.StatusErrorf(http.StatusNotFound, "gone")}, "verify-1"); err != nil {
		t.Errorf("an instance that was never created is fine: %v", err)
	}
	err := deleteInstance(instanceOnly{err: forbidden}, "verify-1")
	if err == nil || !strings.Contains(err.Error(), "verify-1") || !strings.Contains(err.Error(), "not removed") {
		t.Errorf("a failed look-up leaves the throwaway instance behind, and must say which one: %v", err)
	}
}
