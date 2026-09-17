package bootstrap

import (
	"fmt"
	"os/exec"
	"time"
)

// recreateInstance unconditionally deletes (if present) and relaunches an
// instance from image with the given profiles -- matching deploy.sh
// exactly: there is no "skip if nothing changed" check here today, so
// every apply run disrupts incus-ui/authelia/ingress, not just the first
// bootstrap. That's a real, existing behavior this port preserves as-is,
// not something introduced by porting it to Go.
func recreateInstance(r *runner, name, image string, profiles ...string) error {
	existing, err := incusListNames("instance")
	if err != nil {
		return err
	}

	if existing[name] {
		if _, err := r.run(fmt.Sprintf("deleted existing %s", name), "incus", "delete", name, "--force"); err != nil {
			return err
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

func filePush(r *runner, description, createDirs, src, dest string) error {
	args := []string{"file", "push"}
	if createDirs != "" {
		args = append(args, "--create-dirs")
	}
	args = append(args, src, dest)
	_, err := r.run(description, "incus", args...)
	return err
}

func applyIncusUI(r *runner, opts Options) error {
	_, registryPath := registryHostAndPath(opts.Config.ImageRegistry)
	image := fmt.Sprintf("incus-ui-oci:%sincus-ui:latest", registryPath)
	return recreateInstance(r, "incus-ui", image, "default", "incus-ui")
}

func applyAuthelia(r *runner, opts Options) error {
	if err := recreateInstance(r, "authelia", "docker-oci:authelia/authelia:latest", "default", "authelia"); err != nil {
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

	_, err := r.run("restarted authelia (first boot almost always beats the config being there)", "incus", "restart", "authelia")
	return err
}

func applyIngress(r *runner, opts Options) error {
	if err := recreateInstance(r, "ingress", "docker-oci:caddy:2.11.4", "default", "ingress"); err != nil {
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

	_, err := r.run("restarted ingress (picks up the Caddyfile + routes just pushed)", "incus", "restart", "ingress")
	return err
}
