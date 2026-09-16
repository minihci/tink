// Package ingress implements tink's ingress self-registration reconciler:
// discovering instances that opt in via user.ingress.{domain,port,enabled}
// config and rendering/applying the shared ingress instance's routes.
//
// This is a port of incus-host/reconciler/reconcile.sh and its DESIGN.md --
// see those for the full design rationale. Behavior is meant to match
// exactly, not improve on it yet; any deliberate difference is called out
// in this package's own comments.
package ingress

import (
	"fmt"

	"github.com/minihci/tink/internal/incusapi"
)

// Options configures where the reconciler looks for its inputs and where
// it writes/applies its outputs. Defaults match incus-host's reconciler.
type Options struct {
	Socket          string
	RoutesDir       string
	IngressInstance string
	DryRun          bool
}

// DefaultOptions returns the same paths reconcile.sh has always used.
func DefaultOptions() Options {
	return Options{
		Socket:          incusapi.DefaultSocket,
		RoutesDir:       "/var/lib/incus/storage-pools/default/custom/default_ingress-routes/generated",
		IngressInstance: "ingress",
	}
}

// Result summarizes one reconcile or status pass.
type Result struct {
	Registrations []Registration
	Warnings      []string
	Diff          Diff
	Applied       bool
}

// Reconcile discovers registered instances and converges the shared
// ingress instance's routes to match. With Options.DryRun set, it computes
// and reports what would change without writing anything or reloading
// Caddy -- this exists specifically for safe side-by-side comparison
// against the bash version before cutover (see the repo README).
func Reconcile(opts Options) (*Result, error) {
	server, err := incusapi.Connect(opts.Socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to incus: %w", err)
	}

	regs, warnings, err := Discover(server)
	if err != nil {
		return nil, err
	}

	desired, err := Render(regs)
	if err != nil {
		return nil, err
	}

	current, err := readCurrent(opts.RoutesDir)
	if err != nil {
		return nil, err
	}

	d := computeDiff(current, desired)
	result := &Result{Registrations: regs, Warnings: warnings, Diff: d}

	if d.Empty() || opts.DryRun {
		return result, nil
	}

	if err := apply(opts.RoutesDir, opts.IngressInstance, desired); err != nil {
		return nil, err
	}
	result.Applied = true
	return result, nil
}

// Status reports what's currently registered and what would change,
// without touching anything -- always a dry run regardless of the
// caller's Options.DryRun.
func Status(opts Options) (*Result, error) {
	opts.DryRun = true
	return Reconcile(opts)
}
