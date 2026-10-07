package volbackup

import (
	"fmt"
	"sort"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/resolve"
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

// Forget removes a volume's copy policy, so that nothing copies it any more. It reports whether there was one. The
// volume's data, its restore points on the targets and the stamps of what has happened are left exactly as they are:
// forgetting stops the schedule and nothing else. owner is the stack the volume points back at (its user.tink.stack),
// if any, so the caller can say whose YAML still has to change.
func Forget(s incus.InstanceServer, v Volume) (had bool, owner string, err error) {
	vol, etag, err := v.scoped(s).GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return false, "", fmt.Errorf("volume %s/%s: %w", v.pool(), v.Name, err)
	}
	owner = vol.Config[resolve.StackKey]
	if _, has := vol.Config[resolve.PolicyKey]; !has {
		return false, owner, nil
	}
	put := vol.Writable()
	delete(put.Config, resolve.PolicyKey)
	if err := v.scoped(s).UpdateStoragePoolVolume(v.pool(), "custom", v.Name, put, etag); err != nil {
		return false, owner, fmt.Errorf("clearing the copy policy of %s/%s: %w", v.pool(), v.Name, err)
	}
	return true, owner, nil
}
