// Package bootstrap implements tink's "capability zero": converging a
// fresh (or drifted) Incus host to its declared platform state -- storage
// volumes, profiles, the ingress/authelia/incus-ui instances, and the
// daemon's own OIDC/authorization config.
//
// This is a port of incus-host/scripts/deploy.sh -- see that script and
// incus-host/README.md for the full design rationale. Behavior is meant
// to match exactly, including deploy.sh's own quirks (every run
// unconditionally deletes and relaunches incus-ui/authelia/ingress, even
// if nothing changed); any deliberate difference is called out in this
// package's own comments.
package bootstrap

import "fmt"

// Options configures where apply reads its inputs from and how it
// behaves.
type Options struct {
	// RepoRoot is the incus-host checkout's root -- every relative path
	// in deploy.env, the profile templates, and the reconciler cron entry
	// is resolved from here, matching deploy.sh's own `cd "$(dirname "$0")/.."`.
	RepoRoot string
	Config   Config
	DryRun   bool
}

// Result is the ordered log of what apply did (or, in dry-run mode,
// would do).
type Result struct {
	Actions []string
}

// Run validates deploy.env and the required secret files, then converges
// the host to its declared state in the same order deploy.sh does:
// registries, storage volumes, profiles, incus-ui, authelia, ingress, the
// daemon's own config, and finally the reconciler's cron entry.
func Run(opts Options) (*Result, error) {
	if err := opts.Config.Validate(); err != nil {
		return &Result{}, err
	}
	if err := checkSecretsPresent(opts.RepoRoot); err != nil {
		return &Result{}, err
	}

	r := &runner{dryRun: opts.DryRun}

	steps := []struct {
		name string
		fn   func(*runner, Options) error
	}{
		{"registries", applyRegistries},
		{"storage volumes", applyStorageVolumes},
		{"profiles", applyProfiles},
		{"incus-ui", applyIncusUI},
		{"authelia", applyAuthelia},
		{"ingress", applyIngress},
		{"daemon config", applyDaemonConfig},
		{"reconciler cron", applyReconcilerCron},
	}

	for _, step := range steps {
		if err := step.fn(r, opts); err != nil {
			return &Result{Actions: r.actions}, fmt.Errorf("%s: %w", step.name, err)
		}
	}

	return &Result{Actions: r.actions}, nil
}
