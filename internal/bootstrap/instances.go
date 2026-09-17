package bootstrap

import (
	"fmt"
	"os/exec"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/minihci/tink/internal/incusapi"
)

// recreateInstance unconditionally deletes (if present) and relaunches an
// instance from image with the given profiles -- matching deploy.sh
// exactly: there is no "skip if nothing changed" check here today, so
// every deploy run disrupts incus-ui/authelia/ingress, not just the first
// bootstrap. That's a real, existing behavior this port preserves as-is,
// not something introduced by porting it to Go.
//
// Existence-check/stop/delete use the real Incus Go client (verified
// against the actual interface definitions, not guessed) -- these are
// clean, single-purpose calls. Launch stays a CLI call deliberately:
// resolving an OCI image reference like "docker-oci:caddy:2.11.4" means
// connecting to that named remote as an ImageServer and resolving the
// image, none of which the daemon's own API models (remotes are a
// client-config concept, not a server one) -- reimplementing that
// resolution logic to satisfy "use the client everywhere" would mean
// under-verified new code standing between "deploy" and a live instance
// launch, which is a worse trade than one exec call to something already
// proven correct.
func recreateInstance(r *runner, server incus.InstanceServer, name, image string, profiles ...string) error {
	inst, _, err := server.GetInstance(name)
	exists := err == nil

	if exists {
		if r.dryRun {
			r.note("would stop and delete existing %s", name)
		} else {
			if inst.Status == "Running" {
				stopOp, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Force: true, Timeout: 30}, "")
				if err != nil {
					return fmt.Errorf("stopping %s: %w", name, err)
				}
				if err := stopOp.Wait(); err != nil {
					return fmt.Errorf("waiting for %s to stop: %w", name, err)
				}
			}
			deleteOp, err := server.DeleteInstance(name)
			if err != nil {
				return fmt.Errorf("deleting %s: %w", name, err)
			}
			if err := deleteOp.Wait(); err != nil {
				return fmt.Errorf("waiting for %s to delete: %w", name, err)
			}
			r.note("deleted existing %s", name)
		}
	}

	args := []string{"launch", image, name}
	for _, p := range profiles {
		args = append(args, "--profile", p)
	}
	_, err = r.run(fmt.Sprintf("launched %s from %s", name, image), "incus", args...)
	return err
}

// waitForDir polls `incus exec <instance> -- test -d <dir>` until it
// succeeds or 20 seconds pass, mirroring deploy.sh's own wait loop for a
// freshly launched container's filesystem to actually be there. In
// dry-run mode this just records the intent -- the instance was never
// actually launched, so there's nothing to poll yet.
func waitForDir(r *runner, instance, dir string) error {
	if r.dryRun {
		r.note("would wait for %s to appear in %s", dir, instance)
		return nil
	}
	for i := 1; i <= 20; i++ {
		if err := exec.Command("incus", "exec", instance, "--", "test", "-d", dir).Run(); err == nil {
			r.note("%s appeared in %s", dir, instance)
			return nil
		}
		if i == 20 {
			return fmt.Errorf("%s never appeared in %s -- aborting", dir, instance)
		}
		time.Sleep(time.Second)
	}
	return nil
}

// filePush retries on failure -- confirmed live that `test -d <dir>`
// succeeding (waitForDir's readiness signal) does not guarantee a
// freshly launched container is actually ready to receive a file push
// yet. See runner.runWithRetry for why this is a real, confirmed race,
// not speculative hardening.
func filePush(r *runner, description, createDirs, src, dest string) error {
	args := []string{"file", "push"}
	if createDirs != "" {
		args = append(args, "--create-dirs")
	}
	args = append(args, src, dest)
	return r.runWithRetry(description, "incus", args...)
}

// ensureRunning picks the state-appropriate action to make sure name ends
// up running with the config just pushed into it -- confirmed live that
// deploy.sh's blind `incus restart` is a real logic gap, not just a
// timing race: an app container with nothing at its config path yet
// (true on every first boot, before this step pushes it) can crash to
// Stopped before this step even runs, and restart itself requires an
// instance to already be running. Stopped gets a start (reads the
// now-present config fresh); Running gets an actual restart (needed to
// force it to re-read the just-pushed config).
//
// Also confirmed live: Incus's own daemon-side operation queue can still
// reject either action ("instance is busy running a stop operation") if
// the container is mid-transition -- e.g. autorestart already kicked in
// concurrently. That part genuinely is transient, so each retry re-reads
// current state and re-picks the action fresh, rather than blindly
// retrying a decision that may no longer match reality.
func ensureRunning(r *runner, server incus.InstanceServer, name string) error {
	if r.dryRun {
		r.note("would ensure %s is running with its newly pushed config", name)
		return nil
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		action, err := ensureRunningOnce(server, name)
		if err == nil {
			r.note("%sed %s (picks up the config just pushed)", action, name)
			return nil
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return fmt.Errorf("ensuring %s is running: giving up after %d attempts: %w", name, maxAttempts, lastErr)
}

func ensureRunningOnce(server incus.InstanceServer, name string) (action string, err error) {
	inst, _, err := server.GetInstance(name)
	if err != nil {
		return "", fmt.Errorf("checking state: %w", err)
	}

	action = restartAction(inst.Status)

	op, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Timeout: 30}, "")
	if err != nil {
		return action, fmt.Errorf("%sing: %w", action, err)
	}
	if err := op.Wait(); err != nil {
		return action, fmt.Errorf("waiting to %s: %w", action, err)
	}
	return action, nil
}

// restartAction picks "start" for anything not already Running (a
// stopped/crashed instance can't be restarted, only started) and
// "restart" otherwise, where an actual stop+start cycle is needed to make
// a live instance re-read config that was just pushed into it.
func restartAction(currentStatus string) string {
	if currentStatus != "Running" {
		return "start"
	}
	return "restart"
}

func applyIncusUI(r *runner, opts Options) error {
	server, err := incusapi.Connect(opts.socket())
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	_, registryPath := registryHostAndPath(opts.Config.ImageRegistry)
	image := fmt.Sprintf("incus-ui-oci:%sincus-ui:latest", registryPath)
	return recreateInstance(r, server, "incus-ui", image, "default", "incus-ui")
}

func applyAuthelia(r *runner, opts Options) error {
	server, err := incusapi.Connect(opts.socket())
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	if err := recreateInstance(r, server, "authelia", "docker-oci:authelia/authelia:latest", "default", "authelia"); err != nil {
		return err
	}
	if err := waitForDir(r, "authelia", "/config"); err != nil {
		return err
	}

	root := opts.RepoRoot
	pushes := []struct {
		src, dest  string
		createDirs bool
	}{
		{root + "/authelia/configuration.yml", "authelia/config/configuration.yml", false},
		{root + "/authelia/users_database.yml", "authelia/config/users_database.yml", false},
		{root + "/secrets/reset_password_jwt_secret", "authelia/config/secrets/reset_password_jwt_secret", true},
		{root + "/secrets/session_secret", "authelia/config/secrets/session_secret", false},
		{root + "/secrets/storage_encryption_key", "authelia/config/secrets/storage_encryption_key", false},
		{root + "/secrets/oidc_hmac_secret", "authelia/config/secrets/oidc_hmac_secret", false},
		{root + "/secrets/oidc.key", "authelia/config/secrets/oidc.key", false},
	}
	for _, p := range pushes {
		createDirs := ""
		if p.createDirs {
			createDirs = "1"
		}
		if err := filePush(r, fmt.Sprintf("pushed %s", p.dest), createDirs, p.src, p.dest); err != nil {
			return err
		}
	}

	return ensureRunning(r, server, "authelia")
}

func applyIngress(r *runner, opts Options) error {
	server, err := incusapi.Connect(opts.socket())
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	if err := recreateInstance(r, server, "ingress", "docker-oci:caddy:2.11.4", "default", "ingress"); err != nil {
		return err
	}
	if err := waitForDir(r, "ingress", "/etc/caddy"); err != nil {
		return err
	}

	root := opts.RepoRoot
	if err := filePush(r, "pushed ingress Caddyfile", "", root+"/ingress/Caddyfile", "ingress/etc/caddy/Caddyfile"); err != nil {
		return err
	}
	if err := filePush(r, "pushed ingress/routes/incus-ui.caddy", "1", root+"/ingress/routes/incus-ui.caddy", "ingress/etc/caddy/routes/incus-ui.caddy"); err != nil {
		return err
	}
	if err := filePush(r, "pushed ingress/routes/auth.caddy", "", root+"/ingress/routes/auth.caddy", "ingress/etc/caddy/routes/auth.caddy"); err != nil {
		return err
	}

	return ensureRunning(r, server, "ingress")
}
