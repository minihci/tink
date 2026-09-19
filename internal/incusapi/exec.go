package incusapi

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// agentOfflineMessage is the exact text of Incus's own internal
// errQemuAgentOffline sentinel (internal/server/instance/drivers/
// driver_qemu.go), confirmed by reading that source directly. It never
// gets a distinct error type or HTTP status code on the way out to a
// client -- cmd/incusd/instance_exec.go just returns it as a plain error
// -- so a substring match on err.Error() is the only signal a client
// actually has. Fragile against Incus ever changing this exact wording;
// the honest cost of there being no better signal available at all.
const agentOfflineMessage = "VM agent isn't currently running"

// DefaultAgentRetryAttempts/DefaultAgentRetryDelay use the same
// fixed-attempts-plus-sleep shape as run.go's own EnsureRunning, for the
// same reason -- a known, specific, transient daemon condition, not a
// general retry-everything policy -- but not its actual numbers:
// EnsureRunning's 5 attempts at 1 second each were tuned for a genuinely
// short-lived API rejection ("instance is busy"), a different
// phenomenon on a different timescale than a VM finishing its boot.
// First cut here copied that budget anyway and it wasn't enough: a live
// restart-and-race test against this platform's own aic8800 sandbox VM
// timed out at 5 seconds with the agent still not up, then a plain timed
// poll loop measured the real number -- 11 seconds. 30 attempts at 1
// second gives comfortable margin over that measurement rather than a
// second guess.
//
// Exported, not just a bigger constant, because that margin is a
// property of *this* VM's measured boot time, not a universal one -- a
// heavier guest could plausibly need longer, and the caller who'd know
// that (whoever is writing the tink.yaml that names this specific
// instance) shouldn't be gated behind a recompile to say so. See
// ExecInGuestWithRetry.
const (
	DefaultAgentRetryAttempts = 30
	DefaultAgentRetryDelay    = time.Second
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
//
// Retries internally, briefly, on one specific condition: a VM (never a
// container -- containers attach directly into host-visible namespaces,
// no agent involved) can be Running by Incus's own account while the
// incus-agent inside hasn't finished booting yet. Hit twice live in one
// session getting kind: exec working at all (once restarting the
// aic8800 sandbox after a secureboot toggle, again implicitly via the
// same class of race) -- twice is a real, repeated cost, not a
// hypothetical one, so this is fixed here, once, for every caller,
// rather than left for each call site to remember to guard against.
// Every other exec failure (a genuinely broken command, wrong path, a
// real connectivity problem) still returns immediately, unretried --
// this is deliberately narrow, not a blanket "retry anything that
// fails" policy.
func ExecInGuest(server incus.InstanceServer, instance string, command []string) (exitCode int, output string, err error) {
	return ExecInGuestWithRetry(server, instance, command, DefaultAgentRetryAttempts, DefaultAgentRetryDelay)
}

// ExecInGuestWithRetry is ExecInGuest with the agent-boot retry budget
// overridable instead of fixed at the package defaults -- see those
// constants' own doc comment for why a caller might need this rather
// than always taking the default.
func ExecInGuestWithRetry(server incus.InstanceServer, instance string, command []string, attempts int, delay time.Duration) (exitCode int, output string, err error) {
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		exitCode, output, err = execInGuestOnce(server, instance, command)
		if err == nil || !strings.Contains(err.Error(), agentOfflineMessage) {
			return exitCode, output, err
		}
		lastErr = err
		time.Sleep(delay)
	}
	return 0, "", fmt.Errorf("waiting for %s's VM agent to come up: giving up after %d attempts: %w", instance, attempts, lastErr)
}

func execInGuestOnce(server incus.InstanceServer, instance string, command []string) (exitCode int, output string, err error) {
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

	if opAPI := op.Get(); opAPI.Metadata != nil {
		if code, ok := opAPI.Metadata["return"].(float64); ok {
			exitCode = int(code)
		}
	}
	if waitErr != nil {
		// No DataDone wait here, deliberately: matches cmd/incus/exec.go's
		// own ordering exactly (confirmed by reading it, not assumed) --
		// DataDone only ever closes once the exec's data channels actually
		// connected, which never happens if the exec never started in the
		// first place (the VM agent offline case this function's caller
		// retries on is exactly that: op.Wait() fails before any stream
		// exists at all). Waiting on it unconditionally hangs forever on
		// exactly that failure -- caught live, the hard way, retrying this
		// same condition for the first time against a real dead agent.
		return exitCode, buf.String(), fmt.Errorf("running %v on %s: %w", command, instance, waitErr)
	}
	<-args.DataDone
	return exitCode, buf.String(), nil
}
