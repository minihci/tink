package volbackup

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
)

// These pin the branches of copyTo that the other tests do not reach, as it behaves now, so that taking it apart cannot change what is
// said, what is left on the target or the source, or which failures count as a failed copy.

var (
	wantRestorePoint = "lib-bk-" + stamped(remoteNow)
	wantSnapshot     = "tink-copy-" + stamped(remoteNow)
)

// poolCopy is the plainest copy: a volume to a second pool on the same server, at a fixed time.
func poolCopy() (*fakeIncus, Volume, Target, CopyOptions) {
	local := newFake("tron", "default", "nas")
	local.add("default", "lib", nil)
	return local, Volume{Name: "lib"}, Target{Name: "nas", Pool: "nas"}, CopyOptions{Now: func() time.Time { return remoteNow }}
}

func TestACopyReportsEachStepItTakes(t *testing.T) {
	local, v, tg, opts := poolCopy()
	var out bytes.Buffer
	opts.Progress, opts.Retain = &out, "30d"
	if _, err := copyTo(local, v, tg, opts); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"snapshotting default/lib as " + wantSnapshot,
		"copying it to nas/" + wantRestorePoint,
		"pruning restore points older than 30d",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("progress must say %q:\n%s", want, out.String())
		}
	}
}

func TestACopyOfAVolumeThatCannotBeReadSaysSo(t *testing.T) {
	local := newFake("tron", "default", "nas")
	_, _, tg, opts := poolCopy()
	_, err := copyTo(local, Volume{Name: "lib"}, tg, opts)
	if err == nil || !strings.Contains(err.Error(), "volume default/lib:") {
		t.Errorf("%v", err)
	}
}

func TestAServerThatCannotNameItselfIsRefusedBeforeAnythingIsTouched(t *testing.T) {
	local, v, tg, opts := poolCopy()
	local.failServer = errors.New("boom")
	_, err := copyTo(local, v, tg, opts)
	if err == nil || !strings.Contains(err.Error(), "reading the server's name") {
		t.Fatalf("%v", err)
	}
	if len(local.snaps["default/lib"]) != 0 || len(local.names("nas")) != 0 {
		t.Error("nothing may have been snapshotted or copied")
	}
}

func TestARestorePointNameThatAlreadyExistsIsRefused(t *testing.T) {
	local, v, tg, opts := poolCopy()
	local.add("nas", wantRestorePoint, nil)
	_, err := copyTo(local, v, tg, opts)
	if err == nil || !strings.Contains(err.Error(), "nas/"+wantRestorePoint+" already exists (a second run in the same second?)") {
		t.Fatalf("%v", err)
	}
	if len(local.snaps["default/lib"]) != 0 {
		t.Error("no snapshot may be taken for a copy that cannot be made")
	}
}

func TestADryRunSaysWhatItWouldPruneAndSweepAndChangesNothing(t *testing.T) {
	local, v, tg, opts := poolCopy()
	of := backupmeta.CopyOf("", "default", "lib")
	point := func(at string) map[string]string {
		return map[string]string{backupmeta.MarkerCopyOf: of, backupmeta.MarkerCopyAt: at, backupmeta.MarkerCopyTarget: "nas", backupmeta.MarkerCopyServer: "tron"}
	}
	local.add("nas", "lib-bk-20260801-040000", point("2026-08-01T04:00:00Z")) // expired under 30d, and not the newest
	local.add("nas", "lib-bk-20261006-040000", point("2026-10-06T04:00:00Z")) // the newest: never pruned
	local.add("nas", "lib-bk-20260901-040000", partialCfg("lib", "tron", time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)))
	before := strings.Join(local.names("nas"), ",")
	opts.DryRun, opts.Retain = true, "30d"
	res, err := copyTo(local, v, tg, opts)
	if err != nil {
		t.Fatal(err)
	}
	planned := strings.Join(res.Planned, "\n")
	for _, want := range []string{
		"snapshot default/lib@" + wantSnapshot,
		"copy it to nas/" + wantRestorePoint,
		"prune restore point nas/lib-bk-20260801-040000 (older than 30d)",
		"remove abandoned partial copy nas/lib-bk-20260901-040000 (started more than 7 days ago and never finished)",
	} {
		if !strings.Contains(planned, want) {
			t.Errorf("the plan must say %q:\n%s", want, planned)
		}
	}
	if strings.Contains(planned, "lib-bk-20261006-040000") {
		t.Errorf("the newest restore point is never pruned:\n%s", planned)
	}
	if after := strings.Join(local.names("nas"), ","); after != before || len(local.deletedVols) != 0 || len(local.snaps["default/lib"]) != 0 || len(local.modesSeen) != 0 {
		t.Errorf("a dry run changes nothing: volumes %q -> %q, deleted %v, snapshots %v", before, after, local.deletedVols, local.snaps)
	}
}

func TestADryRunThatCannotListTheTargetFailsWhetherItPrunesOrNot(t *testing.T) {
	for name, retain := range map[string]string{"with a retention": "30d", "without one": ""} {
		t.Run(name, func(t *testing.T) {
			local, v, tg, opts := poolCopy()
			local.failList = map[string]error{"nas": errors.New("pool offline")}
			// only the first listing fails: with a retention that is the prune's, and a sweep that then succeeds must not hide it
			local.failListOnce = true
			opts.DryRun, opts.Retain = true, retain
			res, err := copyTo(local, v, tg, opts)
			if err == nil || !strings.Contains(err.Error(), "pool offline") || !strings.Contains(err.Error(), `listing volumes in pool "nas"`) {
				t.Fatalf("err = %v", err)
			}
			if len(res.Planned) == 0 {
				t.Error("what was planned before the failure is still returned")
			}
		})
	}
}

func TestASnapshotThatCannotBeTakenIsRefusedAndNothingIsCopied(t *testing.T) {
	for name, set := range map[string]func(*fakeIncus){
		"it fails at once":         func(f *fakeIncus) { f.failSnapCreate = errors.New("no space") },
		"it fails when waited for": func(f *fakeIncus) { f.failSnapWait = errors.New("no space") },
	} {
		t.Run(name, func(t *testing.T) {
			local, v, tg, opts := poolCopy()
			set(local)
			_, err := copyTo(local, v, tg, opts)
			if err == nil || !strings.Contains(err.Error(), "snapshotting default/lib: no space") {
				t.Fatalf("%v", err)
			}
			if len(local.names("nas")) != 0 || len(local.modesSeen) != 0 {
				t.Errorf("nothing may be copied: %v", local.names("nas"))
			}
		})
	}
}

func TestATemporarySnapshotThatCannotBeRemovedIsReportedAfterAGoodCopy(t *testing.T) {
	local, v, tg, opts := poolCopy()
	local.failSnapDelete = errors.New("busy")
	res, err := copyTo(local, v, tg, opts)
	want := "could not remove the temporary snapshot default/lib@" + wantSnapshot + " (it expires on its own in 24h): busy"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if res.Volume != wantRestorePoint || local.vols["nas/"+wantRestorePoint].Config[backupmeta.MarkerCopyOf] == "" {
		t.Errorf("the restore point was made and marked: %q %v", res.Volume, local.vols["nas/"+wantRestorePoint])
	}
	if local.vols["default/lib"].Config[backupmeta.CopyStampAt("nas")] == "" {
		t.Error("the source was stamped: the copy happened")
	}
}

func TestATemporarySnapshotThatCannotBeRemovedIsAddedToTheCopyFailure(t *testing.T) {
	local, v, tg, opts := poolCopy()
	local.failCopy = errors.New("stream cut")
	local.failSnapDelete = errors.New("busy")
	_, err := copyTo(local, v, tg, opts)
	if err == nil || !strings.Contains(err.Error(), "stream cut") || !strings.Contains(err.Error(), "(and could not remove the temporary snapshot default/lib@"+wantSnapshot) {
		t.Fatalf("both problems must be told: %v", err)
	}
	if _, left := local.vols["nas/"+wantRestorePoint]; left {
		t.Error("the partial volume of the cut copy is still removed")
	}
}

func TestARestorePointThatCannotBeMarkedIsAnErrorAndNeverARestorePoint(t *testing.T) {
	for name, set := range map[string]func(*fakeIncus){
		"it cannot be read": func(f *fakeIncus) {
			f.getFails = map[string]getFail{"nas/" + wantRestorePoint: {err: errors.New("denied"), after: 1}}
		},
		"it cannot be updated": func(f *fakeIncus) {
			f.failUpdate = map[string]error{"nas/" + wantRestorePoint: errors.New("conflict")}
		},
	} {
		t.Run(name, func(t *testing.T) {
			local, v, tg, opts := poolCopy()
			set(local)
			_, err := copyTo(local, v, tg, opts)
			if err == nil || !(strings.Contains(err.Error(), "reading the new restore point nas/"+wantRestorePoint) || strings.Contains(err.Error(), "marking the new restore point nas/"+wantRestorePoint)) {
				t.Fatalf("%v", err)
			}
			if errors.Is(err, errPrune) {
				t.Error("this is a failed copy, not a prune failure")
			}
			if pts, _ := ListRestorePoints(local, v, tg); len(pts) != 0 {
				t.Errorf("a volume that was never marked is not a restore point: %v", pts)
			}
			if len(local.snaps["default/lib"]) != 0 {
				t.Errorf("the temporary snapshot is removed on this path too: %v", local.snaps["default/lib"])
			}
		})
	}
}

func TestASourceThatCannotBeStampedIsAnErrorAfterAGoodCopy(t *testing.T) {
	for name, tc := range map[string]struct {
		set  func(*fakeIncus)
		want string
	}{
		"it cannot be read": {func(f *fakeIncus) {
			f.getFails = map[string]getFail{"default/lib": {err: errors.New("denied"), after: 1}}
		}, "reading default/lib to record the copy"},
		"it cannot be updated": {func(f *fakeIncus) { f.failUpdate = map[string]error{"default/lib": errors.New("conflict")} }, "recording the copy on default/lib"},
	} {
		t.Run(name, func(t *testing.T) {
			local, v, tg, opts := poolCopy()
			tc.set(local)
			res, err := copyTo(local, v, tg, opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, errPrune) {
				t.Fatalf("err = %v, want %q (and not a prune failure)", err, tc.want)
			}
			if local.vols["nas/"+res.Volume].Config[backupmeta.MarkerCopyOf] == "" {
				t.Error("the restore point itself was made and marked before the stamp failed")
			}
		})
	}
}

// A finished restore point says what it is a copy of, when, to where, from which snapshot and by which server, and carries nothing of the
// in-progress mark or of the source's own policy (a scheduler that lists volumes would copy the copy).
func TestAFinishedRestorePointCarriesExactlyItsMarkers(t *testing.T) {
	local, v, tg, opts := poolCopy()
	local.vols["default/lib"].Config[backupmeta.PolicyKey] = `{"proto":1}`
	res, err := copyTo(local, v, tg, opts)
	if err != nil {
		t.Fatal(err)
	}
	got := local.vols["nas/"+res.Volume].Config
	want := map[string]string{
		backupmeta.MarkerCopyOf:     backupmeta.CopyOf("", "default", "lib"),
		backupmeta.MarkerCopyAt:     remoteNow.UTC().Format(time.RFC3339),
		backupmeta.MarkerCopyTarget: "nas",
		backupmeta.MarkerCopySnap:   wantSnapshot,
		backupmeta.MarkerCopyServer: "tron",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
	for _, gone := range []string{backupmeta.MarkerPartialOf, backupmeta.MarkerPartialAt, backupmeta.PolicyKey} {
		if _, still := got[gone]; still {
			t.Errorf("a finished restore point must not carry %s: %v", gone, got)
		}
	}
}

// The copy is asked to carry the source's content type: a block volume copied as a filesystem would be a backup that cannot be restored.
func TestACopyCarriesTheSourcesContentType(t *testing.T) {
	local, v, tg, opts := poolCopy()
	local.vols["default/lib"].ContentType = "block"
	if _, err := copyTo(local, v, tg, opts); err != nil {
		t.Fatal(err)
	}
	if len(local.contentTypes) != 1 || local.contentTypes[0] != "block" {
		t.Errorf("content types asked of the copy: %v, want [block]", local.contentTypes)
	}
}
