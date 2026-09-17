package bootstrap

import (
	"fmt"
	"os/exec"
	"strings"
)

// requiredVolumes are created once and never recreated by apply -- their
// data (Caddy's ACME state, Authelia's config+db, ingress's routes) must
// survive every instance recreation.
var requiredVolumes = []string{
	"incus-ui-caddy-data",
	"authelia-config",
	"ingress-caddy-data",
	"ingress-routes",
}

func existingVolumes(pool string) (map[string]bool, error) {
	out, err := exec.Command("incus", "storage", "volume", "list", pool, "-f", "csv", "-c", "n").Output()
	if err != nil {
		return nil, fmt.Errorf("incus storage volume list %s: %w", pool, err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names[line] = true
		}
	}
	return names, nil
}

func applyStorageVolumes(r *runner, opts Options) error {
	existing, err := existingVolumes(opts.Config.StoragePool)
	if err != nil {
		return err
	}

	for _, vol := range requiredVolumes {
		if existing[vol] {
			r.note("storage volume %s already exists", vol)
			continue
		}
		if _, err := r.run(fmt.Sprintf("created storage volume %s", vol), "incus", "storage", "volume", "create", opts.Config.StoragePool, vol); err != nil {
			return err
		}
	}
	return nil
}
