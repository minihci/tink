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
	"time"

	"github.com/minihci/tink/internal/ingress"
)

// RunOptions configures the reconcile loop.
type RunOptions struct {
	Interval       time.Duration
	IngressOptions ingress.Options
}

// Run reconciles ingress registrations on Interval until ctx is
// cancelled (SIGTERM/SIGINT, wired up by the caller) -- a graceful exit,
// not a crash, on shutdown.
func Run(ctx context.Context, out io.Writer, opts RunOptions) error {
	return run(ctx, out, opts.Interval, func() (*ingress.Result, error) {
		return ingress.Reconcile(opts.IngressOptions)
	})
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
