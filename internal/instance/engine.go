package instance

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/incusconf"
)

// Create creates spec's instance without starting it (create, never
// `incus launch`): some images (e.g. Postgres, Nextcloud) run a one-shot,
// config-gated action on first boot that needs env vars already present,
// so config has to be in place before the instance ever starts (see
// ApplyConfig, EnsureRunning). Goes through Incus's real Go client end to
// end -- CreateInstanceFromImage (client/incus.go) already handles
// local-vs-remote image resolution internally (the same same-server fast
// path the daemon's own optimisation uses), so the only piece this still
// needs to do itself is turning spec.Image's remote:ref syntax into an
// ImageServer plus a resolved api.Image, the same way incus's own CLI
// does it (cmd/incus/create.go's getImgInfo) -- read directly rather
// than reimplemented from guesswork. Client config (remotes) is read via
// cliconfig.LoadConfig, a real Go API for the same file `incus remote`
// itself reads and writes -- see internal/bootstrap/registries.go for
// the same reasoning applied to a read-only remote lookup.
//
// The creation request carries the profiles, the ephemeral flag, spec.CreateConfig
// and, when spec.RootDiskAtCreate is set, the root disk; the rest of the config
// and every other device come afterwards, from ApplyConfig.
func Create(server incus.InstanceServer, spec *Spec, project string) error {
	scoped := server
	if project != "" {
		scoped = server.UseProject(project)
	}

	conf, err := incusconf.Load()
	if err != nil {
		return fmt.Errorf("creating %s: loading incus client config: %w", spec.Name, err)
	}

	imgServer, imgInfo, err := resolveImage(scoped, conf, spec.Image)
	if err != nil {
		return fmt.Errorf("creating %s: %w", spec.Name, err)
	}

	instanceType := api.InstanceTypeContainer
	if spec.VM {
		instanceType = api.InstanceTypeVM
	}

	// The request and the wait are one bounded call: a daemon that never answers the request is the same as one that never finishes the operation.
	return incusapi.BoundedDo(spec.OpTimeout, "creating "+spec.Name, func() error {
		op, err := scoped.CreateInstanceFromImage(imgServer, imgInfo, api.InstancesPost{
			Name: spec.Name,
			Type: instanceType,
			InstancePut: api.InstancePut{
				Profiles:  spec.Profiles,
				Ephemeral: spec.Ephemeral,
				Devices:   creationDevices(spec),
				Config:    spec.CreateConfig,
			},
		})
		if err != nil {
			return fmt.Errorf("creating %s: %w", spec.Name, err)
		}
		return op.Wait()
	})
}

// creationDevices is what a creation request carries besides the profiles: the spec's root disk when it asks for one (Spec.RootDiskAtCreate), else
// nothing. Every other device waits for ApplyConfig, because it may name a volume that does not exist yet, and Incus checks that when it writes the
// record. The root disk is applied again by ApplyConfig, which changes nothing.
func creationDevices(spec *Spec) api.DevicesMap {
	if !spec.RootDiskAtCreate {
		return nil
	}
	var devices api.DevicesMap
	for name, dev := range spec.Devices {
		if isRootDisk(dev) {
			if devices == nil {
				devices = api.DevicesMap{}
			}
			devices[name] = dev
		}
	}
	return devices
}

// resolveImage splits image the same way incus's own CLI parses a
// REMOTE:REF image reference (cmd/incus/create.go's getImgInfo) -- but
// only treats a colon-prefix as a remote name when it actually matches
// one configured in conf; otherwise the whole string is a bare local
// reference (this platform's own kind: image resources always resolve
// to one of these -- an alias with no colon at all -- so this is the
// common path for tink itself, not just a fallback).
func resolveImage(scoped incus.InstanceServer, conf *cliconfig.Config, image string) (incus.ImageServer, api.Image, error) {
	remoteName, ref, hasPrefix := strings.Cut(image, ":")
	remote, isKnownRemote := conf.Remotes[remoteName]
	if !hasPrefix || !isKnownRemote {
		return resolveLocalImage(scoped, image)
	}

	imgServer, err := conf.GetImageServer(remoteName)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("connecting to remote %q: %w", remoteName, err)
	}

	if remote.Protocol != "incus" {
		// Public image servers (simplestreams, oci, ...): the reference
		// itself is the fingerprint/tag to pull, not an alias to resolve
		// first -- confirmed by reading getImgInfo's own "optimisation
		// for public image servers" branch rather than assumed.
		return imgServer, api.Image{Fingerprint: ref, ImagePut: api.ImagePut{Public: true}}, nil
	}

	alias, _, found, err := incusapi.LookupImageAlias(imgServer, ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving %s: %w", image, err)
	}
	if found {
		ref = alias.Target
	}
	imgInfo, _, err := imgServer.GetImage(ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving %s: %w", image, err)
	}
	return imgServer, *imgInfo, nil
}

// resolveLocalImage resolves ref (an alias or a bare fingerprint)
// against scoped directly -- scoped is already project-scoped by
// Create, and a local image alias lives inside that same project once
// features.images is enabled, the same isolation confirmed live while
// building this platform's own haos test project.
func resolveLocalImage(scoped incus.InstanceServer, ref string) (incus.ImageServer, api.Image, error) {
	alias, _, found, err := incusapi.LookupImageAlias(scoped, ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving local image %q: %w", ref, err)
	}
	if found {
		ref = alias.Target
	}
	imgInfo, _, err := scoped.GetImage(ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving local image %q: %w", ref, err)
	}
	return scoped, *imgInfo, nil
}

// ApplyConfig sets spec's translated Config and Devices onto the
// just-created (not yet started) instance. Unlike Create, this is
// modeled cleanly by the daemon's own API, so it goes through Incus's
// Go client rather than a second shell-out.
func ApplyConfig(server incus.InstanceServer, spec *Spec) error {
	if err := ensureManagedVolumes(server, spec); err != nil {
		return err
	}

	type read struct {
		inst *api.Instance
		etag string
	}
	// The instance is read again, and the change worked out again, if Incus refuses the write because it changed in between (a 412): a whole-instance
	// PUT put back from the first read would undo whatever the other writer did. A timeout is not a 412 and ends the retries.
	return incusapi.RetryOnConflict(func() error {
		got, err := incusapi.Bounded(spec.OpTimeout, "reading "+spec.Name, func() (read, error) {
			inst, etag, err := server.GetInstance(spec.Name)
			return read{inst, etag}, err
		})
		if err != nil {
			return fmt.Errorf("reading %s after creation: %w", spec.Name, err)
		}
		inst, etag := got.inst, got.etag

		put := inst.Writable()
		if spec.Ephemeral { // only ever turned on here, like every other declared field: not declaring it changes nothing
			put.Ephemeral = true
		}
		if put.Config == nil {
			put.Config = map[string]string{}
		}
		for k, v := range spec.Config {
			put.Config[k] = v
		}
		for _, k := range StaleOwnedKeys(put.Config, spec.Config, spec.OwnedConfigPrefixes) {
			delete(put.Config, k)
		}
		if put.Devices == nil {
			put.Devices = api.DevicesMap{}
		}
		for name, dev := range spec.Devices {
			put.Devices[name] = dev
		}

		return incusapi.BoundedDo(spec.OpTimeout, "updating "+spec.Name+" config/devices", func() error {
			op, err := server.UpdateInstance(spec.Name, put, etag)
			if err != nil {
				return fmt.Errorf("updating %s config/devices: %w", spec.Name, err)
			}
			return op.Wait()
		})
	})
}

// StaleOwnedKeys are the keys of live that are owned and that declared does not carry, sorted. A prefix that ends in a dot owns every key under it
// (user.tink.publish. owns the stamps of every publish); one that does not owns that one key and nothing built from its name (user.tink.site owns
// user.tink.site and not user.tink.sitemap). It is the whole of what "tink owns this" means: plan reports these as removals and ApplyConfig makes them,
// and both ask here so they cannot disagree. A key that is owned by none is never returned, however absent from declared it is.
func StaleOwnedKeys(live, declared map[string]string, prefixes []string) []string {
	var out []string
	for k := range live {
		if _, ok := declared[k]; ok {
			continue
		}
		for _, p := range prefixes {
			if p == "" {
				continue
			}
			if strings.HasSuffix(p, ".") && strings.HasPrefix(k, p) || !strings.HasSuffix(p, ".") && k == p {
				out = append(out, k)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// ensureManagedVolumes creates any managed storage volume a disk device
// references that doesn't already exist yet. Docker auto-creates a named
// volume on first use (`-v name:path`); Incus's own managed volumes
// don't -- attaching a disk device to one that's never been created
// fails validation outright, and UpdateInstance applies atomically, so
// one missing volume would otherwise silently drop every other
// translated config/device change too.
func ensureManagedVolumes(server incus.InstanceServer, spec *Spec) error {
	for _, dev := range spec.Devices {
		if dev["type"] != "disk" || dev["pool"] == "" || dev["source"] == "" {
			continue // a bind mount (no pool), a non-disk device, or a root disk (a pool, but no volume of its own to create)
		}

		pool, name := dev["pool"], dev["source"]
		found, err := incusapi.Bounded(spec.OpTimeout, "looking for volume "+pool+"/"+name, func() (bool, error) {
			_, _, found, err := incusapi.LookupVolume(server, pool, "custom", name)
			return found, err
		})
		if err != nil {
			// Not "does not exist": creating on a failed read would fail confusingly, or hide the real problem.
			return fmt.Errorf("checking whether managed volume %s/%s exists: %w", pool, name, err)
		}
		if found {
			continue // already exists
		}

		post := api.StorageVolumesPost{
			Name:             name,
			Type:             "custom",
			ContentType:      "filesystem",
			StorageVolumePut: api.StorageVolumePut{Config: spec.VolumeConfig},
		}
		if err := incusapi.BoundedDo(spec.OpTimeout, "creating volume "+pool+"/"+name, func() error { return server.CreateStoragePoolVolume(pool, post) }); err != nil {
			return fmt.Errorf("creating managed volume %s/%s: %w", pool, name, err)
		}
	}
	return nil
}

// EnsureRunning starts name -- in practice always a first start, since
// Create() never starts the instance itself. Picks start-vs-restart from
// current status rather than assuming Stopped, and retries on Incus's
// own transient "instance is busy" rejection (its operation queue can
// briefly reject a state change mid-transition); mirrors
// internal/bootstrap.ensureRunning's own logic, reproduced here rather
// than shared across packages.
func EnsureRunning(server incus.InstanceServer, name string) error {
	return EnsureRunningWithin(server, name, 0)
}

// EnsureRunningWithin is EnsureRunning that abandons each call to the server at timeout (zero: no bound), with an error wrapping
// incusapi.ErrTimeout. A start that timed out is NOT retried: it may still complete, and a second state change on top of it is a race. Only the busy
// rejection above is retried.
func EnsureRunningWithin(server incus.InstanceServer, name string, timeout time.Duration) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		inst, err := incusapi.Bounded(timeout, "checking "+name+"'s state", func() (*api.Instance, error) {
			inst, _, err := server.GetInstance(name)
			return inst, err
		})
		if err != nil {
			return fmt.Errorf("checking %s's state: %w", name, err)
		}

		action := "start"
		if inst.Status == "Running" {
			action = "restart"
		}

		err = incusapi.BoundedDo(timeout, action+" of "+name, func() error {
			op, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Timeout: 30}, "")
			if err != nil {
				return err
			}
			return op.Wait()
		})
		if err == nil {
			return nil
		}
		if errors.Is(err, incusapi.ErrTimeout) {
			return fmt.Errorf("starting/restarting %s to apply its config: %w", name, err)
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return fmt.Errorf("starting/restarting %s to apply its config: giving up after %d attempts: %w", name, maxAttempts, lastErr)
}
