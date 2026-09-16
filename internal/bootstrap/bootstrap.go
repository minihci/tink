// Package bootstrap implements tink's "capability zero": converging a
// fresh (or drifted) Incus host to its declared platform state — storage
// volumes, profiles, the ingress/authelia/incus-ui instances, and the
// daemon's own OIDC/authorization config.
//
// Not yet ported. The working implementation today is
// incus-host/scripts/deploy.sh — this package is where it lands, ported
// faithfully rather than redesigned, once the ingress reconciler (this
// repo's first real capability) has proven out the project's structure.
package bootstrap

import "errors"

// ErrNotImplemented is returned until deploy.sh's logic is ported here.
var ErrNotImplemented = errors.New("bootstrap: not yet implemented — see incus-host/scripts/deploy.sh for the working version")

// Run converges the current host to its declared platform state.
func Run() error {
	return ErrNotImplemented
}
