// Package ingress implements tink's ingress self-registration reconciler:
// discovering instances that opt in via user.ingress.{domain,port,enabled}
// config and rendering/applying the shared ingress instance's routes.
//
// Not yet ported. The working implementation today is
// incus-host/reconciler/reconcile.sh and its DESIGN.md — this is the first
// capability slated to move here, since it's simpler than bootstrap (no
// secrets, no sequencing) and already fully specified.
package ingress

import "errors"

// ErrNotImplemented is returned until reconcile.sh's logic is ported here.
var ErrNotImplemented = errors.New("ingress: not yet implemented — see incus-host/reconciler/reconcile.sh for the working version")

// Reconcile discovers registered instances and converges the shared
// ingress instance's routes to match.
func Reconcile() error {
	return ErrNotImplemented
}

// Status reports what's currently registered, without changing anything.
func Status() error {
	return ErrNotImplemented
}
