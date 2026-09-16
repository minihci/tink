// Package incusapi provides a thin wrapper around Incus's own Go client
// library, shared by every tink capability that needs to talk to an Incus
// daemon -- over the local unix socket today, and over the network API
// with a scoped TLS identity once that work (see incus-host's
// reconciler/DESIGN.md "Open question") is picked up for real.
package incusapi

import (
	incus "github.com/lxc/incus/v7/client"
)

// DefaultSocket is the local Incus daemon socket every tink capability
// talks to until a scoped network identity replaces it.
const DefaultSocket = "/var/lib/incus/unix.socket"

// Connect opens a connection to the local Incus daemon over its unix
// socket. An empty path uses Incus's own default resolution
// ($INCUS_SOCKET, then $INCUS_DIR/unix.socket, then DefaultSocket).
func Connect(socketPath string) (incus.InstanceServer, error) {
	return incus.ConnectIncusUnix(socketPath, nil)
}
