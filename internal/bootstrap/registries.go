package bootstrap

import (
	"fmt"
	"strings"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

// registryHostAndPath splits IMAGE_REGISTRY the same way deploy.sh's own
// ${VAR%%/*} / ${VAR#*/} parameter expansions do: everything before the
// first "/" is the host, everything after (if any) is the path prefix.
func registryHostAndPath(imageRegistry string) (host, path string) {
	host, rest, found := strings.Cut(imageRegistry, "/")
	if !found {
		return host, ""
	}
	return host, rest + "/"
}

// currentRemoteURL and remoteExists (below) read the client's own
// ~/.config/incus/config.yml directly via cliconfig.LoadConfig, the same
// file `incus remote add/list` themselves read and write -- a real Go
// API for this exists, unlike the daemon-side objects this whole
// package increasingly goes through directly, since remotes are client
// config, not something incusd's HTTP API exposes at all. Reads only:
// adding/removing a remote (applyRegistries, just below) still shells
// out, since replicating that safely (cert handling for TLS-secured
// protocols, safe config-file writes) is a materially bigger, separate
// piece of work than swapping a read.
func currentRemoteURL(name string) (string, error) {
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		return "", fmt.Errorf("loading incus client config: %w", err)
	}
	remote, ok := conf.Remotes[name]
	if !ok || len(remote.Addrs) == 0 {
		return "", nil
	}
	return remote.Addrs[0], nil
}

func remoteExists(name string) (bool, error) {
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		return false, fmt.Errorf("loading incus client config: %w", err)
	}
	_, ok := conf.Remotes[name]
	return ok, nil
}

// applyRegistries ensures the docker-oci remote exists and that
// incus-ui-oci points at IMAGE_REGISTRY's host -- replacing it outright
// if it's pointed somewhere else, since keying on name alone would
// silently keep a stale registry across a deploy.env change (this bit
// incus-host for real once already, see registries.go's git history).
func applyRegistries(r *runner, opts Options) error {
	exists, err := remoteExists("docker-oci")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := r.run("added docker-oci remote", "incus", "remote", "add", "docker-oci", "https://docker.io", "--protocol", "oci"); err != nil {
			return err
		}
	} else {
		r.note("docker-oci remote already exists")
	}

	host, _ := registryHostAndPath(opts.Config.ImageRegistry)
	desiredURL := "https://" + host

	currentURL, err := currentRemoteURL("incus-ui-oci")
	if err != nil {
		return err
	}

	if currentURL == desiredURL {
		r.note("incus-ui-oci remote already points at %s", desiredURL)
		return nil
	}

	if currentURL != "" {
		if _, err := r.run(fmt.Sprintf("removed stale incus-ui-oci remote (was %s)", currentURL), "incus", "remote", "remove", "incus-ui-oci"); err != nil {
			return err
		}
	}

	args := []string{"remote", "add", "incus-ui-oci", desiredURL, "--protocol", "oci"}
	if opts.Config.ImageRegistryToken != "" {
		args = append(args, "--token", opts.Config.ImageRegistryToken)
	}
	_, err = r.run(fmt.Sprintf("added incus-ui-oci remote (%s)", desiredURL), "incus", args...)
	return err
}
