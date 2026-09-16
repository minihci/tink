// Package incusapi provides a thin wrapper around Incus's own Go client
// library, shared by every tink capability that needs to talk to an Incus
// daemon — over the local unix socket today, and over the network API
// with a scoped TLS identity once that work (see incus-host's
// reconciler/DESIGN.md "Open question") is picked up for real.
package incusapi

// DefaultSocket is the local Incus daemon socket every tink capability
// talks to until a scoped network identity replaces it.
const DefaultSocket = "/var/lib/incus/unix.socket"

// TODO: wrap Incus's official Go client (see linuxcontainers.org/incus for
// the current module path) here once the first real capability (ingress
// reconcile) is ported — this stub exists so every capability shares one
// place to get a client from, instead of each hand-rolling its own
// connection the way reconcile.sh's curl+jq does today.
