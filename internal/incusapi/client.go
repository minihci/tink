// Package incusapi provides a thin wrapper around Incus's own Go client
// library, shared by every tink capability that needs to talk to an Incus
// daemon: over the local unix socket by default, or, with UseRemote, over
// the network API to a remote from the Incus client configuration.
package incusapi

import (
	"errors"
	"fmt"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusconf"
)

// DefaultSocket is the local Incus daemon socket every tink capability
// talks to unless a remote is chosen.
const DefaultSocket = "/var/lib/incus/unix.socket"

// remote is the Incus remote every Connect goes to, set once at startup by the CLI. It is process-wide state on
// purpose: the choice of server is made once per invocation, by a flag or $TINK_REMOTE, and the alternative is
// threading a value through every options struct in every package.
var remote string

// UseRemote makes every later Connect go to the named remote of the Incus client configuration. An empty name,
// or the client's built-in "local", means the local daemon again.
func UseRemote(name string) {
	if name == "local" {
		name = ""
	}
	remote = name
}

// Remote is the remote in use, or "" for the local daemon.
func Remote() string { return remote }

// IsRemote reports whether tink is pointed at a server other than the local daemon. Anything that works on
// the host's own filesystem or processes must refuse when it is true.
func IsRemote() bool { return remote != "" }

// wantsRemote decides whether a Connect with socketPath goes to the remote. The default socket path is not an
// explicit choice (some commands fill it in as their flag default); any other path alongside a remote is a
// contradiction and an error.
func wantsRemote(socketPath string) (bool, error) {
	if remote == "" {
		return false, nil
	}
	if socketPath != "" && socketPath != DefaultSocket {
		return false, fmt.Errorf("--socket %s and --remote %s contradict each other: choose one", socketPath, remote)
	}
	return true, nil
}

// Connect opens a connection to Incus: the remote chosen with UseRemote, or else the local daemon over its unix
// socket. An empty path uses Incus's own default resolution ($INCUS_SOCKET, then $INCUS_DIR/unix.socket, then
// DefaultSocket).
func Connect(socketPath string) (incus.InstanceServer, error) {
	useRemote, err := wantsRemote(socketPath)
	if err != nil {
		return nil, err
	}
	if useRemote {
		return ConnectRemote(remote)
	}
	return incus.ConnectIncusUnix(socketPath, nil)
}

// remoteAdvice replaces the usual advice for a remote that is not configured, in a process where `incus remote add` is not how it is fixed.
var remoteAdvice string

// SetRemoteAdvice says what to do about a remote that is not configured, when the usual answer (`incus remote add`) does not apply to
// this process: the helper container has no incus CLI. advice is a format with one %s, the remote's name.
func SetRemoteAdvice(advice string) { remoteAdvice = advice }

// RemoteNotConfiguredError is what ConnectRemote returns for a name the client configuration does not have. It is a type so a caller that
// knows more about the remote than the name (a stack that declares its address) can add that to the advice without reading the message.
type RemoteNotConfiguredError struct {
	Name   string
	Advice string
}

func (e *RemoteNotConfiguredError) Error() string {
	return fmt.Sprintf("no Incus remote %q is configured for the user running tink (%s)", e.Name, e.Advice)
}

// ConnectRemote opens the named remote from the Incus client configuration of the user running tink (the
// Incus client's own configuration directory, which $INCUS_CONF overrides; root's under sudo). That is where
// `incus remote add` keeps the address, the client certificate and the project, so tink stores no credentials
// of its own.
func ConnectRemote(name string) (incus.InstanceServer, error) {
	conf, err := incusconf.Load()
	if err != nil {
		return nil, err
	}
	r, ok := conf.Remotes[name]
	if !ok {
		advice := ""
		if remoteAdvice != "" {
			advice = fmt.Sprintf(remoteAdvice, name)
		} else {
			advice = fmt.Sprintf("add it with `incus remote add`; sudo uses root's configuration, in %s", conf.ConfigPath())
		}
		return nil, &RemoteNotConfiguredError{Name: name, Advice: advice}
	}
	if r.Public || r.Protocol != "incus" {
		return nil, errors.New("remote " + name + " is an image server, not an Incus server to manage")
	}
	return conf.GetInstanceServer(name)
}
