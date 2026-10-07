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

func TestSyncEnqueueJobsAndCancel(t *testing.T) {
	root := t.TempDir()
	stacks, jobsDir := filepath.Join(root, "stacks"), filepath.Join(root, "jobs")
	stack := filepath.Join(root, "tink.yaml")
	if err := os.WriteFile(stack, []byte(cliStack), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "daemon", "sync", "home", stack, "--stacks", stacks)
	if err != nil || !strings.Contains(out, "stack home synced: 1 file(s) (tink.yaml)") {
		t.Fatalf("sync: %v\n%s", err, out)
	}
	// a stack that does not load never replaces the good one
	bad := filepath.Join(root, "bad.yaml")
	os.WriteFile(bad, []byte("kind: storage-volume\nname: x\nnonsense: 1\n"), 0o644)
	if _, err := runCLI(t, "daemon", "sync", "home", bad, "--stacks", stacks); err == nil {
		t.Error("a stack that does not parse must not be synced")
	}

	out, err = runCLI(t, "daemon", "enqueue", "--stack", "home", "--due", "--jobs", jobsDir)
	if err != nil || !strings.Contains(out, "queued job ") {
		t.Fatalf("enqueue: %v\n%s", err, out)
	}
	id := strings.Fields(strings.Split(out, "\n")[0])[2]
	out, err = runCLI(t, "daemon", "enqueue", "-f", stack, "lib", "--jobs", jobsDir)
	if err != nil || !strings.Contains(out, "queued job ") {
		t.Fatalf("enqueue with a bundle: %v\n%s", err, out)
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

func TestEnqueueNeedsExactlyOneStackSource(t *testing.T) {
	jobsDir := t.TempDir()
	if _, err := runCLI(t, "daemon", "enqueue", "--jobs", jobsDir); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("neither: %v", err)
	}
	if _, err := runCLI(t, "daemon", "enqueue", "--stack", "x", "-f", "y.yaml", "--jobs", jobsDir); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("both: %v", err)
	}
	if _, err := runCLI(t, "daemon", "enqueue", "--stack", "x"); err == nil || !strings.Contains(err.Error(), "--jobs") {
		t.Errorf("no jobs dir: %v", err)
	}
}

func TestDaemonRunFlagsGoTogether(t *testing.T) {
	if _, err := runCLI(t, "daemon", "run", "--stacks", t.TempDir()); err == nil || !strings.Contains(err.Error(), "go together") {
		t.Errorf("--stacks alone: %v", err)
	}
	if _, err := runCLI(t, "daemon", "run", "--no-ingress"); err == nil || !strings.Contains(err.Error(), "leaves nothing to run") {
		t.Errorf("--no-ingress alone: %v", err)
	}
	if _, err := runCLI(t, "daemon", "run", "--stacks", t.TempDir(), "--jobs", t.TempDir(), "--timezone", "Not/AZone"); err == nil || !strings.Contains(err.Error(), "--timezone") {
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
