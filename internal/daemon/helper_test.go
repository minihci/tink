package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

var now0 = time.Date(2026, 10, 7, 10, 30, 0, 0, time.UTC)

// stubEngine answers LiveConfig from a table and records copies.
type stubEngine struct {
	mu      sync.Mutex
	live    map[string]map[string]string
	liveErr map[string]error
	copyErr map[string]error
	copied  []string
}

func (s *stubEngine) LiveConfig(v volbackup.Volume) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveErr[v.Name]; err != nil {
		return nil, err
	}
	return s.live[v.Name], nil
}

func (s *stubEngine) Copy(v volbackup.Volume, t volbackup.Target, o volbackup.CopyOptions) (volbackup.CopyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := v.Name + "->" + t.Name
	s.copied = append(s.copied, k)
	if err := s.copyErr[k]; err != nil {
		return volbackup.CopyResult{}, err
	}
	return volbackup.CopyResult{Volume: v.Name + "-bk-1"}, nil
}

func (s *stubEngine) copies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.copied...)
}

func stackFiles(volumes ...string) map[string][]byte {
	var b strings.Builder
	b.WriteString("kind: backup-target\nname: nas\nlocation: other-host\nengine: incus\npool: nas\n")
	for _, v := range volumes {
		fmt.Fprintf(&b, "---\nkind: storage-volume\nname: %s\nbackup:\n  copies:\n    - {target: nas, schedule: \"@hourly\", retain: 30d}\n", v)
	}
	return map[string][]byte{"tink.yaml": []byte(b.String())}
}

type rig struct {
	h    *Helper
	eng  *stubEngine
	logs *logBuf
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format+"\n", args...)
}
func (l *logBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func newRig(t *testing.T) *rig {
	t.Helper()
	root := t.TempDir()
	eng := &stubEngine{live: map[string]map[string]string{}, liveErr: map[string]error{}, copyErr: map[string]error{}}
	logs := &logBuf{}
	h := &Helper{
		Stacks:  jobs.Stacks{Dir: filepath.Join(root, "stacks")},
		Store:   jobs.Store{Dir: filepath.Join(root, "jobs")},
		Connect: func() (backuprun.Engine, error) { return eng, nil },
		Version: "test",
		Logf:    logs.logf,
		Now:     func() time.Time { return now0 },
	}
	return &rig{h: h, eng: eng, logs: logs}
}

func (r *rig) sync(t *testing.T, name string, volumes ...string) {
	t.Helper()
	if err := r.h.Stacks.Sync(name, stackFiles(volumes...), []string{"tink.yaml"}, now0); err != nil {
		t.Fatal(err)
	}
}

func (r *rig) runJobs(t *testing.T) int {
	t.Helper()
	n, err := r.h.Executor().RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTickQueuesAJobOnlyWhenACopyIsDue(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib")
	recent := now0.Add(-10 * time.Minute).UTC().Format(time.RFC3339) // 10:20: an hourly copy is not due until 11:00
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): recent}
	var st SchedulerState
	if n := r.h.Tick(&st); n != 0 {
		t.Errorf("nothing is due, nothing is queued: %d", n)
	}
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339)}
	if n := r.h.Tick(&st); n != 1 {
		t.Fatalf("a due copy queues one job: %d", n)
	}
	list, _ := r.h.Store.List()
	if len(list) != 1 || list[0].Kind != KindBackupRun || list[0].Origin != jobs.OriginSchedule || list[0].State != jobs.Queued {
		t.Fatalf("%+v", list)
	}
	// while that job is queued, the next ticks queue nothing more
	for i := 0; i < 3; i++ {
		if n := r.h.Tick(&st); n != 0 {
			t.Errorf("tick %d queued a duplicate", i)
		}
	}
}

func TestTickRespectsBackoffSoAFailingCopyIsNotQueuedEveryMinute(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib")
	r.eng.live["lib"] = map[string]string{
		resolve.CopyStampAt("nas"):   now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
		resolve.CopyFailAt("nas"):    now0.Add(-time.Minute).UTC().Format(time.RFC3339),
		resolve.CopyFailCount("nas"): "3",
	}
	var st SchedulerState
	if n := r.h.Tick(&st); n != 0 {
		t.Errorf("a copy that failed a minute ago is backing off: queued %d", n)
	}
	// once the backoff has elapsed (3 failures: 5m x 4 = 20m)
	r.h.Now = func() time.Time { return now0.Add(25 * time.Minute) }
	if n := r.h.Tick(&st); n != 1 {
		t.Errorf("after the backoff it is due again: %d", n)
	}
}

func TestTickBeatsTheHeartbeatEvenWhenThereIsNothingToDo(t *testing.T) {
	r := newRig(t)
	var st SchedulerState
	r.h.Tick(&st)
	hb, err := r.h.Store.LastBeat()
	if err != nil || !hb.Time.Equal(now0) || hb.PID == 0 || hb.Version != "test" || hb.Zone != "UTC" {
		t.Errorf("%v %+v", err, hb)
	}
}

func TestStacksAreIndependent(t *testing.T) {
	r := newRig(t)
	r.sync(t, "alpha", "a")
	r.sync(t, "bravo", "b")
	old := now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	r.eng.live["a"] = map[string]string{resolve.CopyStampAt("nas"): old}
	r.eng.live["b"] = map[string]string{resolve.CopyStampAt("nas"): old}
	var st SchedulerState
	if n := r.h.Tick(&st); n != 2 {
		t.Fatalf("both stacks are due: %d", n)
	}
	r.runJobs(t) // both finish; the stub does not stamp, so both are due again
	// now only bravo has a job waiting
	if _, err := r.h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginTrigger, Stack: "bravo"}, nil, now0); err != nil {
		t.Fatal(err)
	}
	if n := r.h.Tick(&st); n != 1 {
		t.Fatalf("only the stack with no pending job is queued: %d", n)
	}
	list, _ := r.h.Store.List()
	queuedFor := map[string]int{}
	for _, j := range list {
		if j.State == jobs.Queued {
			req, _ := os.ReadFile(filepath.Join(r.h.Store.Dir, j.ID, "request.json"))
			for _, name := range []string{"alpha", "bravo"} {
				if strings.Contains(string(req), `"`+name+`"`) {
					queuedFor[name]++
				}
			}
		}
	}
	if queuedFor["alpha"] != 1 || queuedFor["bravo"] != 1 {
		t.Errorf("each stack has exactly one waiting job (bravo's is the triggered one): %v", queuedFor)
	}
}

func TestProblemsAreLoggedOnceAndRecoveriesAreNoted(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib")
	r.eng.liveErr["lib"] = errors.New("volume not found")
	var st SchedulerState
	for i := 0; i < 5; i++ {
		r.h.Tick(&st)
	}
	if got := strings.Count(r.logs.String(), "volume not found"); got != 1 {
		t.Errorf("a standing problem is logged once, not every tick (%d times):\n%s", got, r.logs.String())
	}
	delete(r.eng.liveErr, "lib")
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.UTC().Format(time.RFC3339)}
	r.h.Tick(&st)
	if !strings.Contains(r.logs.String(), "recovered:") {
		t.Errorf("a recovery is noted:\n%s", r.logs.String())
	}
	// and the same problem coming back is reported again
	r.eng.liveErr["lib"] = errors.New("volume not found")
	r.h.Tick(&st)
	if got := strings.Count(r.logs.String(), "volume not found"); got != 2 {
		t.Errorf("a problem that returns is reported again: %d", got)
	}
}

func TestABrokenStackDoesNotStopTheOthersAndIsReportedOnce(t *testing.T) {
	r := newRig(t)
	r.sync(t, "good", "lib")
	r.sync(t, "broken", "x")
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339)}
	vdir, _ := filepath.EvalSymlinks(filepath.Join(r.h.Stacks.Dir, "broken", "current"))
	os.WriteFile(filepath.Join(vdir, "tink.yaml"), []byte("kind: nonsense\nname: x\n"), 0o600)
	var st SchedulerState
	for i := 0; i < 3; i++ {
		r.h.Tick(&st)
	}
	list, _ := r.h.Store.List()
	if len(list) != 1 {
		t.Errorf("the good stack's job is queued once: %v", list)
	}
	if got := strings.Count(r.logs.String(), `stack "broken" does not load`); got != 1 {
		t.Errorf("a broken stack is reported once: %d\n%s", got, r.logs.String())
	}
}

func TestIncusBeingDownIsLoggedAndIsNotMistakenForRecovery(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib")
	r.eng.liveErr["lib"] = errors.New("volume not found")
	var st SchedulerState
	r.h.Tick(&st) // logs the volume problem
	r.h.Connect = func() (backuprun.Engine, error) { return nil, errors.New("socket: connection refused") }
	r.h.Tick(&st)
	r.h.Tick(&st)
	logs := r.logs.String()
	if strings.Count(logs, "connection refused") != 1 {
		t.Errorf("logged once:\n%s", logs)
	}
	if strings.Contains(logs, "recovered") {
		t.Errorf("when Incus cannot be reached nothing can be judged recovered:\n%s", logs)
	}
}

// The handler, end to end through the real executor.
func TestABackupJobRunsAndRecordsWhatItDid(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib", "photos")
	var st SchedulerState
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339)}
	r.eng.live["photos"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-5 * time.Minute).UTC().Format(time.RFC3339)} // not due
	r.h.Tick(&st)
	if n := r.runJobs(t); n != 1 {
		t.Fatalf("ran %d", n)
	}
	if got := r.eng.copies(); len(got) != 1 || got[0] != "lib->nas" {
		t.Errorf("the job is a due run: only the due copy is made, got %v", got)
	}
	list, _ := r.h.Store.List()
	if list[0].State != jobs.Succeeded {
		t.Fatalf("%+v", list[0])
	}
	var rep backuprun.Report
	if err := json.Unmarshal(list[0].Summary, &rep); err != nil || rep.Tried != 1 || rep.Skipped != 1 || len(rep.Copies) != 2 {
		t.Errorf("the summary is the report: %v %+v", err, rep)
	}
	if log, _ := r.h.Store.Log(list[0].ID); !strings.Contains(log, "copied lib -> nas") || !strings.Contains(log, "photos -> nas: not yet due") {
		t.Errorf("the job's log is what `backup run` prints:\n%s", log)
	}
}

func TestAFailingCopyFailsTheJobButKeepsTheReport(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib", "photos")
	r.eng.copyErr["lib->nas"] = errors.New("target is full")
	id, _ := r.h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginTrigger, Stack: "home"}, nil, now0)
	r.runJobs(t)
	st, _ := r.h.Store.Status(id)
	if st.State != jobs.Failed || !strings.Contains(st.Error, "1 copy operation(s) failed") {
		t.Errorf("%+v", st)
	}
	var rep backuprun.Report
	if err := json.Unmarshal(st.Summary, &rep); err != nil || rep.Failed != 1 || len(rep.Copies) != 2 {
		t.Errorf("a failed job still says what it did: %v %+v", err, rep)
	}
	if got := r.eng.copies(); len(got) != 2 {
		t.Errorf("the other volume is still copied: %v", got)
	}
}

func TestATriggeredJobCanCarryItsOwnStack(t *testing.T) {
	r := newRig(t) // note: no stack is synced
	args, _ := json.Marshal(BackupRunArgs{Volumes: []string{"lib"}})
	id, err := r.h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginTrigger, Entries: []string{"tink.yaml"}, Args: args}, stackFiles("lib", "photos"), now0)
	if err != nil {
		t.Fatal(err)
	}
	r.runJobs(t)
	if st, _ := r.h.Store.Status(id); st.State != jobs.Succeeded {
		t.Fatalf("%+v", st)
	}
	if got := r.eng.copies(); len(got) != 1 || got[0] != "lib->nas" {
		t.Errorf("only the named volume: %v", got)
	}
}

func TestBadJobsFailWithAReason(t *testing.T) {
	r := newRig(t)
	cases := map[string]struct {
		req     jobs.Request
		bundle  map[string][]byte
		wantErr string
	}{
		"a stack that was never synced":      {jobs.Request{Kind: KindBackupRun, Stack: "ghost"}, nil, "no active version"},
		"neither a stack nor a bundle":       {jobs.Request{Kind: KindBackupRun}, nil, "names no stack and carries none"},
		"an entry that leaves the bundle":    {jobs.Request{Kind: KindBackupRun, Entries: []string{"../../etc/x"}}, map[string][]byte{"tink.yaml": nil}, "leaves the directory"},
		"an entry that is not in the bundle": {jobs.Request{Kind: KindBackupRun, Entries: []string{"missing.yaml"}}, map[string][]byte{"tink.yaml": stackFiles("x")["tink.yaml"]}, "no such file"},
		"arguments that are not JSON":        {jobs.Request{Kind: KindBackupRun, Stack: "ghost", Args: json.RawMessage(`"oops"`)}, nil, "arguments"},
	}
	ids := map[string]string{}
	for name, c := range cases {
		id, err := r.h.Store.Enqueue(c.req, c.bundle, now0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ids[name] = id
	}
	r.runJobs(t)
	for name, c := range cases {
		st, _ := r.h.Store.Status(ids[name])
		if st.State != jobs.Failed || !strings.Contains(st.Error, c.wantErr) {
			t.Errorf("%s: %+v, want a failure containing %q", name, st, c.wantErr)
		}
	}
}

func TestAJobFailsClearlyWhenIncusCannotBeReached(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib")
	r.h.Connect = func() (backuprun.Engine, error) { return nil, errors.New("connection refused") }
	id, _ := r.h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Stack: "home"}, nil, now0)
	r.runJobs(t)
	if st, _ := r.h.Store.Status(id); st.State != jobs.Failed || !strings.Contains(st.Error, "connecting to incus: connection refused") {
		t.Errorf("%+v", st)
	}
}

func TestSuperviseRestartsACrashedWorkerAndStopsWhenAsked(t *testing.T) {
	var out bytes.Buffer
	var mu sync.Mutex
	starts := 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		supervise(ctx, &syncWriter{w: &out}, "w", time.Millisecond, func(ctx context.Context) error {
			mu.Lock()
			starts++
			n := starts
			mu.Unlock()
			switch n {
			case 1:
				panic("kaboom")
			case 2:
				return errors.New("lost its connection")
			case 3:
				return nil // returned without being asked
			}
			<-ctx.Done()
			return nil
		})
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := starts
		mu.Unlock()
		if n >= 4 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("supervise did not stop")
	}
	logs := out.String()
	for _, want := range []string{"w stopped: panic: kaboom", "w stopped: lost its connection", "w stopped without being asked", "restarting in"} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log must say %q:\n%s", want, logs)
		}
	}
	if starts != 4 {
		t.Errorf("started %d times, want 4 (three failures, then a stable run)", starts)
	}
}

// The whole thing, as `tink daemon run` runs it: a synced stack with a due copy turns, unattended, into a finished job.
func TestRunStartsTheWorkersAndAJobHappensWithoutAnyoneAsking(t *testing.T) {
	r := newRig(t)
	r.sync(t, "home", "lib")
	r.eng.live["lib"] = map[string]string{} // never copied: due
	r.h.Now = nil
	r.h.SchedulerInterval = 20 * time.Millisecond
	r.h.ExecutorPoll = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, &out, RunOptions{NoIngress: true, Helper: r.h, RestartBackoff: time.Millisecond})
	}()

	deadline := time.Now().Add(10 * time.Second)
	var st jobs.Status
	for time.Now().Before(deadline) {
		if list, _ := r.h.Store.List(); len(list) > 0 && list[0].State == jobs.Succeeded {
			st = list[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a clean stop is not an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon did not stop")
	}
	if st.ID == "" {
		t.Fatalf("no job ran by itself:\n%s", out.String())
	}
	if got := r.eng.copies(); len(got) == 0 || got[0] != "lib->nas" {
		t.Errorf("copies: %v", got)
	}
	if _, err := r.h.Store.LastBeat(); err != nil {
		t.Errorf("the heartbeat must be written: %v", err)
	}
	if !strings.Contains(out.String(), "backup scheduler every 20ms") || !strings.Contains(out.String(), "shutting down") {
		t.Errorf("the daemon says what it runs and that it stopped:\n%s", out.String())
	}
}

// A long copy occupies the executor and nothing else: the scheduler keeps ticking (so the heartbeat advances, and a
// stack that falls due is still noticed), because each worker runs on its own.
type blockingEngine struct {
	*stubEngine
	entered chan struct{}
	release chan struct{}
}

func (b *blockingEngine) Copy(v volbackup.Volume, t volbackup.Target, o volbackup.CopyOptions) (volbackup.CopyResult, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.stubEngine.Copy(v, t, o)
}

func TestALongCopyDoesNotStopTheSchedulerOrTheHeartbeat(t *testing.T) {
	r := newRig(t)
	r.sync(t, "slow", "lib")
	r.sync(t, "other", "docs")
	be := &blockingEngine{stubEngine: r.eng, entered: make(chan struct{}, 1), release: make(chan struct{})}
	r.h.Connect = func() (backuprun.Engine, error) { return be, nil }
	r.h.Now = nil
	r.h.SchedulerInterval = 15 * time.Millisecond
	r.h.ExecutorPoll = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		Run(ctx, &out, RunOptions{NoIngress: true, Helper: r.h, RestartBackoff: time.Millisecond})
		close(done)
	}()
	defer func() { cancel(); <-done }()

	select {
	case <-be.entered: // a copy is now in flight and stays there
	case <-time.After(10 * time.Second):
		t.Fatal("no copy started")
	}
	first, err := r.h.Store.LastBeat()
	if err != nil {
		t.Fatal(err)
	}
	// while it is stuck, the heartbeat keeps advancing: the scheduler is alive
	deadline := time.Now().Add(5 * time.Second)
	advanced := false
	for time.Now().Before(deadline) {
		if hb, err := r.h.Store.LastBeat(); err == nil && hb.Time.After(first.Time) {
			advanced = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !advanced {
		t.Error("the heartbeat stopped while a copy was running: one long job must not stall the scheduler")
	}
	close(be.release)
}
