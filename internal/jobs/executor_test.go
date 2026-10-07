package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minihci/tink/internal/secrets"
)

func newExec(t *testing.T, h map[string]Handler) (*Executor, Store) {
	t.Helper()
	s := Store{Dir: t.TempDir()}
	return &Executor{Store: s, Handlers: h, Now: func() time.Time { return t0 }, CancelPoll: 5 * time.Millisecond}, s
}

func enqueue(t *testing.T, s Store, kind string, created time.Time) string {
	t.Helper()
	id, err := s.Enqueue(Request{Kind: kind, Origin: OriginSchedule, Created: created}, nil, created)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func status(t *testing.T, s Store, id string) Status {
	t.Helper()
	st, err := s.Status(id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestJobsRunOldestFirstAndRecordTheirSummaryAndLog(t *testing.T) {
	var order []string
	h := func(ctx context.Context, j Job, log io.Writer) (any, error) {
		order = append(order, j.ID)
		fmt.Fprintf(log, "working on %s\n", j.ID)
		return map[string]int{"copies": 2}, nil
	}
	e, s := newExec(t, map[string]Handler{"k": h})
	late := enqueue(t, s, "k", t0.Add(2*time.Hour))
	early := enqueue(t, s, "k", t0.Add(time.Hour))
	n, err := e.RunOnce(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("%d %v", n, err)
	}
	if len(order) != 2 || order[0] != early || order[1] != late {
		t.Errorf("oldest first: %v", order)
	}
	st := status(t, s, early)
	var sum map[string]int
	if err := json.Unmarshal(st.Summary, &sum); err != nil || sum["copies"] != 2 {
		t.Errorf("summary: %s %v", st.Summary, err)
	}
	if st.State != Succeeded || st.Started.IsZero() || st.Finished.IsZero() || st.Kind != "k" {
		t.Errorf("status: %+v", st)
	}
	if log, _ := s.Log(early); log != "working on "+early+"\n" {
		t.Errorf("log = %q", log)
	}
	// a job that has run is not run again
	if n, _ := e.RunOnce(context.Background()); n != 0 {
		t.Errorf("finished jobs must not run again: ran %d", n)
	}
}

func TestABadJobNeverStopsTheOthers(t *testing.T) {
	ran := 0
	good := func(ctx context.Context, j Job, log io.Writer) (any, error) { ran++; return nil, nil }
	boom := func(ctx context.Context, j Job, log io.Writer) (any, error) { panic("kaboom") }
	fails := func(ctx context.Context, j Job, log io.Writer) (any, error) { return nil, errors.New("target is full") }
	e, s := newExec(t, map[string]Handler{"good": good, "boom": boom, "fails": fails})

	malformed := enqueue(t, s, "good", t0.Add(1*time.Minute))
	os.WriteFile(filepath.Join(s.Dir, malformed, "request.json"), []byte("{ this is not json"), 0o600)
	tooNew := enqueue(t, s, "good", t0.Add(2*time.Minute))
	rb, _ := os.ReadFile(filepath.Join(s.Dir, tooNew, "request.json"))
	os.WriteFile(filepath.Join(s.Dir, tooNew, "request.json"), []byte(strings.Replace(string(rb), `"proto": 1`, `"proto": 99`, 1)), 0o600)
	unknown := enqueue(t, s, "no-such-kind", t0.Add(3*time.Minute))
	panics := enqueue(t, s, "boom", t0.Add(4*time.Minute))
	failing := enqueue(t, s, "fails", t0.Add(5*time.Minute))
	fine := enqueue(t, s, "good", t0.Add(6*time.Minute))

	if _, err := e.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{
		malformed: "unreadable request",
		tooNew:    "unsupported protocol 99",
		unknown:   `unknown job kind "no-such-kind"`,
		panics:    "the job panicked: kaboom",
		failing:   "target is full",
	} {
		st := status(t, s, id)
		if st.State != Failed || !strings.Contains(st.Error, want) || st.Finished.IsZero() {
			t.Errorf("%s: %+v, want a failure containing %q", id, st, want)
		}
	}
	if st := status(t, s, fine); st.State != Succeeded || ran != 1 {
		t.Errorf("the good job after all the bad ones must still run: %+v ran=%d", st, ran)
	}
}

func TestRecoverMarksInterruptedJobsFailedAndLeavesQueuedOnesToRun(t *testing.T) {
	ran := 0
	e, s := newExec(t, map[string]Handler{"k": func(ctx context.Context, j Job, log io.Writer) (any, error) { ran++; return nil, nil }})
	crashed := enqueue(t, s, "k", t0)
	if err := s.writeStatus(Status{ID: crashed, Kind: "k", State: Running, Started: t0}); err != nil {
		t.Fatal(err)
	}
	waiting := enqueue(t, s, "k", t0.Add(time.Minute))
	if err := e.Recover(); err != nil {
		t.Fatal(err)
	}
	if st := status(t, s, crashed); st.State != Failed || !strings.Contains(st.Error, "interrupted") || st.Finished.IsZero() {
		t.Errorf("a job left running by a dead process failed: %+v", st)
	}
	if st := status(t, s, waiting); st.State != Queued {
		t.Errorf("a queued job is untouched by recovery: %+v", st)
	}
	if n, _ := e.RunOnce(context.Background()); n != 1 || ran != 1 {
		t.Errorf("only the queued job runs: n=%d ran=%d (a failed-interrupted job is not re-run by the executor)", n, ran)
	}
}

func TestCancelWhileRunning(t *testing.T) {
	started := make(chan struct{})
	h := func(ctx context.Context, j Job, log io.Writer) (any, error) {
		close(started)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Second):
			return nil, errors.New("never cancelled")
		}
	}
	e, s := newExec(t, map[string]Handler{"k": h})
	id := enqueue(t, s, "k", t0)
	done := make(chan struct{})
	go func() { e.RunOnce(context.Background()); close(done) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the job never started")
	}
	if err := s.Cancel(id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling did not stop the job")
	}
	if st := status(t, s, id); st.State != Cancelled || st.Error != "" {
		t.Errorf("a cancelled job is cancelled, not failed: %+v", st)
	}
}

func TestCancelBeforeItStartsMeansItNeverRuns(t *testing.T) {
	ran := false
	e, s := newExec(t, map[string]Handler{"k": func(ctx context.Context, j Job, log io.Writer) (any, error) { ran = true; return nil, nil }})
	id := enqueue(t, s, "k", t0)
	if err := s.Cancel(id); err != nil {
		t.Fatal(err)
	}
	e.RunOnce(context.Background())
	if ran || status(t, s, id).State != Cancelled {
		t.Errorf("ran=%v status=%+v", ran, status(t, s, id))
	}
}

// A job that finished is succeeded even if someone asked it to stop a moment too late.
func TestACancelThatArrivesAfterSuccessDoesNotRewriteHistory(t *testing.T) {
	var s Store
	h := func(ctx context.Context, j Job, log io.Writer) (any, error) {
		// the cancel request lands while the handler is finishing, and the handler does not notice it
		os.WriteFile(filepath.Join(j.Dir, "cancel"), nil, 0o600)
		time.Sleep(50 * time.Millisecond) // long enough for the watcher to see it
		return nil, nil
	}
	e, st := newExec(t, map[string]Handler{"k": h})
	s = st
	id := enqueue(t, s, "k", t0)
	e.RunOnce(context.Background())
	if got := status(t, s, id); got.State != Succeeded {
		t.Errorf("the job finished, so it succeeded: %+v", got)
	}
}

func TestSecretsAreScrubbedFromTheLogAndTheErrorAndTheLogIsBounded(t *testing.T) {
	red := secrets.NewRedactor()
	red.Add("hunter2-the-api-key")
	h := func(ctx context.Context, j Job, log io.Writer) (any, error) {
		fmt.Fprintln(log, "connecting with hunter2-the-api-key")
		fmt.Fprint(log, strings.Repeat("x", 200))
		return nil, errors.New("failed to run truenas_incus_ctl --api-key hunter2-the-api-key")
	}
	e, s := newExec(t, map[string]Handler{"k": h})
	e.Redactor, e.LogLimit = red, 100
	id := enqueue(t, s, "k", t0)
	e.RunOnce(context.Background())
	st := status(t, s, id)
	if strings.Contains(st.Error, "hunter2") || !strings.Contains(st.Error, "--api-key") {
		t.Errorf("the error must be scrubbed, not dropped: %q", st.Error)
	}
	log, _ := s.Log(id)
	if strings.Contains(log, "hunter2") {
		t.Errorf("the log leaked a secret: %q", log)
	}
	if !strings.Contains(log, "[log truncated") || len(log) > 200 {
		t.Errorf("the log must be bounded and say so: %d bytes %q", len(log), log)
	}
}

func TestRetentionKeepsTheNewestAndTheRecent(t *testing.T) {
	e, s := newExec(t, map[string]Handler{"k": func(ctx context.Context, j Job, log io.Writer) (any, error) { return nil, nil }})
	e.KeepJobs, e.KeepFor = 3, 14*24*time.Hour
	now := t0
	e.Now = func() time.Time { return now }
	mk := func(age time.Duration, state State) string {
		id := enqueue(t, s, "k", now.Add(-age))
		if err := s.writeStatus(Status{ID: id, Kind: "k", State: state, Created: now.Add(-age), Finished: now.Add(-age)}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var recent, old []string
	for _, d := range []time.Duration{time.Hour, 2 * time.Hour, 3 * time.Hour, 24 * time.Hour, 5 * 24 * time.Hour} { // all within 14 days
		recent = append(recent, mk(d, Succeeded))
	}
	for _, d := range []time.Duration{20 * 24 * time.Hour, 30 * 24 * time.Hour, 40 * 24 * time.Hour} {
		old = append(old, mk(d, Failed))
	}
	queued := enqueue(t, s, "k", now.Add(-100*24*time.Hour)) // an ancient job that never ran: not finished, never pruned
	running := mk(90*24*time.Hour, Running)
	if err := e.Prune(); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool { _, err := os.Stat(filepath.Join(s.Dir, id)); return err == nil }
	for _, id := range recent {
		if !exists(id) {
			t.Errorf("a job from the last 14 days must be kept (%s)", id)
		}
	}
	// 3 newest overall are among the recent ones, so the three old ones fall outside both rules and go
	for _, id := range old {
		if exists(id) {
			t.Errorf("an old job outside the newest %d must be pruned (%s)", e.KeepJobs, id)
		}
	}
	if !exists(queued) || !exists(running) {
		t.Error("a job that is not finished must never be pruned")
	}
}

func TestRetentionKeepsTheNewestNEvenWhenAllAreOld(t *testing.T) {
	e, s := newExec(t, map[string]Handler{})
	e.KeepJobs = 2
	var ids []string
	for i := 0; i < 5; i++ {
		age := time.Duration(30+i) * 24 * time.Hour
		id := enqueue(t, s, "k", t0.Add(-age))
		s.writeStatus(Status{ID: id, State: Succeeded, Created: t0.Add(-age), Finished: t0.Add(-age)})
		ids = append(ids, id)
	}
	e.Prune()
	left, _ := s.List()
	if len(left) != 2 {
		t.Fatalf("the newest 2 must survive even though all are older than 14 days: %d left", len(left))
	}
	// ids[0] is the youngest (30 days old) and ids[4] the oldest: the survivors are the two youngest
	got := map[string]bool{left[0].ID: true, left[1].ID: true}
	if !got[ids[0]] || !got[ids[1]] {
		t.Errorf("the survivors must be the two newest (%s, %s), got %v", ids[0], ids[1], got)
	}
}

func TestPruneRemovesAbandonedHalfWrittenJobsAfterAnHour(t *testing.T) {
	e, s := newExec(t, map[string]Handler{})
	dir := filepath.Join(s.Dir, "abandoned")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "request.json"), []byte("{}"), 0o600)
	e.Now = func() time.Time { return time.Now() }
	e.Prune()
	if _, err := os.Stat(dir); err != nil {
		t.Error("a recent unfinished upload may still be in progress: leave it")
	}
	e.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	e.Prune()
	if _, err := os.Stat(dir); err == nil {
		t.Error("an unfinished upload an hour old was abandoned: remove it")
	}
}

// A reader polling a status while the executor rewrites it never sees half a file.
func TestStatusIsAlwaysWholeJSON(t *testing.T) {
	_, s := newExec(t, nil)
	id := enqueue(t, s, "k", t0)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.writeStatus(Status{ID: id, Kind: "k", State: Running, Error: strings.Repeat("e", i%5000)})
		}
	}()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(filepath.Join(s.Dir, id, "status.json"))
		if err != nil {
			continue // not written yet
		}
		var st Status
		if err := json.Unmarshal(b, &st); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("a reader saw a torn status.json: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestRunLoopRecoversThenRunsAndStopsWhenAsked(t *testing.T) {
	var mu sync.Mutex
	ran := 0
	e, s := newExec(t, map[string]Handler{"k": func(ctx context.Context, j Job, log io.Writer) (any, error) {
		mu.Lock()
		ran++
		mu.Unlock()
		return nil, nil
	}})
	e.Poll = 10 * time.Millisecond
	e.Now = time.Now // the real clock: Prune compares it with real file times, and a pinned clock hours ahead would call a job still being written abandoned
	crashed := enqueue(t, s, "k", t0)
	s.writeStatus(Status{ID: crashed, State: Running})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	late := enqueue(t, s, "k", t0.Add(time.Hour)) // a job that arrives while the loop is running
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && status(t, s, late).State != Succeeded {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a clean stop is not an error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the loop did not stop")
	}
	if status(t, s, crashed).State != Failed {
		t.Error("the interrupted job must be recovered when the loop starts")
	}
	if st := status(t, s, late); st.State != Succeeded {
		all, _ := s.List()
		t.Errorf("a job that arrives while the loop runs must be picked up: %+v (all: %+v)", st, all)
	}
}

// The property READY exists for: however fast the executor scans, a job it picks up has every file its writer sent.
func TestAJobIsNeverRunBeforeItsFilesAreThere(t *testing.T) {
	const jobs, filesPer = 60, 6
	var mu sync.Mutex
	var broken []string
	ran := 0
	h := func(ctx context.Context, j Job, log io.Writer) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		ran++
		for i := 0; i < filesPer; i++ {
			b, err := os.ReadFile(filepath.Join(j.BundleDir, fmt.Sprintf("f%d", i)))
			if err != nil || len(b) != 300_000 {
				broken = append(broken, fmt.Sprintf("%s f%d: %d bytes, %v", j.ID, i, len(b), err))
			}
		}
		return nil, nil
	}
	e, s := newExec(t, map[string]Handler{"k": h})
	e.Now = time.Now
	e.Poll = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	files := map[string][]byte{}
	for i := 0; i < filesPer; i++ {
		files[fmt.Sprintf("f%d", i)] = make([]byte, 300_000)
	}
	for i := 0; i < jobs; i++ {
		if _, err := s.Enqueue(Request{Kind: "k", Origin: OriginTrigger}, files, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := ran
		mu.Unlock()
		if n == jobs {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if ran != jobs {
		t.Fatalf("only %d of %d jobs ran", ran, jobs)
	}
	if len(broken) > 0 {
		t.Errorf("%d job(s) were run before their files were complete, e.g. %s", len(broken), broken[0])
	}
}
