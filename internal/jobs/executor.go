package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/minihci/tink/internal/secrets"
)

// Handler does one kind of job. It writes what a person would want to read to log, returns what it did as the job's
// summary (anything that marshals to JSON, or nil), and returns an error if it failed. ctx is cancelled when someone
// asks the job to stop.
type Handler func(ctx context.Context, j Job, log io.Writer) (summary any, err error)

// Executor runs the jobs in a Store, one at a time, oldest first.
type Executor struct {
	Store    Store
	Handlers map[string]Handler
	// Redactor scrubs secrets from logs and error text; nil leaves them as they are.
	Redactor *secrets.Redactor
	Now      func() time.Time
	// Poll is how often the directory is scanned (default 2s) and CancelPoll how often a running job is checked for a
	// cancel request (default 1s).
	Poll, CancelPoll time.Duration
	// KeepJobs and KeepFor are the retention: a finished job is kept while it is among the newest KeepJobs (default 50) or
	// finished within KeepFor (default 14 days), whichever keeps more.
	KeepJobs int
	KeepFor  time.Duration
	// LogLimit bounds a job's log (default 1 MiB).
	LogLimit int64
	// Logf reports what the executor itself does (not the jobs); nil discards it.
	Logf func(format string, args ...any)
}

func (e *Executor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Executor) logf(format string, args ...any) {
	if e.Logf != nil {
		e.Logf(format, args...)
	}
}

func orDefault[T comparable](v, d T) T {
	var zero T
	if v == zero {
		return d
	}
	return v
}

func (e *Executor) scrub(s string) string {
	if e.Redactor == nil {
		return s
	}
	return e.Redactor.Redact(s)
}

// Recover marks every job still `running` as failed: the previous helper process died (or was upgraded) in the middle
// of it. The schedule will retry the work; nothing in the job's own state can be trusted to resume.
func (e *Executor) Recover() error {
	list, err := e.Store.List()
	if err != nil {
		return err
	}
	for _, st := range list {
		if st.State != Running {
			continue
		}
		st.State, st.Finished = Failed, e.now().UTC()
		st.Error = "interrupted: the helper stopped while this job was running"
		if err := e.Store.writeStatus(st); err != nil {
			return err
		}
		e.logf("job %s was running when the helper stopped: marked failed", st.ID)
	}
	return nil
}

// RunOnce runs every queued job, oldest first, then applies the retention. It returns how many ran.
func (e *Executor) RunOnce(ctx context.Context) (int, error) {
	list, err := e.Store.List()
	if err != nil {
		return 0, err
	}
	ran := 0
	for _, st := range list {
		if st.State != Queued {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		e.runJob(ctx, st)
		ran++
	}
	if err := e.Prune(); err != nil {
		e.logf("pruning old jobs: %v", err)
	}
	return ran, nil
}

// Run recovers, then scans the directory every Poll until ctx is done.
func (e *Executor) Run(ctx context.Context) error {
	if err := e.Recover(); err != nil {
		return fmt.Errorf("recovering interrupted jobs: %w", err)
	}
	tick := time.NewTicker(orDefault(e.Poll, 2*time.Second))
	defer tick.Stop()
	for {
		if _, err := e.RunOnce(ctx); err != nil {
			e.logf("scanning the jobs directory: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (e *Executor) fail(st Status, msg string) {
	st.State, st.Finished, st.Error = Failed, e.now().UTC(), e.scrub(msg)
	if err := e.Store.writeStatus(st); err != nil {
		e.logf("job %s: writing its status: %v", st.ID, err)
	}
}

// runJob runs one queued job. Nothing a job does, however malformed its request or badly it fails, may stop the
// executor: every problem becomes that job's failed status.
func (e *Executor) runJob(parent context.Context, queued Status) {
	id := queued.ID
	dir := e.Store.jobDir(id)
	st := Status{ID: id, Kind: queued.Kind, Origin: queued.Origin, Created: queued.Created, State: Running, Started: e.now().UTC()}

	req, err := e.Store.readRequest(id)
	if err != nil {
		e.fail(st, fmt.Sprintf("unreadable request: %v", err))
		return
	}
	st.Kind, st.Origin, st.Created = req.Kind, req.Origin, req.Created
	if req.Proto != Proto {
		e.fail(st, fmt.Sprintf("unsupported protocol %d: this helper speaks %d", req.Proto, Proto))
		return
	}
	h, ok := e.Handlers[req.Kind]
	if !ok {
		e.fail(st, fmt.Sprintf("unknown job kind %q", req.Kind))
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "cancel")); err == nil {
		st.State, st.Finished = Cancelled, e.now().UTC()
		_ = e.Store.writeStatus(st)
		return
	}
	if err := e.Store.writeStatus(st); err != nil {
		e.logf("job %s: writing its status: %v", id, err)
		return
	}

	logFile, err := os.OpenFile(filepath.Join(dir, "log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		e.fail(st, fmt.Sprintf("opening the log: %v", err))
		return
	}
	defer logFile.Close()
	var w io.Writer = &boundedWriter{w: logFile, left: orDefault(e.LogLimit, 1<<20)}
	var rw *secrets.RedactingWriter
	if e.Redactor != nil {
		rw = e.Redactor.Writer(w)
		w = rw
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cancelled := make(chan struct{})
	go e.watchCancel(ctx, dir, cancel, cancelled)

	summary, runErr := e.invoke(ctx, h, Job{ID: id, Dir: dir, Request: req, BundleDir: filepath.Join(dir, "bundle")}, w)
	if rw != nil {
		_ = rw.Flush()
	}

	st.Finished = e.now().UTC()
	wasCancelled := false
	select {
	case <-cancelled:
		wasCancelled = true
	default:
	}
	switch {
	case runErr == nil:
		st.State = Succeeded // a job that finished, finished, even if someone asked it to stop a moment too late
	case wasCancelled:
		st.State = Cancelled
	default:
		st.State, st.Error = Failed, e.scrub(runErr.Error())
	}
	if summary != nil {
		if b, err := json.Marshal(summary); err == nil {
			st.Summary = b
		}
	}
	if err := e.Store.writeStatus(st); err != nil {
		e.logf("job %s: writing its status: %v", id, err)
	}
}

// invoke runs a handler and turns a panic into an error.
func (e *Executor) invoke(ctx context.Context, h Handler, j Job, log io.Writer) (summary any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the job panicked: %v", r)
		}
	}()
	return h(ctx, j, log)
}

// watchCancel cancels the job's context when a cancel file appears.
func (e *Executor) watchCancel(ctx context.Context, dir string, cancel context.CancelFunc, cancelled chan<- struct{}) {
	t := time.NewTicker(orDefault(e.CancelPoll, time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := os.Stat(filepath.Join(dir, "cancel")); err == nil {
				close(cancelled)
				cancel()
				return
			}
		}
	}
}

// Prune applies the retention, and removes job directories that never got a READY (a writer that died part way), once
// they are an hour old.
func (e *Executor) Prune() error {
	now := e.now()
	entries, err := os.ReadDir(e.Store.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, d := range entries {
		if !d.IsDir() || !ValidID(d.Name()) {
			continue
		}
		dir := e.Store.jobDir(d.Name())
		if _, err := os.Stat(filepath.Join(dir, "READY")); err != nil {
			if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
				_ = os.RemoveAll(dir)
			}
		}
	}

	list, err := e.Store.List()
	if err != nil {
		return err
	}
	var done []Status
	for _, st := range list {
		if st.State.Finished() {
			done = append(done, st)
		}
	}
	sort.SliceStable(done, func(i, j int) bool { return finishedAt(done[i]).After(finishedAt(done[j])) }) // newest first
	keepN, keepFor := orDefault(e.KeepJobs, 50), orDefault(e.KeepFor, 14*24*time.Hour)
	for i, st := range done {
		if i < keepN || now.Sub(finishedAt(st)) <= keepFor {
			continue
		}
		if err := os.RemoveAll(e.Store.jobDir(st.ID)); err != nil {
			return err
		}
	}
	return nil
}

func finishedAt(st Status) time.Time {
	if !st.Finished.IsZero() {
		return st.Finished
	}
	return st.Created
}

// boundedWriter writes up to left bytes, then drops the rest after saying so once.
type boundedWriter struct {
	w     io.Writer
	left  int64
	noted bool
}

func (b *boundedWriter) Write(p []byte) (int, error) {
	if b.left <= 0 {
		if !b.noted {
			b.noted = true
			_, _ = b.w.Write([]byte("\n[log truncated: it reached its size limit]\n"))
		}
		return len(p), nil
	}
	if int64(len(p)) > b.left {
		n, err := b.w.Write(p[:b.left])
		b.left = 0
		if !b.noted {
			b.noted = true
			_, _ = b.w.Write([]byte("\n[log truncated: it reached its size limit]\n"))
		}
		if err != nil {
			return n, err
		}
		return len(p), nil
	}
	n, err := b.w.Write(p)
	b.left -= int64(n)
	return n, err
}
