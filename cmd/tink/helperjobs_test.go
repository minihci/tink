package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/jobs"
)

func TestHelperLogShowsStatusAndLog(t *testing.T) {
	store := jobs.Store{Dir: t.TempDir()}
	id, err := store.Enqueue(jobs.Request{Kind: "backup-run", Origin: jobs.OriginTrigger}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(store.Dir, id, "log"), []byte("copied lib to nas\n"), 0o600)
	var out, errOut bytes.Buffer
	if err := showHelperJob(context.Background(), &out, &errOut, store, id, false, jobs.FollowOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"state": "queued"`) || !strings.Contains(out.String(), "--- log\ncopied lib to nas") {
		t.Errorf("%s", out.String())
	}
	if err := showHelperJob(context.Background(), &out, &errOut, store, "20260101T000000Z-nope", false, jobs.FollowOptions{}); err == nil {
		t.Error("a job that is not there is an error")
	}
}

func TestFollowedJobsExitWithTheirOutcome(t *testing.T) {
	for _, tc := range []struct {
		state   jobs.State
		errText string
		wantErr string
	}{
		{jobs.Succeeded, "", ""},
		{jobs.Failed, "lib -> nas: boom", "failed: lib -> nas: boom"},
		{jobs.Cancelled, "", "was cancelled"},
	} {
		store := jobs.Store{Dir: t.TempDir()}
		id, _ := store.Enqueue(jobs.Request{Kind: "backup-run"}, time.Now())
		os.WriteFile(filepath.Join(store.Dir, id, "status.json"),
			[]byte(`{"proto":1,"id":"`+id+`","state":"`+string(tc.state)+`","error":"`+tc.errText+`"}`), 0o600)
		os.WriteFile(filepath.Join(store.Dir, id, "log"), []byte("line\n"), 0o600)
		var out bytes.Buffer
		err := showHelperJob(context.Background(), &out, &bytes.Buffer{}, store, id, true, jobs.FollowOptions{})
		if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
			t.Errorf("%s: %v", tc.state, err)
		}
		if out.String() != "line\n" {
			t.Errorf("%s: %q", tc.state, out.String())
		}
	}
}

func writeJob(t *testing.T, s jobs.Store, id, status, log string) {
	t.Helper()
	os.WriteFile(filepath.Join(s.Dir, id, "status.json"), []byte(status), 0o600)
	os.WriteFile(filepath.Join(s.Dir, id, "log"), []byte(log), 0o600)
}

func readFile(s jobs.Store, id, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.Dir, id, name))
}
