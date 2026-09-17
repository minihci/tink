package bootstrap

import (
	"encoding/csv"
	"fmt"
	"os/exec"
	"strings"
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

func currentRemoteURL(name string) (string, error) {
	out, err := exec.Command("incus", "remote", "list", "-f", "csv").Output()
	if err != nil {
		return "", fmt.Errorf("incus remote list: %w", err)
	}
	records, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil {
		return "", fmt.Errorf("parsing incus remote list output: %w", err)
	}
	for _, rec := range records {
		if len(rec) >= 2 && rec[0] == name {
			return rec[1], nil
		}
	}
	return "", nil
}

// applyRegistries ensures the docker-oci remote exists and that
// incus-ui-oci points at IMAGE_REGISTRY's host -- replacing it outright
// if it's pointed somewhere else, since keying on name alone would
// silently keep a stale registry across a deploy.env change (this bit
// incus-host for real once already, see registries.go's git history).
func applyRegistries(r *runner, opts Options) error {
	remotes, err := incusListNames("remote")
	if err != nil {
		return err
	}
	if !remotes["docker-oci"] {
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
