package helper

import (
	"fmt"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// The status volume.
//
// The helper's status document used to be a config key on the helper's own instance. The permission that lets the helper write it
// (edit that instance) also lets it change the instance's devices, and a measured lab showed that is a route to root: the helper could
// give itself a proxy device to the Incus unix socket. So the credential the helper runs under must hold no edit right on any
// instance, and the document moves to a config key on a custom storage volume, which has no devices.
//
// Three facts hold the arrangement together:
//
//   - The helper's instance, which the admin creates at install and the helper never writes, carries a POINTER, StatusVolumeKey, beside
//     the marker: "POOL/VOLUME". The volume is in the instance's own project.
//   - The helper (`tink daemon run --status-volume`) writes StatusKey on that volume: the same document, with the same change-plus-
//     heartbeat rule, in the same key name.
//   - Find reads it back and puts it in Found.Config[StatusKey], so everything downstream (Evaluate, `tink helper status`, `kind: helper`
//     planning, plan's notes) is unchanged: it does not know where the document was read from.
//
// A helper installed before this (--status-instance, no pointer) is read exactly as it was; nothing is migrated. Installing with
// --status-on-instance makes the old shape on purpose.

const (
	// StatusVolumeKey is the instance config key that points at the volume holding the helper's status document, as "POOL/VOLUME". It is
	// written by `helper install` and read by Find; the helper itself never writes it.
	StatusVolumeKey = MarkerKey + ".status-volume"
	// statusVolumeSuffix names the volume install makes: "<instance name>-status".
	statusVolumeSuffix = "-status"
)

// StatusVolumeName is the name of the status volume `helper install` makes for the helper instance called instance.
func StatusVolumeName(instance string) string { return instance + statusVolumeSuffix }

// StatusVolumeRef is the pointer's value: the pool and the name of the volume.
func StatusVolumeRef(pool, volume string) string { return pool + "/" + volume }

// ParseStatusVolume reads a pointer back into the pool and the volume. A volume name cannot contain a slash in Incus, so the first one
// divides them; a pointer that is not two non-empty parts is an error.
func ParseStatusVolume(ref string) (pool, volume string, err error) {
	pool, volume, ok := strings.Cut(ref, "/")
	if !ok || pool == "" || volume == "" || strings.Contains(volume, "/") {
		return "", "", fmt.Errorf("%q is not POOL/VOLUME", ref)
	}
	return pool, volume, nil
}

// withStatusVolume completes a helper listed by Find from its status volume, if its instance points at one. The volume is then the
// only place the document is read from: a status key left on the instance (an old shape, edited by hand) is dropped, because a stale
// document must never stand in for one that could not be read. A volume that cannot be read does not fail the listing; the problem is
// carried on f for Evaluate to say, since "healthy" and "no status yet" would both be untrue.
//
// inst.Config is not changed: f gets a copy.
func withStatusVolume(s incus.InstanceServer, f *Found) {
	ref, ok := f.Config[StatusVolumeKey]
	if !ok {
		return
	}
	cfg := make(map[string]string, len(f.Config))
	for k, v := range f.Config {
		if k != StatusKey {
			cfg[k] = v
		}
	}
	f.Config, f.StatusVolume = cfg, ref

	pool, volume, err := ParseStatusVolume(ref)
	if err != nil {
		f.StatusProblem = err.Error()
		return
	}
	vol, _, err := s.UseProject(f.Project).GetStoragePoolVolume(pool, "custom", volume)
	if err != nil {
		f.StatusProblem = err.Error()
		return
	}
	if text, has := vol.Config[StatusKey]; has {
		cfg[StatusKey] = text
	}
}

// ensureStatusVolume makes the volume the helper will publish its status document on, when the helper is to have one: a new
// instance in the default shape, or an existing instance that already points at one (a volume that was deleted is made again). An
// existing instance with no pointer is left as it is, publishing on itself: install does not migrate. The volume is a filesystem
// volume with no config of its own, and in particular no backup policy: it holds one small document that the next heartbeat rewrites.
//
// existing is the helper instance if there already is one in the project, else nil.
func (in *Installer) ensureStatusVolume(scoped incus.InstanceServer, opts InstallOptions, existing *api.Instance) error {
	var pool, volume string
	switch {
	case existing != nil:
		ref, has := existing.Config[StatusVolumeKey]
		if !has {
			if !opts.StatusOnInstance {
				in.say("this helper publishes its status document on its own instance (it was installed before the status volume): it is left as it is, and nothing is migrated")
			}
			return nil
		}
		var err error
		if pool, volume, err = ParseStatusVolume(ref); err != nil {
			in.say("the helper's %s is %v, so no status volume is made", StatusVolumeKey, err)
			return nil
		}
	case opts.StatusOnInstance:
		return nil
	default:
		pool, volume = opts.Pool, StatusVolumeName(opts.Name)
	}

	_, _, found, err := incusapi.LookupVolume(scoped, pool, "custom", volume)
	if err != nil {
		return fmt.Errorf("checking whether the status volume %s/%s exists: %w", pool, volume, err)
	}
	if found {
		return nil
	}
	in.say("creating the status volume %s/%s (the helper publishes its status document there, so it needs no right to edit an instance)", pool, volume)
	if err := scoped.CreateStoragePoolVolume(pool, api.StorageVolumesPost{Name: volume, Type: "custom", ContentType: "filesystem"}); err != nil {
		return fmt.Errorf("creating the status volume %s/%s: %w", pool, volume, err)
	}
	return nil
}

// statusArgs are the daemon arguments that say where it publishes its status document: on a volume by default, on the helper's own
// instance with StatusOnInstance (the shape of every helper installed before the status volume).
func statusArgs(o InstallOptions) []string {
	if o.StatusOnInstance {
		return []string{"--status-instance", o.Name, "--status-project", o.Project}
	}
	return []string{"--status-volume", StatusVolumeName(o.Name), "--status-pool", o.Pool, "--status-project", o.Project}
}

// addStatusPointer records on the helper instance's config where its status volume is, unless the document is to be on the instance.
func addStatusPointer(cfg map[string]string, o InstallOptions) {
	if !o.StatusOnInstance {
		cfg[StatusVolumeKey] = StatusVolumeRef(o.Pool, StatusVolumeName(o.Name))
	}
}

// purgeStatusVolume deletes the status volume install made, found through the pointer, for `remove --purge`. A pointer that names
// anything other than "<instance>-status" is not followed: it was edited by hand, and the volume is not this command's to delete.
func (in *Installer) purgeStatusVolume(scoped incus.InstanceServer, target *Found) error {
	if target.StatusVolume == "" {
		return nil
	}
	pool, volume, err := ParseStatusVolume(target.StatusVolume)
	if err != nil {
		in.say("kept the status volume: %s %v", StatusVolumeKey, err)
		return nil
	}
	if volume != StatusVolumeName(target.Name) {
		in.say("kept volume %s/%s: it is not the %s that install makes, so it is not this command's to delete", pool, volume, StatusVolumeName(target.Name))
		return nil
	}
	if err := scoped.DeleteStoragePoolVolume(pool, "custom", volume); err != nil && !incusapi.IsNotFound(err) {
		return fmt.Errorf("deleting the status volume %s: %w", volume, err)
	}
	in.say("deleted volume %s", volume)
	return nil
}

// keptStatusVolume says that a plain remove left the status volume, as it leaves the helper's other volumes.
func (in *Installer) keptStatusVolume(target *Found) {
	if target.StatusVolume != "" {
		in.say("kept the status volume %s too", target.StatusVolume)
	}
}
