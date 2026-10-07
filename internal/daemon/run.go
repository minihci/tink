// Package daemon implements "tink daemon": a long-running process that
// manages running tink's periodic actions itself (starting with ingress
// reconciliation), plus generating the init-system unit files needed to
// actually supervise it -- a persistent process needs real supervision
// (restart-on-crash, start-on-boot), which is exactly what an init
// system is for. Generating files for whichever init system the host
// already runs, rather than shipping one systemd unit and calling it
// done, keeps this consistent with not assuming any one host's init
// system -- the same reasoning that kept the reconciler on cron instead
// of a systemd timer in the first place, just applied to what happens
// once running it as a persistent process is the actual goal.
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

	"github.com/minihci/tink/internal/ingress"
)

// RunOptions configures the daemon.
type RunOptions struct {
	Interval       time.Duration
	IngressOptions ingress.Options
	// NoIngress leaves the ingress reconcile loop out (a helper that only runs backups, beside an ingress daemon that
	// already exists).
	NoIngress bool
	// Helper, if set, adds the backup scheduler and the job executor.
	Helper *Helper
	// RestartBackoff is the first wait before a crashed worker is restarted (default 5s; it doubles up to a minute).
	RestartBackoff time.Duration
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
	if !opts.NoIngress {
		start("ingress", func(ctx context.Context) error {
			return run(ctx, out, opts.Interval, func() (*ingress.Result, error) { return ingress.Reconcile(opts.IngressOptions) })
		})
	}
	if h := opts.Helper; h != nil {
		if h.Logf == nil {
			h.Logf = func(format string, args ...any) { fmt.Fprintf(out, "tink daemon: "+format+"\n", args...) }
		}
		fmt.Fprintf(out, "tink daemon: backup scheduler every %s, job executor on %s, schedules in %s\n", orInterval(h.SchedulerInterval), h.Store.Dir, h.now().Location())
		start("executor", func(ctx context.Context) error { return h.Executor().Run(ctx) })
		start("scheduler", func(ctx context.Context) error { return h.runScheduler(ctx) })
	}
	wg.Wait()
	if opts.NoIngress {
		fmt.Fprintln(out, "tink daemon: shutting down")
	}
	return nil
}

func orInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Minute
	}
	return d
}

// runScheduler ticks immediately and then every SchedulerInterval until ctx is done.
func (h *Helper) runScheduler(ctx context.Context) error {
	var state SchedulerState
	h.Tick(&state)
	t := time.NewTicker(orInterval(h.SchedulerInterval))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			h.Tick(&state)
		}
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

// run holds the actual loop mechanics, independent of what "reconcile"
// means -- reconcileOnce is injectable so the loop's own behavior (does
// it respect cancellation, does it survive a failed pass, does it run
// once immediately rather than waiting a full interval first) is
// testable without a live Incus daemon.
func run(ctx context.Context, out io.Writer, interval time.Duration, reconcileOnce func() (*ingress.Result, error)) error {
	fmt.Fprintf(out, "tink daemon: reconciling ingress every %s\n", interval)

	doOnePass := func() {
		result, err := reconcileOnce()
		if err != nil {
			// A failed pass logs and keeps running rather than exiting --
			// matching reconcile.sh's own cron-based resilience, where a
			// bad pass just means cron tries again in 60s; a persistent
			// loop should retry on its own schedule instead of dying.
			fmt.Fprintf(out, "tink daemon: reconcile error: %v\n", err)
			return
		}
		for _, w := range result.Warnings {
			fmt.Fprintf(out, "tink daemon: WARN: %s\n", w)
		}
		if result.Applied {
			fmt.Fprintf(out, "tink daemon: applied: +%d -%d ~%d\n",
				len(result.Diff.Added), len(result.Diff.Removed), len(result.Diff.Changed))
		}
	}

	doOnePass() // immediately, not after waiting a full interval first

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "tink daemon: shutting down")
			return nil
		case <-ticker.C:
			doOnePass()
		}
	}
}
