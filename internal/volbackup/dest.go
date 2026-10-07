package volbackup

import (
	"fmt"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// connectRemote opens a named remote from the Incus client configuration of the user running tink (see
// incusapi.ConnectRemote). A variable so tests can stand in for the network.
var connectRemote = incusapi.ConnectRemote

// pool is the pool on the target that holds restore points. A remote target may leave it out: "default".
func (t Target) pool() string {
	if t.Pool == "" && t.Remote != "" {
		return "default"
	}
	return t.Pool
}

// transferMode is how a volume travels to or from the target. A remote is always RELAYED through
// tink: the one thing tink knows is reachable from both ends is tink itself. Pull needs the other
// server to be able to open a connection back to this one, and push needs this one to reach the
// other's own advertised address; either breaks behind NAT or over a tunnel. (Restricted Incus
// projects refuse pull outright.) Within one server there is nothing to choose.
func (t Target) transferMode() string {
	if t.Remote != "" {
		return "relay"
	}
	return ""
}

// dest returns the server, scoped to the right project, that holds this target's restore points.
// For a pool target that is the volume's own server and project; for a remote it is the remote, in the
// project configured for it in the Incus client configuration.
func (t Target) dest(local incus.InstanceServer, v Volume) (incus.InstanceServer, error) {
	if t.Remote == "" {
		return v.scoped(local), nil
	}
	d, err := connectRemote(t.Remote)
	if err != nil {
		return nil, fmt.Errorf("target %q: remote %q: %w", t.Name, t.Remote, err)
	}
	return d, nil
}

// where describes the target for messages: "nas" pool, or the remote and pool.
func (t Target) where() string {
	if t.Remote != "" {
		return t.Remote + ":" + t.pool()
	}
	return t.Pool
}
