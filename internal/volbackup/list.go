package volbackup

import (
	"fmt"
	"sort"

	incus "github.com/lxc/incus/v7/client"
)

// ListedVolume is a custom volume found by listing the server, with the config it carries now.
type ListedVolume struct {
	Volume Volume
	Config map[string]string
}

// ListVolumes lists every custom volume on the server, in every project and every pool, with its config: what a
// scheduler needs to find the volumes that carry a copy policy. One call per pool covers every project, whichever
// project the client is scoped to.
//
// A pool that cannot be listed (an unreachable NAS, say) is reported in poolErrs, keyed by the pool's name, and the
// others are still listed: one broken pool must not stop the copies of volumes on the rest. err is for what stops the
// whole listing (the pools themselves cannot be listed).
func ListVolumes(s incus.InstanceServer) (vols []ListedVolume, poolErrs map[string]error, err error) {
	pools, err := s.GetStoragePoolNames()
	if err != nil {
		return nil, nil, fmt.Errorf("listing storage pools: %w", err)
	}
	sort.Strings(pools)
	poolErrs = map[string]error{}
	for _, pool := range pools {
		all, err := s.GetStoragePoolVolumesAllProjects(pool)
		if err != nil {
			poolErrs[pool] = err
			continue
		}
		for _, v := range all {
			if v.Type != "custom" {
				continue
			}
			cfg := v.Config
			if cfg == nil {
				cfg = map[string]string{}
			}
			vols = append(vols, ListedVolume{Volume: Volume{Project: v.Project, Pool: pool, Name: v.Name}, Config: cfg})
		}
	}
	return vols, poolErrs, nil
}
