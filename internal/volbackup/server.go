package volbackup

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	incus "github.com/lxc/incus/v7/client"
)

// serverName is the name of the Incus server s is connected to (its host name unless configured otherwise): the
// identity written into every restore point so that a server only ever prunes its own.
func serverName(s incus.InstanceServer) (string, error) {
	srv, _, err := s.GetServer()
	if err != nil {
		return "", fmt.Errorf("reading the server's name: %w", err)
	}
	if srv.Environment.ServerName == "" {
		return "", errors.New("the server reports no name, so restore points cannot say which server made them")
	}
	return srv.Environment.ServerName, nil
}

// splitByServer separates the restore points `me` made (or that carry no server marker, made before the marker
// existed: those are treated as this server's) from those another server made. The second result is the names of
// the other servers, sorted.
func splitByServer(points []RestorePoint, me string) (own []RestorePoint, others []string) {
	seen := map[string]bool{}
	for _, p := range points {
		if p.Server == "" || p.Server == me {
			own = append(own, p)
			continue
		}
		if !seen[p.Server] {
			seen[p.Server] = true
			others = append(others, p.Server)
		}
	}
	sort.Strings(others)
	return own, others
}

// ErrBusy is returned by Copy when the same copy (the same volume to the same target) is already running in this
// process. It is not a failure of the copy: nothing was tried.
var ErrBusy = errors.New("that copy is already running")

var running sync.Map // copy key -> struct{}

func copyKey(v Volume, t Target) string {
	project := v.Project
	if project == "" {
		project = "default"
	}
	return project + "/" + v.pool() + "/" + v.Name + " -> " + t.Name
}
