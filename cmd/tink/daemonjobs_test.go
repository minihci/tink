package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errb bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errb)
	err := root.Execute()
	return out.String() + errb.String(), err
}

const cliStack = "kind: backup-target\nname: nas\nlocation: other-host\nengine: incus\npool: nas\n---\nkind: storage-volume\nname: lib\nbackup:\n  copies:\n    - {target: nas, schedule: \"@daily\", retain: 30d}\n"

func TestEnqueueJobsAndCancel(t *testing.T) {
	root := t.TempDir()
	jobsDir := filepath.Join(root, "jobs")
	stack := filepath.Join(root, "tink.yaml")
	if err := os.WriteFile(stack, []byte(cliStack), 0o644); err != nil {
		t.Fatal(err)
	}

	// by default a job works from the copy policies on the volumes: nothing is sent with it
	out, err := runCLI(t, "daemon", "enqueue", "--due", "--jobs", jobsDir)
	if err != nil || !strings.Contains(out, "queued job ") {
		t.Fatalf("enqueue: %v\n%s", err, out)
	}
	id := strings.Fields(strings.Split(out, "\n")[0])[2]
	if _, err := os.Stat(filepath.Join(jobsDir, id, "bundle")); err == nil {
		t.Error("a job that uses the volumes' policies carries no bundle")
	}
	out, err = runCLI(t, "daemon", "enqueue", "-f", stack, "lib", "--jobs", jobsDir)
	if err != nil || !strings.Contains(out, "queued job ") {
		t.Fatalf("enqueue with a bundle: %v\n%s", err, out)
	}
	// a stack that does not load is never bundled
	bad := filepath.Join(root, "bad.yaml")
	os.WriteFile(bad, []byte("kind: storage-volume\nname: x\nnonsense: 1\n"), 0o644)
	if _, err := runCLI(t, "daemon", "enqueue", "-f", bad, "--jobs", jobsDir); err == nil {
		t.Error("a stack that does not parse must not be sent")
	}

	out, err = runCLI(t, "daemon", "jobs", "--jobs", jobsDir)
	if err != nil || strings.Count(out, "queued") != 2 || !strings.Contains(out, "backup-run") || !strings.Contains(out, "no heartbeat") {
		t.Errorf("jobs: %v\n%s", err, out)
	}
	out, err = runCLI(t, "daemon", "jobs", "--jobs", jobsDir, "--log", id)
	if err != nil || !strings.Contains(out, `"state": "queued"`) || !strings.Contains(out, "--- log") {
		t.Errorf("jobs --log: %v\n%s", err, out)
	}
	if out, err = runCLI(t, "daemon", "cancel", id, "--jobs", jobsDir); err != nil || !strings.Contains(out, "asked job") {
		t.Errorf("cancel: %v\n%s", err, out)
	}
	if _, err = runCLI(t, "daemon", "cancel", "no-such-job", "--jobs", jobsDir); err == nil {
		t.Error("cancelling a job that does not exist is an error")
	}
}

func TestEnqueueNeedsAJobsDirectory(t *testing.T) {
	if _, err := runCLI(t, "daemon", "enqueue"); err == nil || !strings.Contains(err.Error(), "--jobs") {
		t.Errorf("no jobs dir: %v", err)
	}
}

func TestTheStackStoreIsGone(t *testing.T) {
	// the volumes carry the policy now, so there is nothing to sync a stack to
	if out, err := runCLI(t, "daemon", "--help"); err != nil || strings.Contains(out, "  sync ") {
		t.Errorf("`daemon sync` no longer exists, but the help lists it: %v\n%s", err, out)
	}
	if _, err := runCLI(t, "daemon", "run", "--stacks", t.TempDir()); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("--stacks no longer exists: %v", err)
	}
	if _, err := runCLI(t, "daemon", "enqueue", "--stack", "x", "--jobs", t.TempDir()); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("--stack no longer exists: %v", err)
	}
}

func TestDaemonRunFlags(t *testing.T) {
	if _, err := runCLI(t, "daemon", "run", "--no-ingress"); err == nil || !strings.Contains(err.Error(), "leaves nothing to run") {
		t.Errorf("--no-ingress alone: %v", err)
	}
	if _, err := runCLI(t, "daemon", "run", "--jobs", t.TempDir(), "--timezone", "Not/AZone"); err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Errorf("a bad time zone: %v", err)
	}
}

func TestAJobWithAnUnreadableRequestHasNoNonsenseAge(t *testing.T) {
	if got := jobAge(time.Time{}); got != "-" {
		t.Errorf("age of an unknown time = %q, want %q", got, "-")
	}
	if got := jobAge(time.Now().Add(-90 * time.Second)); !strings.HasPrefix(got, "1m") {
		t.Errorf("age = %q", got)
	}
}

func TestDaemonRunUnderARemoteRefusesOnlyTheIngressPathItCannotReach(t *testing.T) {
	// With a remote and nothing said about ingress, it would read a path inside the host's storage pool: refused, saying what to give.
	err := runRoot(t, "--remote", "helper-host", "daemon", "run", "--jobs", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), `remote "helper-host"`) || !strings.Contains(err.Error(), "--no-ingress") || !strings.Contains(err.Error(), "--routes-dir") {
		t.Errorf("err = %v", err)
	}
	// --no-ingress or an explicit --routes-dir gets past the refusal. A bad --timezone then stops the command before it
	// starts anything, which is how this test knows the PreRunE let it through.
	for _, extra := range [][]string{{"--no-ingress"}, {"--routes-dir", t.TempDir()}} {
		args := append([]string{"--remote", "helper-host", "daemon", "run", "--jobs", t.TempDir(), "--timezone", "Not/AZone"}, extra...)
		if err := runRoot(t, args...); err == nil || !strings.Contains(err.Error(), "--timezone") {
			t.Errorf("%v must pass the remote check and fail on the time zone, got: %v", extra, err)
		}
	}
	// no remote: unchanged
	if err := runRoot(t, "daemon", "run", "--jobs", t.TempDir(), "--timezone", "Not/AZone"); err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Errorf("without a remote nothing is refused: %v", err)
	}
}

func TestDaemonRunUnderARemoteAcceptsTheIngressViaTheFileAPI(t *testing.T) {
	// a bad --timezone stops the command before it starts anything, which is how this knows the refusal let it through
	args := []string{"--remote", "helper-host", "daemon", "run", "--jobs", t.TempDir(), "--timezone", "Not/AZone", "--ingress-via-api"}
	if err := runRoot(t, args...); err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Errorf("--ingress-via-api needs nothing from this machine, so a remote is fine: %v", err)
	}
	err := runRoot(t, "--remote", "helper-host", "daemon", "run", "--jobs", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "--ingress-via-api") {
		t.Errorf("and the refusal now names it as the way out: %v", err)
	}
}

func TestIngressCommandsWorkUnderARemoteOnlyThroughTheFileAPI(t *testing.T) {
	for _, sub := range []string{"reconcile", "status"} {
		err := runRoot(t, "--remote", "helper-host", "ingress", sub)
		if err == nil || !strings.Contains(err.Error(), "works on the host it runs on") {
			t.Errorf("ingress %s without --via-api is still host-local: %v", sub, err)
		}
		// with --via-api it is not refused for being under a remote: it goes on to look for the remote itself
		err = runRoot(t, "--remote", "helper-host", "ingress", sub, "--via-api")
		if err == nil || strings.Contains(err.Error(), "works on the host it runs on") {
			t.Errorf("ingress %s --via-api must get past the refusal: %v", sub, err)
		}
	}
}
