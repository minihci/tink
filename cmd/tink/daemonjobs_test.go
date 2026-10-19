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

	// a job works from the copy policies on the volumes: nothing but the request is sent
	out, err := runCLI(t, "daemon", "enqueue", "--due", "--jobs", jobsDir)
	if err != nil || !strings.Contains(out, "queued job ") {
		t.Fatalf("enqueue: %v\n%s", err, out)
	}
	id := strings.Fields(strings.Split(out, "\n")[0])[2]
	if entries, _ := os.ReadDir(filepath.Join(jobsDir, id)); len(entries) != 2 {
		t.Errorf("a job is its request.json and READY, nothing else: %v", entries)
	}
	out, err = runCLI(t, "daemon", "enqueue", "lib", "--jobs", jobsDir)
	if err != nil || !strings.Contains(out, "queued job ") {
		t.Fatalf("enqueue a named volume: %v\n%s", err, out)
	}
	// sending a stack with a job is gone: a stack that exists is still refused, as an unknown flag
	if _, err := runCLI(t, "daemon", "enqueue", "-f", stack, "--jobs", jobsDir); err == nil || !strings.Contains(err.Error(), "unknown shorthand flag") {
		t.Errorf("-f must no longer exist: %v", err)
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
	if _, err := runCLI(t, "daemon", "run"); err == nil || !strings.Contains(err.Error(), "needs --jobs") {
		t.Errorf("with the ingress reconcile gone, --jobs is all it runs, so it is required: %v", err)
	}
	for _, gone := range []string{"--no-ingress", "--ingress-via-api", "--routes-dir", "--ingress-instance", "--interval"} {
		if _, err := runCLI(t, "daemon", "run", "--jobs", t.TempDir(), gone); err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Errorf("%s no longer exists: %v", gone, err)
		}
	}
	if _, err := runCLI(t, "ingress", "status"); err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("tink ingress no longer exists: %v", err)
	}
	if out, err := runCLI(t, "daemon", "--help"); err != nil || strings.Contains(out, "  install ") {
		t.Errorf("`daemon install` no longer exists, but the help lists it: %v\n%s", err, out)
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

func TestDaemonRunUnderARemoteIsNotRefused(t *testing.T) {
	// A bad --timezone stops the command before it starts anything, which is how this knows nothing refused it first.
	args := []string{"--remote", "helper-host", "daemon", "run", "--jobs", t.TempDir(), "--timezone", "Not/AZone"}
	if err := runRoot(t, args...); err == nil || !strings.Contains(err.Error(), "--timezone") {
		t.Errorf("the daemon is a client of whatever server it is pointed at, so a remote is fine: %v", err)
	}
}
