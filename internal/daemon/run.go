// Package daemon implements "tink daemon run": a long-running process that runs the helper's periodic work -- the backup scheduler
// and the job executor -- and publishes the status document about them.
//
// This is a subcommand of the same tink binary, not a separate "tinkd"
// binary or a client/server split -- considered both and rejected them:
// a symlink/argv[0]-dispatch trick is implicit "magic" this project has
// consistently avoided elsewhere, and a thin-client-talks-to-a-daemon
// API design (the Docker/dockerd shape) only earns its complexity when
// the daemon is the sole owner of state a one-shot invocation can't
// otherwise see -- tink has no such state, since Incus's own daemon
// already owns everything tink cares about. One binary, plain
// subcommands (matching how k3s does "k3s server"/"k3s agent") gets the
// same "one binary" outcome with none of the added machinery.
package daemon

import (
	"context"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
	"time"

	"github.com/minihci/tink/internal/helper"
)

// RunOptions configures the daemon.
type RunOptions struct {
	// Helper, if set, adds the backup scheduler and the job executor.
	Helper *Helper
	// RestartBackoff is the first wait before a crashed worker is restarted (default 5s; it doubles up to a minute).
	RestartBackoff time.Duration
	// Status, if set, publishes the status document about this daemon (see StatusOptions).
	Status *StatusOptions
	// Reconciler, if set, keeps derived state in step with the server, woken by its events (see Reconciler). It is off by default.
	Reconciler *Reconciler
}

// Run starts the daemon's workers and runs them until ctx is cancelled (SIGTERM/SIGINT, wired up by the caller) -- a
// graceful exit, not a crash, on shutdown. Each worker is isolated: one that crashes or panics is logged and restarted,
// and does not stop the others.
func Run(ctx context.Context, out io.Writer, opts RunOptions) error {
	out = &syncWriter{w: out}
	var wg sync.WaitGroup
	start := func(name string, fn func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			supervise(ctx, out, name, opts.RestartBackoff, fn)
		}()
	}
	var live *Live
	if opts.Status != nil {
		live = &Live{}
		if opts.Helper != nil {
			opts.Helper.Live = live
		}
		seedFromStored(ctx, out, opts.Status.Publisher, live)
	}
	if h := opts.Helper; h != nil {
		if h.Logf == nil {
			h.Logf = func(format string, args ...any) { fmt.Fprintf(out, "tink daemon: "+format+"\n", args...) }
		}
		fmt.Fprintf(out, "tink daemon: backup scheduler every %s, job executor on %s, schedules in %s\n", orInterval(h.SchedulerInterval), h.Store.Dir, h.now().Location())
		start("executor", func(ctx context.Context) error { return h.Executor().Run(ctx) })
		start("scheduler", func(ctx context.Context) error { return h.runScheduler(ctx) })
	}
	if rc := opts.Reconciler; rc != nil {
		if rc.Live == nil {
			rc.Live = live
		}
		if rc.Logf == nil {
			rc.Logf = func(format string, args ...any) { fmt.Fprintf(out, "tink daemon: "+format+"\n", args...) }
		}
		fmt.Fprintf(out, "tink daemon: reconciler on the Incus event stream, resync every %s\n", rc.resync())
		start("reconciler", func(ctx context.Context) error { return rc.Run(ctx) })
	}
	if opts.Status != nil {
		started := time.Now()
		so := *opts.Status
		so.WaitForBackup = so.WaitForBackup || opts.Helper != nil
		start("status", func(ctx context.Context) error { return runStatus(ctx, out, so, live, started) })
	}
	wg.Wait()
	fmt.Fprintln(out, "tink daemon: shutting down")
	return nil
}

// seedFromStored gives the Live what this daemon's own status document said before it started, so that a restart does not make every standing problem look
// new: the clock keeps a state's since when the first pass confirms it, and the list of what the daemon did carries on. It reads the key the publisher
// writes, from where it writes it. The place it reads from may not be reachable yet (the helper's proxy to the host's API comes up a moment after its
// process does), so a failed read is tried again a few times, seedRetryDelay apart, before giving up. A document that still cannot be read seeds nothing:
// the clock starts empty, and there is no version handling to go wrong.
func seedFromStored(ctx context.Context, out io.Writer, p *helper.Publisher, live *Live) {
	if p == nil {
		return
	}
	var err error
	for attempt := 1; attempt <= seedAttempts; attempt++ {
		var st helper.Status
		if st, err = p.Load(); err == nil {
			live.Seed(st)
			return
		}
		if attempt < seedAttempts {
			select {
			case <-time.After(seedRetryDelay):
			case <-ctx.Done():
				return
			}
		}
	}
	fmt.Fprintf(out, "tink daemon: the stored status document could not be read after %d attempts, so the states start empty: %v\n", seedAttempts, err)
}

// seedAttempts and seedRetryDelay bound how long the daemon waits at start for its stored status document. Variables so a test can shorten them.
var (
	seedAttempts   = 3
	seedRetryDelay = 2 * time.Second
)

func orInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Minute
	}
	return d
}

// defaultRetryAfter is how soon the scheduler looks again after a pass that could not look: Incus not reachable yet (the helper's
// proxy to the host's API comes up a moment after its process does) or a listing that failed. A pass that worked waits the full
// interval; one that did not should not leave a minute of nothing.
const defaultRetryAfter = 5 * time.Second

// runScheduler ticks immediately and then every SchedulerInterval until ctx is done; after a pass that could not look, it tries again
// sooner.
func (h *Helper) runScheduler(ctx context.Context) error {
	var state SchedulerState
	interval := orInterval(h.SchedulerInterval)
	retry := h.RetryAfter
	if retry <= 0 {
		retry = defaultRetryAfter
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		h.Tick(&state)
		next := interval
		if !h.tickLooked.Load() && retry < interval {
			next = retry
		}
		timer.Reset(next)
	}
}

// supervise runs fn until ctx is done, and restarts it, after a growing wait, if it returns early or panics: a worker
// that dies must be noticed and brought back, and must never take the process, or the other workers, with it.
func supervise(ctx context.Context, out io.Writer, name string, first time.Duration, fn func(ctx context.Context) error) {
	wait := first
	if wait <= 0 {
		wait = 5 * time.Second
	}
	base := wait
	for {
		started := time.Now()
		err := func() (err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic: %v\n%s", r, debug.Stack())
				}
			}()
			return fn(ctx)
		}()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			fmt.Fprintf(out, "tink daemon: %s stopped: %v; restarting in %s\n", name, err, wait)
		} else {
			fmt.Fprintf(out, "tink daemon: %s stopped without being asked; restarting in %s\n", name, wait)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if time.Since(started) > time.Minute { // it ran for a while: a fresh start, not a crash loop
			wait = base
		} else if wait *= 2; wait > time.Minute {
			wait = time.Minute
		}
	}
}

// syncWriter makes a writer safe for the several workers that share it.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}
