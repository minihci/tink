package bootstrap

import (
	"fmt"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/minihci/tink/internal/incusapi"
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

func applyStorageVolumes(r *runner, opts Options) error {
	server, err := incusapi.Connect(opts.socket())
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}

	existingNames, err := server.GetStoragePoolVolumeNames(opts.Config.StoragePool)
	if err != nil {
		return fmt.Errorf("listing storage volumes in %s: %w", opts.Config.StoragePool, err)
	}
	existing := map[string]bool{}
	for _, name := range existingNames {
		existing[name] = true
	}

	for _, vol := range requiredVolumes {
		if existing[vol] {
			r.note("storage volume %s already exists", vol)
			continue
		}
		if r.dryRun {
			r.note("would create storage volume %s", vol)
			continue
		}
		if err := server.CreateStoragePoolVolume(opts.Config.StoragePool, api.StorageVolumesPost{
			Name:        vol,
			Type:        "custom",
			ContentType: "filesystem",
		}); err != nil {
			return fmt.Errorf("creating storage volume %s: %w", vol, err)
		}
		r.note("created storage volume %s", vol)
	}
	return nil
}
