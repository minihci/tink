package volbackup

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
)

func point(name, server, at string) map[string]string {
	c := map[string]string{backupmeta.MarkerCopyOf: backupmeta.CopyOf("", "default", "lib"), backupmeta.MarkerCopyAt: at}
	if server != "" {
		c[backupmeta.MarkerCopyServer] = server
	}
	return c
}

func TestACopyRecordsWhichServerMadeIt(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}
	// the SOURCE server's name, not the target's and not this machine's
	if got := remote.vols["default/"+res.Volume].Config[backupmeta.MarkerCopyServer]; got != "tron" {
		t.Errorf("copy-server = %q, want the source server's name %q", got, "tron")
	}
}

// Two servers copy a volume of the same name into one target. Each prunes only what it made.
func TestPruneLeavesAnotherServersRestorePointsAlone(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)

	oldA := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	oldB := time.Date(2026, 8, 2, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	oldC := time.Date(2026, 8, 3, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	recent := time.Date(2026, 10, 6, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-mine-old", point("", "tron", oldA))
	remote.add("default", "lib-bk-legacy-old", point("", "", oldB))           // made before the marker existed: treated as ours
	remote.add("default", "lib-bk-theirs-old", point("", "other-host", oldC)) // another server's, and old enough to be pruned
	remote.add("default", "lib-bk-theirs-recent", point("", "other-host", recent))

	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Retain: "30d", Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"lib-bk-legacy-old", "lib-bk-mine-old"}; !sameSet(res.Pruned, want) {
		t.Errorf("pruned %v, want exactly our own old points %v", res.Pruned, want)
	}
	for _, keep := range []string{"lib-bk-theirs-old", "lib-bk-theirs-recent", res.Volume} {
		if remote.vols["default/"+keep] == nil {
			t.Errorf("%s must survive: another server's restore points are never pruned", keep)
		}
	}
	if !reflect.DeepEqual(res.OtherServers, []string{"other-host"}) {
		t.Errorf("the other server must be named so the caller can say so: %v", res.OtherServers)
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]bool{}
	for _, x := range a {
		m[x] = true
	}
	for _, y := range b {
		if !m[y] {
			return false
		}
	}
	return true
}

func TestADryRunPlansOnlyOurOwnPrunes(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	old := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-theirs", point("", "other-host", old))
	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{DryRun: true, Retain: "30d", Now: func() time.Time { return remoteNow }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(res.Planned, "\n"), "lib-bk-theirs") {
		t.Errorf("a dry run must not plan to prune another server's point: %v", res.Planned)
	}
	if len(remote.deletedVols) != 0 {
		t.Error("a dry run deleted something")
	}
}

func TestSplitByServer(t *testing.T) {
	pts := []RestorePoint{{Volume: "a", Server: "tron"}, {Volume: "b"}, {Volume: "c", Server: "zed"}, {Volume: "d", Server: "alpha"}, {Volume: "e", Server: "zed"}}
	own, others := splitByServer(pts, "tron")
	if len(own) != 2 || own[0].Volume != "a" || own[1].Volume != "b" {
		t.Errorf("own = %v: ours are the marked-as-us and the unmarked", own)
	}
	if !reflect.DeepEqual(others, []string{"alpha", "zed"}) {
		t.Errorf("others = %v: each other server once, sorted", others)
	}
}

// Restoring after a host is lost and rebuilt (maybe under another name) must find the old server's points, and say so.
func TestRestoreFindsAnotherServersPointsAndSaysSo(t *testing.T) {
	local := newFake("rebuilt-host", "default") // the source volume is gone and the server has a new name
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	at := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20261001-040000", point("", "tron", at))

	res, err := Restore(local, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "lib-recovered"})
	if err != nil {
		t.Fatal(err)
	}
	if res.MadeBy != "tron" {
		t.Errorf("the restore must say it used another server's backup: MadeBy = %q", res.MadeBy)
	}
	if local.vols["default/lib-recovered"] == nil {
		t.Error("the volume was not restored")
	}

	// and from its own point nothing is said
	local2 := newFake("tron", "default")
	remote2 := newFake("vps", "default")
	useRemote(t, "vps", remote2)
	remote2.add("default", "lib-bk-20261001-040000", point("", "tron", at))
	res, err = Restore(local2, Volume{Name: "lib"}, RestoreOptions{From: &Target{Name: "vps", Remote: "vps"}, As: "x"})
	if err != nil || res.MadeBy != "" {
		t.Errorf("a restore of this server's own point says nothing: %v MadeBy=%q", err, res.MadeBy)
	}
}

func TestTheSameCopyDoesNotRunTwiceAtOnce(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	remote.entered, remote.release = make(chan struct{}), make(chan struct{})
	useRemote(t, "vps", remote)

	done := make(chan error, 1)
	go func() {
		_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }})
		done <- err
	}()
	select {
	case <-remote.entered: // the first copy is now inside the transfer
	case <-time.After(5 * time.Second):
		t.Fatal("the first copy never started")
	}

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow.Add(time.Hour) }})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("a second copy of the same volume to the same target must be refused as busy: %v", err)
	}
	if got := local.vols["default/lib"].Config[backupmeta.CopyFailCount("vps")]; got != "" {
		t.Errorf("being busy is not a failed copy, but the failure count is %q", got)
	}
	// a different target is not blocked by it: the guard is per (volume, target)
	local.pools["nas"] = true
	if _, err := Copy(local, Volume{Name: "lib"}, Target{Name: "nas", Pool: "nas"}, CopyOptions{Now: func() time.Time { return remoteNow }}); err != nil {
		t.Errorf("a different target must not be blocked: %v", err)
	}

	remote.release <- struct{}{}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the first copy must still finish: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the first copy never finished")
	}
	// released: the same copy can run again
	remote.entered, remote.release = nil, nil
	if _, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow.Add(2 * time.Hour) }}); err != nil {
		t.Errorf("after the first finished, the guard must be released: %v", err)
	}
}

func TestTheGuardIsReleasedAfterAFailureToo(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.failCopy = errors.New("down")
	if _, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow }}); err == nil {
		t.Fatal("expected a failure")
	}
	if _, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return remoteNow.Add(time.Hour) }}); err != nil {
		t.Errorf("a failed copy must release the guard: %v", err)
	}
}
