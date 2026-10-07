package volbackup

import (
	"errors"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/resolve"
)

func at(h, m int) func() time.Time {
	return func() time.Time { return time.Date(2026, 10, 7, h, m, 0, 0, time.UTC) }
}

// A copy that fails leaves a mark on the source volume (when and how many in a row, never why), and a later
// success clears it.
func TestFailedCopiesAreCountedAndASuccessClearsThem(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	cfg := func() map[string]string { return local.vols["default/lib"].Config }

	for i, want := range []string{"1", "2", "3"} {
		remote.failCopy = errors.New("tunnel dropped, token=hunter2")
		_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: at(10, i*10)})
		if err == nil {
			t.Fatalf("attempt %d: the copy was meant to fail", i+1)
		}
		if got := cfg()[resolve.CopyFailCount("vps")]; got != want {
			t.Errorf("after %d failures the count is %q, want %q", i+1, got, want)
		}
	}
	if got := cfg()[resolve.CopyFailAt("vps")]; got != "2026-10-07T10:20:00Z" {
		t.Errorf("the failure time is the LAST attempt's: %q", got)
	}
	// nothing about WHY is stored on the volume: the error text can contain credentials
	for k, v := range cfg() {
		if strings.Contains(k, "hunter2") || strings.Contains(v, "hunter2") || strings.Contains(v, "tunnel") {
			t.Errorf("the failure reason leaked into the volume's config: %s=%s", k, v)
		}
	}
	if cfg()[resolve.CopyStampAt("vps")] != "" {
		t.Error("a failure must never stamp the copy as done")
	}

	// the next attempt succeeds: the run of failures ends
	if _, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: at(11, 0)}); err != nil {
		t.Fatal(err)
	}
	if _, has := cfg()[resolve.CopyFailAt("vps")]; has {
		t.Error("a success must remove the failure time")
	}
	if _, has := cfg()[resolve.CopyFailCount("vps")]; has {
		t.Error("a success must remove the failure count")
	}
	if cfg()[resolve.CopyStampAt("vps")] == "" {
		t.Error("the success must stamp the copy")
	}
	// and a failure after that starts again from one
	remote.failCopy = errors.New("again")
	_, _ = Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: at(12, 0)})
	if got := cfg()[resolve.CopyFailCount("vps")]; got != "1" {
		t.Errorf("a new run of failures starts at 1, got %q", got)
	}
}

func TestCountsAreTrackedPerTarget(t *testing.T) {
	local := newFake("tron", "default", "nas")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	remote.failCopy = errors.New("down")
	_, _ = Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: at(10, 0)})
	if _, err := Copy(local, Volume{Name: "lib"}, Target{Name: "nas", Pool: "nas"}, CopyOptions{Now: at(10, 5)}); err != nil {
		t.Fatal(err)
	}
	cfg := local.vols["default/lib"].Config
	if cfg[resolve.CopyFailCount("vps")] != "1" || cfg[resolve.CopyFailAt("nas")] != "" {
		t.Errorf("one target's failure and another's success must not touch each other: %v", cfg)
	}
}

func TestAnUnreachableRemoteCountsAsAFailure(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	useRemote(t, "elsewhere", newFake("x")) // "vps" is not configured: the target cannot be reached at all
	if _, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Now: at(10, 0)}); err == nil {
		t.Fatal("expected an error")
	}
	if got := local.vols["default/lib"].Config[resolve.CopyFailCount("vps")]; got != "1" {
		t.Errorf("failing before the first snapshot is still a failed copy, count = %q", got)
	}
}

func TestADryRunNeverRecordsAFailure(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	useRemote(t, "elsewhere", newFake("x"))
	if _, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{DryRun: true, Now: at(10, 0)}); err == nil {
		t.Fatal("expected an error")
	}
	if len(local.vols["default/lib"].Config) != 0 {
		t.Errorf("a dry run must change nothing: %v", local.vols["default/lib"].Config)
	}
}

// The copy worked and the source is stamped; only removing OLD restore points failed. That is a different problem
// from a failed copy, and must not put the copy into backoff.
func TestAPruneFailureIsNotAFailedCopy(t *testing.T) {
	local := newFake("tron", "default")
	local.add("default", "lib", nil)
	remote := newFake("vps", "default")
	useRemote(t, "vps", remote)
	old := time.Date(2026, 1, 1, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)
	remote.add("default", "lib-bk-20260101-040000", map[string]string{resolve.MarkerCopyOf: resolve.CopyOf("", "default", "lib"), resolve.MarkerCopyAt: old})
	remote.add("default", "lib-bk-20260102-040000", map[string]string{resolve.MarkerCopyOf: resolve.CopyOf("", "default", "lib"), resolve.MarkerCopyAt: time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC).Format(time.RFC3339)})
	remote.failDelete = true

	_, err := Copy(local, Volume{Name: "lib"}, remoteTarget(), CopyOptions{Retain: "1d", Now: at(10, 0)})
	if err == nil || !errors.Is(err, errPrune) || !strings.Contains(err.Error(), "the copy succeeded") {
		t.Fatalf("the error must say the copy succeeded and pruning failed: %v", err)
	}
	cfg := local.vols["default/lib"].Config
	if cfg[resolve.CopyStampAt("vps")] == "" {
		t.Error("the copy succeeded, so it must be stamped")
	}
	if _, has := cfg[resolve.CopyFailAt("vps")]; has {
		t.Error("a prune failure must not be recorded as a failed copy")
	}
}

func TestAFailureThatCannotBeRecordedSaysSo(t *testing.T) {
	local := newFake("tron", "default") // the source volume does not exist, so nothing can be stamped
	_, err := Copy(local, Volume{Name: "lib"}, Target{Name: "nas", Pool: "nas"}, CopyOptions{Now: at(10, 0)})
	if err == nil || !strings.Contains(err.Error(), "could not be recorded") {
		t.Errorf("both problems must be reported: %v", err)
	}
}

func TestRecordFailureOnlyCountsAFailureNewerThanTheLastSuccess(t *testing.T) {
	local := newFake("tron", "default")
	// a stale failure (older than the last success) must not inflate the count
	local.add("default", "lib", map[string]string{
		resolve.CopyStampAt("nas"):   "2026-10-07T09:00:00Z",
		resolve.CopyFailAt("nas"):    "2026-10-07T08:00:00Z",
		resolve.CopyFailCount("nas"): "9",
	})
	var srv incus.InstanceServer = local
	if err := recordFailure(srv, Volume{Name: "lib"}, "nas", time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if got := local.vols["default/lib"].Config[resolve.CopyFailCount("nas")]; got != "1" {
		t.Errorf("a stale failure must not count: got %q, want 1", got)
	}
}
