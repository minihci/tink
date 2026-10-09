// Package ingress implements tink's ingress self-registration reconciler:
// discovering instances that opt in via user.tink.ingress.{domain,port,enabled}
// config and rendering/applying the shared ingress instance's routes.
//
// This is a port of incus-host/reconciler/reconcile.sh and its DESIGN.md --
// see those for the full design rationale. Behavior is meant to match
// exactly, not improve on it yet; any deliberate difference is called out
// in this package's own comments.
package ingress

import (
	"fmt"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// Options configures where the reconciler looks for its inputs and where
// it writes/applies its outputs. Defaults match incus-host's reconciler.
type Options struct {
	Socket          string
	RoutesDir       string
	IngressInstance string
	DryRun          bool
	// ViaAPI reads and writes the route files through the ingress instance's file API instead of on the host's filesystem: RoutesDir is then
	// a path INSIDE that instance (InstanceRoutesDir), and nothing here touches the machine tink runs on. It is what lets the helper, which
	// has no access to the host's storage pool, run the reconcile, and what works under --remote. Off by default: the host-path mode is
	// what runs in production today.
	ViaAPI bool
}

// InstanceRoutesDir is where the generated route files are inside the ingress instance: the `ingress-routes` volume is mounted at
// /etc/caddy/routes there, and the reconciler owns its generated/ subdirectory (configs/ingress/Caddyfile imports both).
const InstanceRoutesDir = "/etc/caddy/routes/generated"

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
	// Legacy names the registered instances (project/name outside the default project) that still use the old user.ingress.* keys. It is not
	// in Warnings: it is the same few instances every pass until someone renames them, so each caller says it once and in its own way.
	Legacy  []string
	Diff    Diff
	Applied bool
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
	return reconcileOn(server, opts)
}

// reconcileOn is Reconcile on a connection already made, so the whole pass can be tested without a daemon.
func reconcileOn(server incus.InstanceServer, opts Options) (*Result, error) {
	regs, warnings, err := Discover(server)
	if err != nil {
		return nil, err
	}

	desired, err := Render(regs)
	if err != nil {
		return nil, err
	}

	var current map[string]string
	if opts.ViaAPI {
		current, err = readCurrentVia(server, opts.IngressInstance, opts.RoutesDir)
	} else {
		current, err = readCurrent(opts.RoutesDir)
	}
	if err != nil {
		return nil, err
	}

	d := computeDiff(current, desired)
	result := &Result{Registrations: regs, Warnings: warnings, Legacy: legacyNames(regs), Diff: d}

	if d.Empty() || opts.DryRun {
		return result, nil
	}

	if opts.ViaAPI {
		err = applyVia(server, opts.RoutesDir, opts.IngressInstance, desired)
	} else {
		err = apply(server, opts.RoutesDir, opts.IngressInstance, desired)
	}
	if err != nil {
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

// legacyNames lists the registrations that still use the old key names.
func legacyNames(regs []Registration) []string {
	var out []string
	for _, r := range regs {
		if r.Legacy {
			out = append(out, qualifiedName(r.Project, r.Name))
		}
	}
	return out
}

// LegacyNotice is the one-line request to rename, for the places that print it.
func LegacyNotice(names []string) string {
	return fmt.Sprintf("%d instance(s) register with the old user.ingress.* keys and should use user.tink.ingress.* instead (the old names still work for now): %s",
		len(names), strings.Join(names, ", "))
}
