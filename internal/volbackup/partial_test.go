package volbackup

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
)

var sweepNow = remoteNow // 2026-10-07 04:00 UTC

func partialCfg(volume, server string, started time.Time) map[string]string {
	c := map[string]string{
		backupmeta.MarkerPartialOf: backupmeta.CopyOf("", "default", volume),
		backupmeta.MarkerPartialAt: started.UTC().Format(time.RFC3339),
	}
	if server != "" {
		c[backupmeta.MarkerCopyServer] = server
	}
	return c
}

// While a copy is going on, the new volume is marked in progress and is NOT a restore point; when it completes the
// marks are swapped.
func TestACopyIsMarkedInProgressUntilItCompletes(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	remote.entered, remote.release = make(chan struct{}), make(chan struct{})
	useRemote(t, "vps", remote)

	done := make(chan error, 1)
	var res CopyResult
	go func() {
		var err error
		res, err = Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return sweepNow }})
		done <- err
	}()
	select {
	case <-remote.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the copy never started")
	}
	// mid-copy: the volume exists with the in-progress mark, and is not listed as a restore point
	var mid map[string]string
	for k, v := range remote.vols {
		if strings.HasPrefix(k, "default/lib-bk-") {
			mid = v.Config
		}
	}
	if mid[backupmeta.MarkerPartialOf] != backupmeta.CopyOf("", "default", "lib") || mid[backupmeta.MarkerPartialAt] == "" {
		t.Errorf("a copy under way must carry the in-progress mark: %v", mid)
	}
	if _, isPoint := mid[backupmeta.MarkerCopyOf]; isPoint {
		t.Errorf("a copy under way must NOT look like a restore point: %v", mid)
	}
	if pts, _ := ListRestorePoints(local, Volume{Name: "lib"}, remoteTarget()); len(pts) != 0 {
		t.Errorf("an unfinished copy must not be listed as a restore point: %v", pts)
	}
	remote.release <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final := remote.vols["default/"+res.Volume].Config
	if final[backupmeta.MarkerCopyOf] == "" {
		t.Errorf("a finished copy is a restore point: %v", final)
	}
	if _, left := final[backupmeta.MarkerPartialOf]; left {
		t.Error("the in-progress mark must be gone once the copy is finished")
	}
	if _, left := final[backupmeta.MarkerPartialAt]; left {
		t.Error("the in-progress time must be gone once the copy is finished")
	}
	if pts, _ := ListRestorePoints(local, Volume{Name: "lib"}, remoteTarget()); len(pts) != 1 {
		t.Errorf("now it is listed: %v", pts)
	}
}

func TestSweepRemovesOnlyAbandonedCopiesOfThisVolumeFromThisServer(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	old := sweepNow.Add(-8 * 24 * time.Hour)
	young := sweepNow.Add(-2 * time.Hour)

	remote.add("default", "lib-bk-abandoned", partialCfg("lib", "tron", old))
	remote.add("default", "lib-bk-abandoned-legacy", partialCfg("lib", "", old)) // no server recorded: treated as ours

	keeps := map[string]map[string]string{
		"lib-bk-young":        partialCfg("lib", "tron", young), // might still be running
		"lib-bk-other-server": partialCfg("lib", "other-host", old),
		"other-bk-abandoned":  partialCfg("other", "tron", old), // another volume's
		"lib-bk-unmarked":     {},                               // looks like ours, has no mark: never touched
		"lib-bk-no-time":      {backupmeta.MarkerPartialOf: backupmeta.CopyOf("", "default", "lib"), backupmeta.MarkerCopyServer: "tron"},
		"lib-bk-bad-time":     {backupmeta.MarkerPartialOf: backupmeta.CopyOf("", "default", "lib"), backupmeta.MarkerPartialAt: "last tuesday", backupmeta.MarkerCopyServer: "tron"},
	}
	// a RESTORE POINT that somehow still carries an in-progress mark: a finished copy is never swept
	both := partialCfg("lib", "tron", old)
	both[backupmeta.MarkerCopyOf] = backupmeta.CopyOf("", "default", "lib")
	both[backupmeta.MarkerCopyAt] = old.Format(time.RFC3339)
	keeps["lib-bk-finished-but-marked"] = both
	for name, cfg := range keeps {
		remote.add("default", name, cfg)
	}

	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return sweepNow }})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSet(res.Swept, []string{"lib-bk-abandoned", "lib-bk-abandoned-legacy"}) {
		t.Errorf("swept %v, want exactly the two abandoned copies of this volume", res.Swept)
	}
	for name := range keeps {
		if remote.vols["default/"+name] == nil {
			t.Errorf("%s must NOT be swept", name)
		}
	}
	if remote.vols["default/"+res.Volume] == nil {
		t.Error("the copy this run just made must survive")
	}
}

func TestSweepIsPlannedByADryRunAndDoesNothing(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.add("default", "lib-bk-abandoned", partialCfg("lib", "tron", sweepNow.Add(-8*24*time.Hour)))
	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{DryRun: true, Now: func() time.Time { return sweepNow }})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Planned, "\n"), "remove abandoned partial copy vps:default/lib-bk-abandoned") {
		t.Errorf("the dry run must say it would sweep it: %v", res.Planned)
	}
	if remote.vols["default/lib-bk-abandoned"] == nil || len(remote.deletedVols) != 0 {
		t.Error("a dry run must not delete anything")
	}
}

// A sweep that cannot delete is like a prune that cannot: the copy happened, so it is not a failed copy.
func TestASweepFailureIsNotAFailedCopy(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.add("default", "lib-bk-abandoned", partialCfg("lib", "tron", sweepNow.Add(-8*24*time.Hour)))
	remote.failDelete = true
	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return sweepNow }})
	if err == nil || !errors.Is(err, errPrune) || !strings.Contains(err.Error(), "the copy succeeded") || !strings.Contains(err.Error(), "abandoned partial copy") {
		t.Fatalf("the error must say the copy succeeded and what failed: %v", err)
	}
	cfg := local.vols["default/lib"].Config
	if cfg[backupmeta.CopyStampAt("vps")] == "" {
		t.Error("the copy succeeded, so it is stamped")
	}
	if _, has := cfg[backupmeta.CopyFailAt("vps")]; has {
		t.Error("a sweep failure must not put the copy into backoff")
	}
}

func TestSweepNeverRunsAgainstTheOtherServersCopyInFlight(t *testing.T) {
	// another server started a copy of a same-named volume a moment ago: it is young, and it is theirs, so doubly safe
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.add("default", "lib-bk-inflight", partialCfg("lib", "other-host", sweepNow.Add(-time.Minute)))
	res, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return sweepNow }})
	if err != nil || len(res.Swept) != 0 || remote.vols["default/lib-bk-inflight"] == nil {
		t.Errorf("%v %v", err, res.Swept)
	}
}

func TestAGraceOfSevenDays(t *testing.T) {
	if PartialGrace != 7*24*time.Hour {
		t.Errorf("PartialGrace = %s: a slow copy of a big volume must not be mistaken for an abandoned one", PartialGrace)
	}
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.add("default", "lib-bk-just-inside", partialCfg("lib", "tron", sweepNow.Add(-PartialGrace+time.Minute)))
	remote.add("default", "lib-bk-just-outside", partialCfg("lib", "tron", sweepNow.Add(-PartialGrace-time.Minute)))
	res, _ := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: func() time.Time { return sweepNow }})
	if len(res.Swept) != 1 || res.Swept[0] != "lib-bk-just-outside" {
		t.Errorf("only the one older than the grace: %v", res.Swept)
	}
}
