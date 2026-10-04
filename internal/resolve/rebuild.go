package resolve

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/run"
)

// on_image_change: rebuild -- converge an image change by rebuilding the
// instance root filesystem onto the new image. Opt-in per instance, OCI app
// containers only. See the design note for the reasoning; the facts below were
// each verified against Incus source or a live daemon:
//   - incus rebuild needs a STOPPED instance, and downloads the image inside
//     the operation, so an un-pulled image means downtime for the whole pull.
//   - it deletes the root volume and then creates the new one: not atomic.
//   - it does not refresh the oci.* / environment.* keys copied from the old
//     image at creation (see runtimeConfigDiff).
//   - instance snapshots make it fail; custom-volume snapshots do not.

const (
	OnImageChangeReport  = "report"
	OnImageChangeIgnore  = "ignore"
	OnImageChangeRebuild = "rebuild"
)

// rebuildMu serializes rebuilds across a whole apply, so two instances are
// never down for a rebuild at once.
var rebuildMu sync.Mutex

// preflight is the read-only part: everything that can be checked before
// anything is pulled, stopped or deleted.
type preflight struct {
	Blockers []string
	Warnings []string
}

func rebuildPreflight(server incus.InstanceServer, current *api.Instance, r Resource, env *imageEnv) preflight {
	var p preflight
	block := func(format string, a ...any) { p.Blockers = append(p.Blockers, fmt.Sprintf(format, a...)) }

	if current.Type != "container" {
		block("on_image_change: rebuild supports containers only; %s is a %s", r.Name, current.Type)
		return p
	}
	if current.Config["image.type"] != "oci" {
		block("on_image_change: rebuild is for OCI app containers; %s was not built from an OCI image (image.type=%q)", r.Name, current.Config["image.type"])
	}

	if names, err := server.GetInstanceSnapshotNames(r.Name); err != nil {
		block("could not list snapshots of %s: %v", r.Name, err)
	} else if len(names) > 0 {
		block("%s has instance snapshots (%s); Incus refuses to rebuild an instance that has them -- delete them and snapshot the data volumes instead", r.Name, strings.Join(names, ", "))
	}

	if !hasDataVolume(current.ExpandedDevices) {
		p.Warnings = append(p.Warnings, fmt.Sprintf("%s has no attached data volume: a rebuild discards everything written to its root filesystem", r.Name))
	}

	remoteName, ref, _ := strings.Cut(r.Image, ":")
	remote, ok := env.remotes()[remoteName]
	if !ok || remote.Protocol != "oci" {
		block("image %q is not a ref on a configured OCI remote", r.Image)
		return p
	}

	if rt, err := env.runtimeConfig(remote, ref); err != nil {
		block("could not read the new image's runtime config: %v", err)
	} else if diffs, keys := runtimeConfigDiff(current.Config, r.Config, rt); len(diffs) > 0 {
		block("the new image changes runtime config that a rebuild does not refresh; declare these keys under config: so they are written after the rebuild (%s) -- %s",
			strings.Join(keys, ", "), strings.Join(diffs, "; "))
	}

	if img, err := env.registryImage(remoteName, ref); err != nil {
		p.Warnings = append(p.Warnings, fmt.Sprintf("could not size the new image for the free-space check: %v", err))
	} else if pool := current.ExpandedDevices["root"]["pool"]; pool != "" && img.Size > 0 {
		if res, err := server.GetStoragePoolResources(pool); err != nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("could not read free space of pool %q: %v", pool, err))
		} else {
			// Compressed layers expand and the image is cached as well as unpacked
			// into the instance: a generous heuristic, not a measurement.
			need := uint64(img.Size)*3 + 256<<20
			if free := res.Space.Total - res.Space.Used; res.Space.Total > 0 && free < need {
				block("pool %q has %d MiB free, the rebuild needs roughly %d MiB", pool, free>>20, need>>20)
			}
		}
	}
	return p
}

// hasDataVolume reports whether any disk device is a custom storage volume
// (a pool and a named source, not a host path). The root disk has no source.
func hasDataVolume(devices map[string]map[string]string) bool {
	for _, dev := range devices {
		if dev["type"] == "disk" && dev["pool"] != "" && dev["source"] != "" && !strings.HasPrefix(dev["source"], "/") {
			return true
		}
	}
	return false
}

// rebuildOps is everything runRebuild does to the outside world, so the
// sequencing and the failure handling can be tested with a fake.
type rebuildOps interface {
	InstanceRunning(name string) (bool, error)
	InstanceSnapshots(name string) ([]string, error)
	PullImage(image string) (fingerprint string, err error)
	Stop(name string) error
	Start(name string) error
	SnapshotVolumes(name, snapshot string) ([]string, error)
	Rebuild(name, fingerprint string) error
	ApplyConfig(r Resource) error
}

// runRebuild: pull -> stop -> [snapshot volumes] -> rebuild -> config -> start.
// Config goes on after the rebuild so the new config is always paired with the
// new image. Every failure is recoverable by re-running apply, because drift
// persists until the rebuild succeeds.
func runRebuild(ops rebuildOps, r Resource, now func() time.Time, note func(string, ...any)) error {
	was, err := ops.InstanceRunning(r.Name)
	if err != nil {
		return fmt.Errorf("checking %s: %w", r.Name, err)
	}
	if snaps, err := ops.InstanceSnapshots(r.Name); err != nil {
		return fmt.Errorf("listing snapshots of %s: %w", r.Name, err)
	} else if len(snaps) > 0 {
		return fmt.Errorf("%s gained instance snapshots (%s) since planning; nothing was changed", r.Name, strings.Join(snaps, ", "))
	}

	// Before stopping anything: downtime must not include the download.
	fp, err := ops.PullImage(r.Image)
	if err != nil {
		return fmt.Errorf("pulling the new image: %w (nothing was changed)", err)
	}
	note("%s/%s: new image %.12s is in the local store", r.Kind, r.Name, fp)

	if was {
		if err := ops.Stop(r.Name); err != nil {
			return fmt.Errorf("stopping %s: %w (nothing was changed)", r.Name, err)
		}
	}
	restore := func() error {
		if was {
			return ops.Start(r.Name)
		}
		return nil
	}

	// After the stop, so the copy is consistent (a live SQLite WAL is not).
	if r.SnapshotVolumes {
		snap := "tink-pre-rebuild-" + now().UTC().Format("20060102T150405Z")
		vols, err := ops.SnapshotVolumes(r.Name, snap)
		if err != nil {
			if rerr := restore(); rerr != nil {
				return fmt.Errorf("snapshotting volumes: %w; and restarting %s failed too: %v", err, r.Name, rerr)
			}
			return fmt.Errorf("snapshotting volumes: %w (instance restored to its previous state, still on the old image)", err)
		}
		note("%s/%s: snapshotted volumes %v as %s", r.Kind, r.Name, vols, snap)
	}

	if err := ops.Rebuild(r.Name, fp); err != nil {
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("rebuild failed: %w; %s did not restart (%v) -- its root filesystem may have been destroyed (volumes are intact), re-run apply to retry the rebuild", err, r.Name, rerr)
		}
		return fmt.Errorf("rebuild failed: %w (instance restored to its previous state)", err)
	}

	if err := ops.ApplyConfig(r); err != nil {
		return fmt.Errorf("rebuilt onto the new image, but applying config failed: %w; %s was left stopped -- fix and re-run apply", err, r.Name)
	}
	if was {
		if err := ops.Start(r.Name); err != nil {
			return fmt.Errorf("rebuilt and configured, but starting %s failed: %w", r.Name, err)
		}
		note("%s/%s: rebuilt onto %.12s and started", r.Kind, r.Name, fp)
	} else {
		note("%s/%s: rebuilt onto %.12s (left stopped, as it was)", r.Kind, r.Name, fp)
	}
	return nil
}

// incusRebuildOps is the real thing. server must already be scoped to the
// instance's project.
type incusRebuildOps struct {
	server incus.InstanceServer
	env    *imageEnv
}

func (o incusRebuildOps) InstanceRunning(name string) (bool, error) {
	inst, _, err := o.server.GetInstance(name)
	if err != nil {
		return false, err
	}
	return inst.Status == "Running", nil
}

func (o incusRebuildOps) InstanceSnapshots(name string) ([]string, error) {
	return o.server.GetInstanceSnapshotNames(name)
}

// PullImage copies the image into the local store (no instance involved) and
// returns its fingerprint; a later rebuild then needs no download.
func (o incusRebuildOps) PullImage(image string) (string, error) {
	remoteName, ref, _ := strings.Cut(image, ":")
	if o.env == nil || o.env.conf == nil {
		return "", errors.New("incus client config unavailable")
	}
	ensureSkopeoOnPath()
	is, err := o.env.conf.GetImageServer(remoteName)
	if err != nil {
		return "", err
	}
	alias, _, err := is.GetImageAlias(ref)
	if err != nil {
		return "", err
	}
	img, _, err := is.GetImage(alias.Target)
	if err != nil {
		return "", err
	}
	if _, _, err := o.server.GetImage(img.Fingerprint); err == nil {
		return img.Fingerprint, nil // already local
	}
	op, err := o.server.CopyImage(is, *img, &incus.ImageCopyArgs{})
	if err != nil {
		return "", err
	}
	if err := op.Wait(); err != nil {
		return "", err
	}
	return img.Fingerprint, nil
}

func (o incusRebuildOps) Stop(name string) error {
	op, err := o.server.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Timeout: 60}, "")
	if err != nil {
		return err
	}
	return op.Wait()
}

func (o incusRebuildOps) Start(name string) error { return run.EnsureRunning(o.server, name) }

// SnapshotVolumes snapshots every custom volume attached to the instance.
func (o incusRebuildOps) SnapshotVolumes(name, snapshot string) ([]string, error) {
	inst, _, err := o.server.GetInstance(name)
	if err != nil {
		return nil, err
	}
	var done []string
	for _, dev := range inst.ExpandedDevices {
		pool, src := dev["pool"], dev["source"]
		if dev["type"] != "disk" || pool == "" || src == "" || strings.HasPrefix(src, "/") {
			continue
		}
		op, err := o.server.CreateStoragePoolVolumeSnapshot(pool, "custom", src, api.StorageVolumeSnapshotsPost{Name: snapshot})
		if err != nil {
			return done, fmt.Errorf("volume %s/%s: %w", pool, src, err)
		}
		if err := op.Wait(); err != nil {
			return done, fmt.Errorf("volume %s/%s: %w", pool, src, err)
		}
		done = append(done, pool+"/"+src)
	}
	return done, nil
}

func (o incusRebuildOps) Rebuild(name, fingerprint string) error {
	op, err := o.server.RebuildInstance(name, api.InstanceRebuildPost{Source: api.InstanceSource{Type: "image", Fingerprint: fingerprint}})
	if err != nil {
		return err
	}
	return op.Wait()
}

func (o incusRebuildOps) ApplyConfig(r Resource) error {
	return run.ApplyConfig(o.server, &run.Spec{Name: r.Name, Config: r.Config, Devices: r.Devices})
}
