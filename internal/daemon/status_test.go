package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

func TestATickTellsTheStatusDocumentWhatItCouldNotDoAndWhatIsFailing(t *testing.T) {
	r := newRig(t)
	r.h.Live = &Live{}
	r.volume(t, "lib", "docs")
	r.eng.listed = append(r.eng.listed, volbackup.ListedVolume{
		Volume: volbackup.Volume{Project: "tenant", Pool: "default", Name: "newer"},
		Config: map[string]string{resolve.PolicyKey: `{"proto":2}`}})
	r.eng.poolErrs = map[string]error{"nas": errors.New("TrueNAS refused: api key tk-SECRET-123 is not valid")}
	r.eng.liveErr["docs"] = errors.New("incus said: token tk-SECRET-456 expired")
	r.eng.live["lib"] = map[string]string{
		resolve.CopyStampAt("nas"):   now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
		resolve.CopyFailAt("nas"):    now0.Add(-time.Minute).UTC().Format(time.RFC3339),
		resolve.CopyFailCount("nas"): "3",
	}

	var st SchedulerState
	r.h.Tick(&st)
	skipped, failing, ingress := r.h.Live.snapshot()

	if len(failing) != 1 || failing[0].Volume != "lib" || failing[0].Target != "nas" || failing[0].Count != 3 {
		t.Errorf("failing = %+v", failing)
	}
	reasons := map[string]string{}
	for _, s := range skipped {
		reasons[s.Volume] = s.Reason
	}
	if !strings.Contains(reasons["tenant/newer"], "protocol 2") {
		t.Errorf("a policy tink cannot read is explained by tink's own message: %v", reasons)
	}
	if reasons["pool nas"] != "the pool could not be listed" || reasons["docs"] != "the volume could not be read" {
		t.Errorf("what came from Incus gets a fixed phrase: %v", reasons)
	}
	for _, s := range skipped {
		if strings.Contains(s.Reason, "SECRET") {
			t.Errorf("an Incus error's text must never reach the status document: %+v", s)
		}
	}
	if ingress != nil {
		t.Errorf("no ingress pass has run: %+v", ingress)
	}
}

func TestATickWithNothingWrongLeavesTheDocumentClean(t *testing.T) {
	r := newRig(t)
	r.h.Live = &Live{}
	r.volume(t, "lib")
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-5 * time.Minute).UTC().Format(time.RFC3339)}
	r.h.Tick(&SchedulerState{})
	if skipped, failing, _ := r.h.Live.snapshot(); len(skipped) != 0 || len(failing) != 0 {
		t.Errorf("%+v %+v", skipped, failing)
	}
	// and a recovery clears it: the volume that could not be read can be again
	r.eng.liveErr["lib"] = errors.New("busy")
	r.h.Tick(&SchedulerState{})
	if skipped, _, _ := r.h.Live.snapshot(); len(skipped) != 1 {
		t.Fatalf("%+v", skipped)
	}
	delete(r.eng.liveErr, "lib")
	r.h.Tick(&SchedulerState{})
	if skipped, _, _ := r.h.Live.snapshot(); len(skipped) != 0 {
		t.Errorf("a volume that can be read again is no longer skipped: %+v", skipped)
	}
}

func TestBuildStatusSaysWhatTheHelperIsAndWhatItLastDid(t *testing.T) {
	r := newRig(t)
	r.volume(t, "lib")
	r.eng.copyErr["lib->nas"] = errors.New("target is full")
	id, _ := r.h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginTrigger}, now0)
	r.runJobs(t) // the real executor: the job fails because its copy does

	live := &Live{}
	live.setIngress(true, 2, now0)
	st := buildStatus(StatusOptions{Version: "v1.2.3", Zone: time.UTC, Store: r.h.Store}, live, now0.Add(-time.Hour), now0)

	if st.Version != "v1.2.3" || st.TZ != "UTC" || st.JobProto != jobs.Proto || st.PolicyProto != resolve.PolicyProto {
		t.Errorf("who it is, and what it speaks (so an apply can tell whether it reads a policy): %+v", st)
	}
	if !st.Started.Equal(now0.Add(-time.Hour)) {
		t.Errorf("started: %v", st.Started)
	}
	if st.LastJob == nil || st.LastJob.ID != id || st.LastJob.State != "failed" || st.LastJob.Finished.IsZero() {
		t.Errorf("last job: %+v", st.LastJob)
	}
	if strings.Contains(st.LastJob.ID, "full") {
		t.Error("the job's error text is not in the document")
	}
	if st.Ingress == nil || !st.Ingress.OK || st.Ingress.Warnings != 2 {
		t.Errorf("ingress: %+v", st.Ingress)
	}
	// a job that has not finished is not "the last job"
	r.h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginSchedule}, now0.Add(time.Hour))
	if st := buildStatus(StatusOptions{Store: r.h.Store}, live, now0, now0); st.LastJob == nil || st.LastJob.ID != id {
		t.Errorf("a queued job is not a finished one: %+v", st.LastJob)
	}
	// a daemon that runs no jobs has no last job to report
	if st := buildStatus(StatusOptions{}, live, now0, now0); st.LastJob != nil {
		t.Errorf("%+v", st.LastJob)
	}
}

// patchRecorder is the instance config the publisher writes to.
type patchRecorder struct {
	mu     sync.Mutex
	writes []map[string]string
}

func (p *patchRecorder) patch(c map[string]string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, c)
	return nil
}

func (p *patchRecorder) n() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.writes)
}

func TestTheStatusWorkerPublishesAtOnceAndThenOnlyWhatChanged(t *testing.T) {
	rec := &patchRecorder{}
	live := &Live{}
	o := StatusOptions{Publisher: &helper.Publisher{Patch: rec.patch, Heartbeat: time.Hour}, Interval: 10 * time.Millisecond, Version: "v1"}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { runStatus(ctx, &syncWriter{w: &out}, o, live, time.Now()); close(done) }()

	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	waitFor("the first write", func() bool { return rec.n() == 1 })
	time.Sleep(80 * time.Millisecond) // many intervals pass with nothing changing
	if rec.n() != 1 {
		t.Errorf("nothing changed, so nothing is written: %d writes", rec.n())
	}
	live.setBackup([]helper.Skip{{Volume: "lib", Reason: "x"}}, nil)
	waitFor("the write after a change", func() bool { return rec.n() == 2 })
	cancel()
	<-done
	st, err := helper.Parse(rec.writes[1][helper.StatusKey])
	if err != nil || len(st.Skipped) != 1 || st.Skipped[0].Volume != "lib" {
		t.Errorf("%+v %v", st, err)
	}
}

func TestAFailingPublisherIsLoggedOnceAndRecovers(t *testing.T) {
	var mu sync.Mutex
	fail := true
	calls := 0
	o := StatusOptions{Publisher: &helper.Publisher{Heartbeat: time.Hour, Patch: func(map[string]string) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if fail {
			return errors.New("not authorized")
		}
		return nil
	}}, Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { runStatus(ctx, &syncWriter{w: &out}, o, &Live{}, time.Now()); close(done) }()
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	fail = false
	mu.Unlock()
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done
	log := out.String()
	if got := strings.Count(log, "publishing the status document: not authorized"); got != 1 {
		t.Errorf("a standing failure is logged once, not every interval (%d times):\n%s", got, log)
	}
	if !strings.Contains(log, "recovered") {
		t.Errorf("and its recovery is noted:\n%s", log)
	}
}

func TestRunStartsTheStatusWorkerBesideTheOthersAndTheHelperFeedsIt(t *testing.T) {
	r := newRig(t)
	r.volume(t, "lib")
	r.eng.live["lib"] = map[string]string{
		resolve.CopyStampAt("nas"):   now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339),
		resolve.CopyFailAt("nas"):    now0.Add(-time.Minute).UTC().Format(time.RFC3339),
		resolve.CopyFailCount("nas"): "2",
	}
	r.h.Now = nil
	r.h.SchedulerInterval = 10 * time.Millisecond
	rec := &patchRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, &out, RunOptions{NoIngress: true, Helper: r.h, RestartBackoff: time.Millisecond, Status: &StatusOptions{
			Publisher: &helper.Publisher{Patch: rec.patch, Heartbeat: time.Hour}, Interval: 10 * time.Millisecond, Version: "vtest", Store: r.h.Store}})
	}()
	deadline := time.Now().Add(10 * time.Second)
	var st helper.Status
	for time.Now().Before(deadline) {
		rec.mu.Lock()
		for _, w := range rec.writes {
			if got, err := helper.Parse(w[helper.StatusKey]); err == nil && len(got.Failing) == 1 {
				st = got
			}
		}
		rec.mu.Unlock()
		if st.Proto != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st.Proto == 0 {
		t.Fatalf("the scheduler's finding never reached the document:\n%s", out.String())
	}
	if st.Version != "vtest" || st.Failing[0].Volume != "lib" || st.Failing[0].Count != 2 {
		t.Errorf("%+v", st)
	}
	if rec.writes[0][helper.MarkerKey] == "" {
		t.Error("a helper that publishes marks its instance, so `plan` can find it")
	}
}

func TestTheTimeZoneIsNamedSoAPersonCanReadIt(t *testing.T) {
	denver, err := time.LoadLocation("America/Denver")
	if err != nil {
		t.Skip("no tz database")
	}
	if got := zoneName(denver, now0); got != "America/Denver" {
		t.Errorf("a zone loaded by name says so: %q", got)
	}
	t.Setenv("TZ", "Europe/Paris")
	if got := zoneName(time.Local, now0); got != "Europe/Paris" {
		t.Errorf("the local zone is named by $TZ when it is set: %q", got)
	}
	t.Setenv("TZ", "")
	if got := zoneName(time.Local, now0); !strings.HasPrefix(got, "Local (") {
		t.Errorf("and by its abbreviation when not, never a bare \"Local\": %q", got)
	}
}

func TestNothingIsPublishedUntilTheSchedulerHasLooked(t *testing.T) {
	rec := &patchRecorder{}
	live := &Live{}
	o := StatusOptions{Publisher: &helper.Publisher{Patch: rec.patch, Heartbeat: time.Hour}, Interval: 10 * time.Millisecond, WaitForBackup: true}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runStatus(ctx, &syncWriter{w: &bytes.Buffer{}}, o, live, time.Now()); close(done) }()
	time.Sleep(100 * time.Millisecond)
	if rec.n() != 0 {
		t.Fatalf("a helper that has not looked at its volumes must not publish a document that says nothing is wrong: %d writes", rec.n())
	}
	live.setBackup(nil, nil) // the scheduler has completed a pass, and found nothing amiss
	deadline := time.Now().Add(5 * time.Second)
	for rec.n() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if rec.n() != 1 {
		t.Errorf("once it has looked it publishes: %d", rec.n())
	}
}

func TestTheSchedulerLooksAgainSoonAfterAPassThatCouldNotLookAndWaitsAfterOneThatCould(t *testing.T) {
	r := newRig(t)
	r.volume(t, "lib")
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-5 * time.Minute).UTC().Format(time.RFC3339)}
	var mu sync.Mutex
	connects := 0
	r.h.Connect = func() (backuprun.Engine, error) {
		mu.Lock()
		defer mu.Unlock()
		connects++
		if connects <= 2 { // the proxy to the host's API is not up yet
			return nil, errors.New("connection refused")
		}
		return r.eng, nil
	}
	r.h.Live = &Live{}
	r.h.Now = nil
	r.h.SchedulerInterval = time.Hour // so only the quick retry can explain a second pass
	r.h.RetryAfter = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.h.runScheduler(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !r.h.Live.backupKnown() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !r.h.Live.backupKnown() {
		t.Fatal("it never looked: a failed pass must be retried in seconds, not after the whole interval")
	}
	// now that it has looked, it waits the full (hour-long) interval: no further connects
	mu.Lock()
	before := connects
	mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if before != 3 || connects != before {
		t.Errorf("connects: %d at the first success, %d later; want 3 and no more", before, connects)
	}
}

func TestADrainQueuesNothingAndTheStatusDocumentSaysSo(t *testing.T) {
	r := newRig(t)
	r.h.Live = &Live{}
	r.volume(t, "lib")
	r.eng.live["lib"] = map[string]string{resolve.CopyStampAt("nas"): now0.Add(-3 * time.Hour).UTC().Format(time.RFC3339)} // due
	if err := os.MkdirAll(r.h.Store.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.h.Store.Dir, jobs.DrainFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var st SchedulerState
	if n := r.h.Tick(&st); n != 0 {
		t.Errorf("a copy is due, but a drain queues nothing: %d", n)
	}
	if !strings.Contains(r.logs.String(), "draining") {
		t.Errorf("and says why:\n%s", r.logs.String())
	}
	doc := buildStatus(StatusOptions{Store: r.h.Store}, r.h.Live, now0, now0)
	if !doc.Draining {
		t.Errorf("the status document says it is draining: %+v", doc)
	}
	os.Remove(filepath.Join(r.h.Store.Dir, jobs.DrainFile))
	if n := r.h.Tick(&st); n != 1 {
		t.Errorf("once the drain is lifted the due copy is queued: %d", n)
	}
	if doc := buildStatus(StatusOptions{Store: r.h.Store}, r.h.Live, now0, now0); doc.Draining || doc.Queued != 1 {
		t.Errorf("%+v", doc)
	}
}

func TestBuildStatusReportsTheRemotesItCanReachOnlyWhenItCouldRead(t *testing.T) {
	live := &Live{}
	rs := []helper.Remote{{Name: "host", Addr: "https://127.0.0.1:8443"}, {Name: "nas2", Addr: "https://nas2.lan:8443"}}
	if st := buildStatus(StatusOptions{Remotes: func() ([]helper.Remote, error) { return rs, nil }}, live, now0, now0); len(st.Remotes) != 2 || st.Remotes[1].Name != "nas2" {
		t.Errorf("%+v", st.Remotes)
	}
	// none is a statement; not knowing is not one
	if st := buildStatus(StatusOptions{Remotes: func() ([]helper.Remote, error) { return []helper.Remote{}, nil }}, live, now0, now0); st.Remotes == nil {
		t.Error("a helper that has no remote says so")
	}
	if st := buildStatus(StatusOptions{Remotes: func() ([]helper.Remote, error) { return nil, errors.New("config unreadable") }}, live, now0, now0); st.Remotes != nil {
		t.Errorf("a configuration that could not be read must not read as 'no remotes': %+v", st.Remotes)
	}
	if st := buildStatus(StatusOptions{}, live, now0, now0); st.Remotes != nil {
		t.Errorf("%+v", st.Remotes)
	}
}
