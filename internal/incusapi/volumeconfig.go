package incusapi

import (
	"fmt"
	"net/http"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// volumeConfigAttempts is how many times MergeVolumeConfig reads and writes before it gives up on a volume that keeps changing under it.
const volumeConfigAttempts = 3

// MergeVolumeConfig sets keys on a custom storage volume's config and keeps every other key as it was.
//
// A volume has no PATCH (an instance does, see PatchInstanceConfig), so this reads the volume, adds the keys to the config it read
// and writes the whole thing back with the ETag it read: if anyone changed the volume in between, Incus refuses the write (412) and
// the read-and-write is done again from what is there now, a few times, instead of overwriting their change. The keys not named are
// carried through untouched, which is the point: the volume's other keys (a description, a backup policy, Incus's own volatile.*) are
// not this caller's to drop.
//
// It is what the helper publishes its status document with when the document lives on a volume: a volume has no devices, so the
// permission to edit one volume is not the permission to give an instance a device that reaches the host.
//
// project is the project the volume is in ("" for the connection's own).
func MergeVolumeConfig(s incus.InstanceServer, project, pool, name string, config map[string]string) error {
	if project != "" {
		s = s.UseProject(project)
	}
	var err error
	for attempt := 0; attempt < volumeConfigAttempts; attempt++ {
		var vol *api.StorageVolume
		var etag string
		if vol, etag, err = s.GetStoragePoolVolume(pool, "custom", name); err != nil {
			return fmt.Errorf("reading volume %s/%s: %w", pool, name, err)
		}
		put := vol.Writable()
		merged := make(map[string]string, len(put.Config)+len(config))
		for k, v := range put.Config {
			merged[k] = v
		}
		for k, v := range config {
			merged[k] = v
		}
		put.Config = merged
		if err = s.UpdateStoragePoolVolume(pool, "custom", name, put, etag); err == nil {
			return nil
		}
		if !api.StatusErrorCheck(err, http.StatusPreconditionFailed) {
			break
		}
	}
	return fmt.Errorf("updating the config of volume %s/%s: %w", pool, name, err)
}
