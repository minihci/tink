package incusapi

import (
	"bytes"
	"fmt"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// ExecInGuest runs command inside the named instance over Incus's own
// WebSocket exec (ExecInstance) -- the same primitive `incus exec` itself
// is built on. Confirmed by reading cmd/incus/exec.go directly rather
// than guessed from the API docs alone: op.Get().Metadata["return"] is
// exactly how the real CLI extracts the process's exit code once
// op.Wait() returns, and the DataDone channel below is exactly how it
// waits for buffered stdout/stderr to finish flushing before reading
// them back -- skip that wait and a fast-exiting command's output can
// still be arriving after Wait() already returned.
//
// Lives here, not in the package that first needed it (internal/resolve's
// kind: exec), because internal/ingress's own apply() needed the exact
// same thing for the exact same reason: reloading Caddy after a route
// change is also a guest-side command, and its own doc comment already
// named this WebSocket exec protocol as "scope beyond what this first
// port needs" rather than shelling out to the incus binary -- this is
// that scope, now needed for real by a second caller, so it belongs in
// incusapi (already the shared home for anything both packages need to
// do against a live daemon) rather than duplicated or awkwardly
// cross-imported between sibling packages.
func ExecInGuest(server incus.InstanceServer, instance string, command []string) (exitCode int, output string, err error) {
	var buf bytes.Buffer
	args := incus.InstanceExecArgs{
		Stdin:    bytes.NewReader(nil),
		Stdout:   &buf,
		Stderr:   &buf,
		DataDone: make(chan bool),
	}
	op, err := server.ExecInstance(instance, api.InstanceExecPost{
		Command:   command,
		WaitForWS: true,
	}, &args)
	if err != nil {
		return 0, "", fmt.Errorf("starting exec of %v on %s: %w", command, instance, err)
	}

	waitErr := op.Wait()
	<-args.DataDone

	if opAPI := op.Get(); opAPI.Metadata != nil {
		if code, ok := opAPI.Metadata["return"].(float64); ok {
			exitCode = int(code)
		}
	}
	if waitErr != nil {
		return exitCode, buf.String(), fmt.Errorf("running %v on %s: %w", command, instance, waitErr)
	}
	return exitCode, buf.String(), nil
}
