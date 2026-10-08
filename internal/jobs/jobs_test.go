package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

func TestValidID(t *testing.T) {
	for _, ok := range []string{NewID(t0), "a", "job-1", "20261007T100000Z-abc123", "A.b_c-1"} {
		if !ValidID(ok) {
			t.Errorf("%q must be a valid id", ok)
		}
	}
	for _, bad := range []string{"", ".hidden", "-x", "a/b", "../x", "a b", strings.Repeat("x", 65), "READY"[:0], "a\x00b"} {
		if ValidID(bad) {
			t.Errorf("%q must not be a valid id", bad)
		}
	}
	a, b := NewID(t0), NewID(t0)
	if a == b {
		t.Error("two ids made in the same second must differ")
	}
}

func TestEnqueueWritesREADYLastAndTheJobIsQueued(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	id, err := s.Enqueue(Request{Kind: "backup-run", Origin: OriginTrigger}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"request.json", "READY"} {
		if _, err := os.Stat(filepath.Join(s.Dir, id, filepath.FromSlash(f))); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	st, err := s.Status(id)
	if err != nil || st.State != Queued || st.Kind != "backup-run" || st.Origin != OriginTrigger {
		t.Errorf("a job with no status yet is queued: %+v %v", st, err)
	}
	var req Request
	b, _ := os.ReadFile(filepath.Join(s.Dir, id, "request.json"))
	if err := json.Unmarshal(b, &req); err != nil || req.Proto != Proto || req.Created.IsZero() || req.Kind != "backup-run" {
		t.Errorf("request.json: %v %+v", err, req)
	}
}

// The property READY exists for: the Incus file API writes a file in place, so a reader can see a request half-written. READY is the writer
// saying it is finished, which only holds if it is written after the whole request. Checked at the one moment it matters, not by timing.
func TestREADYIsWrittenOnlyAfterTheWholeRequest(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	var readyThen, completeThen bool
	beforeREADY = func(dir string) {
		_, err := os.Stat(filepath.Join(dir, "READY"))
		readyThen = err == nil
		var r Request
		b, _ := os.ReadFile(filepath.Join(dir, "request.json"))
		completeThen = json.Unmarshal(b, &r) == nil && r.Kind == "k"
	}
	t.Cleanup(func() { beforeREADY = nil })
	if _, err := s.Enqueue(Request{Kind: "k", Origin: OriginTrigger}, t0); err != nil {
		t.Fatal(err)
	}
	if readyThen {
		t.Error("READY existed before the request was written: a reader could pick up a job with no request")
	}
	if !completeThen {
		t.Error("request.json was not complete when READY was about to be written")
	}
}

func TestEnqueueRefusesAJobWithoutAKindAndLeavesNothing(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if _, err := s.Enqueue(Request{}, t0); err == nil {
		t.Error("a job needs a kind")
	}
	if entries, _ := os.ReadDir(s.Dir); len(entries) != 0 {
		t.Errorf("a refused job must leave nothing behind: %v", entries)
	}
}

// Nothing is a job until READY exists: a half-delivered directory is invisible.
func TestADirectoryWithoutREADYIsNotAJob(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	dir := filepath.Join(s.Dir, "half")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{"kind":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("a job with no READY must not be listed: %v", list)
	}
	if _, err := s.Status("half"); err == nil {
		t.Error("a job with no READY has no status")
	}
	if err := s.Cancel("half"); err == nil {
		t.Error("a job with no READY cannot be cancelled: it is not a job")
	}
	os.WriteFile(filepath.Join(dir, "READY"), nil, 0o600)
	if list, _ := s.List(); len(list) != 1 {
		t.Errorf("with READY it is a job: %v", list)
	}
}

func TestListIsOldestFirstAndIgnoresStrangers(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	b, _ := s.Enqueue(Request{Kind: "k", Created: t0.Add(2 * time.Hour)}, t0)
	a, _ := s.Enqueue(Request{Kind: "k", Created: t0.Add(time.Hour)}, t0)
	os.MkdirAll(filepath.Join(s.Dir, ".hidden"), 0o700)
	os.WriteFile(filepath.Join(s.Dir, "a-file"), nil, 0o600)
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].ID != a || list[1].ID != b {
		t.Errorf("%v %v", list, err)
	}
	if got, _ := (Store{Dir: filepath.Join(s.Dir, "nope")}).List(); len(got) != 0 {
		t.Error("a missing directory has no jobs")
	}
}

func TestPendingSeesQueuedAndRunningJobsOfTheKind(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	if p, _ := s.Pending("backup-run"); p {
		t.Error("nothing is pending yet")
	}
	id, _ := s.Enqueue(Request{Kind: "backup-run", Origin: OriginTrigger}, t0)
	if p, _ := s.Pending("backup-run"); !p {
		t.Error("a queued job is pending, whoever queued it")
	}
	if p, _ := s.Pending("other-kind"); p {
		t.Error("another kind is not pending")
	}
	if err := s.writeStatus(Status{ID: id, Kind: "backup-run", State: Running}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Pending("backup-run"); !p {
		t.Error("a running job is pending")
	}
	if err := s.writeStatus(Status{ID: id, Kind: "backup-run", State: Succeeded}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Pending("backup-run"); p {
		t.Error("a finished job is not pending")
	}
}
