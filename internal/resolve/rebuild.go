package resolve

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
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

// rebuildSlot serializes rebuilds across a whole apply, so two instances are
// never down for a rebuild at once. It is a channel rather than a sync.Mutex so that a rebuild waiting its turn can give up when the run is
// interrupted.
var rebuildSlot = make(chan struct{}, 1)

// lockRebuild waits for the rebuild slot, or for ctx to end, in which case the caller has changed nothing and must not unlock.
func lockRebuild(ctx context.Context) error {
	select {
	case rebuildSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("interrupted while waiting for another rebuild to finish; nothing was changed: %w", ctx.Err())
	}
}

func unlockRebuild() { <-rebuildSlot }

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

	if rt, err := env.runtimeConfig(server, remote, ref); err != nil {
		block("could not read the new image's runtime config: %v", err)
	} else if diffs, keys := runtimeConfigDiff(current.Config, r.Config, rt); len(diffs) > 0 {
		block("the new image changes runtime config that a rebuild does not refresh; declare these keys under config: so they are written after the rebuild (%s) -- %s",
			strings.Join(keys, ", "), strings.Join(diffs, "; "))
	}

	if img, err := env.registryImage(server, remoteName, ref); err != nil {
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
	PullImage(ctx context.Context, image string) (fingerprint string, err error)
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
//
// A step that is abandoned at its deadline (incusapi.TimeoutError) is not a failed step: it may still be running on the server. So none of the
// recoveries below is done for it (restarting the instance under a snapshot or a rebuild that has not finished is the opposite of recovering), and the
// error says the outcome is unknown, what state the instance may be in, and that `tink plan` shows what the server reached.
//
// ctx (Ctrl-C) is honoured only until the instance is stopped: before that nothing has been changed, so the pull (the long step) is
// abandoned and the rebuild stops with "nothing was changed". From the stop on, ctx is not consulted again. A rebuild stopped between its stop and
// its restart would leave the instance down on a root filesystem that is deleted and not yet recreated, which is worse than the wait.
func runRebuild(ctx context.Context, ops rebuildOps, r Resource, now func() time.Time, note func(string, ...any)) error {
	interrupted := func() error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("rebuild of %s interrupted (nothing was changed): %w", r.Name, err)
		}
		return nil
	}
	if err := interrupted(); err != nil {
		return err
	}
	was, err := ops.InstanceRunning(r.Name)
	if err != nil {
		return fmt.Errorf("checking %s: %w", r.Name, err)
	}
	if snaps, err := ops.InstanceSnapshots(r.Name); err != nil {
		return fmt.Errorf("listing snapshots of %s: %w", r.Name, err)
	} else if len(snaps) > 0 {
		return fmt.Errorf("%s gained instance snapshots (%s) since planning; nothing was changed", r.Name, strings.Join(snaps, ", "))
	}

	if err := interrupted(); err != nil {
		return err
	}
	// Before stopping anything: downtime must not include the download.
	fp, err := ops.PullImage(ctx, r.Image)
	if err != nil {
		return fmt.Errorf("pulling the new image: %w (nothing was changed)", err)
	}
	note("%s/%s: new image %.12s is in the local store", r.Kind, r.Name, fp)
	if err := interrupted(); err != nil {
		return err
	}

	if was {
		if err := ops.Stop(r.Name); err != nil {
			if abandoned(err) {
				return fmt.Errorf("stopping %s: %w; %s may be stopped, stopping, or still running, and the rebuild went no further", r.Name, err, r.Name)
			}
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
		if abandoned(err) {
			return fmt.Errorf("snapshotting volumes: %w; %s is left stopped, not restarted, because the snapshot may still be running; the rebuild went no further", err, r.Name)
		}
		if err != nil {
			if rerr := restore(); rerr != nil {
				return fmt.Errorf("snapshotting volumes: %w; and restarting %s failed too: %v", err, r.Name, rerr)
			}
			return fmt.Errorf("snapshotting volumes: %w (instance restored to its previous state, still on the old image)", err)
		}
		note("%s/%s: snapshotted volumes %v as %s", r.Kind, r.Name, vols, snap)
	}

	if err := ops.Rebuild(r.Name, fp); err != nil {
		if abandoned(err) {
			return fmt.Errorf("rebuild: %w; %s is left stopped, not restarted, because the rebuild may still be running: its root filesystem may be deleted, recreated, or part way between (volumes are intact). "+
				"Re-run apply once tink plan shows the instance has settled", err, r.Name)
		}
		if rerr := restore(); rerr != nil {
			return fmt.Errorf("rebuild failed: %w; %s did not restart (%v) -- its root filesystem may have been destroyed (volumes are intact), re-run apply to retry the rebuild", err, r.Name, rerr)
		}
		return fmt.Errorf("rebuild failed: %w (instance restored to its previous state)", err)
	}

	if err := ops.ApplyConfig(r); err != nil {
		if abandoned(err) {
			return fmt.Errorf("rebuilt onto the new image, but applying config was abandoned: %w; %s is left stopped, not started, because its config may still be being written", err, r.Name)
		}
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

// abandoned reports whether err is a step given up on at its deadline, whose outcome is therefore not known (as opposed to one that failed).
func abandoned(err error) bool {
	var te *incusapi.TimeoutError
	return errors.As(err, &te)
}

// incusRebuildOps is the real thing. server must already be scoped to the
// instance's project.
type incusRebuildOps struct {
	server incus.InstanceServer
	env    *imageEnv
	// opTimeout, when set, replaces the deadlines (opbounds.go); a field so a test can use a short one.
	opTimeout time.Duration
}

func (o incusRebuildOps) limit(slow bool) time.Duration { return opLimit(o.opTimeout, slow) }

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

// PullImage puts the image into the server's local store (no instance involved) and returns its fingerprint; the rebuild then needs no
// download. The registry is asked for the image's fingerprint first, for the server's architecture, to see whether it is already there;
// if not, the SERVER is told to pull it by reference, with its own tools, so no image data and no tool of ours is involved, from a laptop
// as on the server. (Naming the image by fingerprint instead does not work: the server would try to resolve the hash as a registry name.)
func (o incusRebuildOps) PullImage(ctx context.Context, image string) (string, error) {
	remoteName, ref, _ := strings.Cut(image, ":")
	if o.env == nil {
		return "", errors.New("no registry access")
	}
	remote, ok := o.env.remotes()[remoteName]
	if !ok || len(remote.Addrs) == 0 {
		return "", fmt.Errorf("%q is not a configured OCI remote", remoteName)
	}
	reg, err := o.env.registryImage(o.server, remoteName, ref)
	if err != nil {
		return "", err
	}
	_, _, local, err := incusapi.LookupImage(o.server, reg.Fingerprint)
	if err != nil {
		// Not "not local": pulling on a lookup that merely failed would hide the real problem behind a second one.
		return "", fmt.Errorf("checking whether the image is already local: %w", err)
	}
	if local {
		return reg.Fingerprint, nil
	}
	// The server pulls from the address it is given, and the API has no other place for a credential: as `incus launch` does, send the
	// address with the credentials the remote's helper supplies written into it.
	authed, err := o.env.authedRemote(remote)
	if err != nil {
		return "", fmt.Errorf("remote %q: %w", remoteName, err)
	}
	addr := authed.Addrs[0]
	// The one call in a rebuild that is cancelled, not just abandoned, at its deadline: the pull changes no instance, so there is nothing to leave half done.
	limit := o.limit(true)
	pullCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	// The deadline cannot be taken from pullCtx for the request: a client call that is not an operation has no context to end.
	op, err := incusapi.Bounded(limit, "pulling "+image, func() (incus.Operation, error) {
		return o.server.CreateImage(api.ImagesPost{Source: &api.ImagesPostSource{
			ImageSource: api.ImageSource{Protocol: "oci", Server: addr, Alias: ref},
			Mode:        "pull",
			Type:        "image",
		}}, nil)
	})
	if err != nil {
		if abandoned(err) {
			return "", pullTimeout(image, limit)
		}
		return "", scrubCredentials(err, addr)
	}
	// Ending pullCtx makes the client cancel the server's download as well. That is safe here: the pull changes no instance.
	if err := op.WaitContext(pullCtx); err != nil {
		if errors.Is(pullCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", pullTimeout(image, limit)
		}
		return "", scrubCredentials(err, addr)
	}
	fp, _ := op.Get().Metadata["fingerprint"].(string)
	if fp == "" {
		return "", errors.New("the server pulled the image but did not say its fingerprint")
	}
	return fp, nil
}

// pullTimeout is the error of a pull that did not finish in time and was cancelled.
func pullTimeout(image string, after time.Duration) error {
	return fmt.Errorf("%w: pulling %s did not finish within %s, so it was cancelled (a pull changes no instance, so nothing is left running)", incusapi.ErrTimeout, image, after)
}

func (o incusRebuildOps) Stop(name string) error {
	return incusapi.BoundedDo(o.limit(false), "stopping "+name, func() error {
		op, err := o.server.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Timeout: 60}, "")
		if err != nil {
			return err
		}
		return op.Wait()
	})
}

func (o incusRebuildOps) Start(name string) error {
	return run.EnsureRunningWithin(o.server, name, o.limit(false))
}

// SnapshotVolumes snapshots every custom volume attached to the instance.
func (o incusRebuildOps) SnapshotVolumes(name, snapshot string) ([]string, error) {
	inst, err := incusapi.Bounded(o.limit(false), "reading "+name, func() (*api.Instance, error) {
		inst, _, err := o.server.GetInstance(name)
		return inst, err
	})
	if err != nil {
		return nil, err
	}
	var done []string
	for _, dev := range inst.ExpandedDevices {
		pool, src := dev["pool"], dev["source"]
		if dev["type"] != "disk" || pool == "" || src == "" || strings.HasPrefix(src, "/") {
			continue
		}
		err := incusapi.BoundedDo(o.limit(true), "snapshotting volume "+pool+"/"+src, func() error {
			op, err := o.server.CreateStoragePoolVolumeSnapshot(pool, "custom", src, api.StorageVolumeSnapshotsPost{Name: snapshot})
			if err != nil {
				return err
			}
			return op.Wait()
		})
		if err != nil {
			return done, fmt.Errorf("volume %s/%s: %w", pool, src, err)
		}
		done = append(done, pool+"/"+src)
	}
	return done, nil
}

func (o incusRebuildOps) Rebuild(name, fingerprint string) error {
	return incusapi.BoundedDo(o.limit(true), "rebuilding "+name, func() error {
		op, err := o.server.RebuildInstance(name, api.InstanceRebuildPost{Source: api.InstanceSource{Type: "image", Fingerprint: fingerprint}})
		if err != nil {
			return err
		}
		return op.Wait()
	})
}

func (o incusRebuildOps) ApplyConfig(r Resource) error {
	return run.ApplyConfig(o.server, &run.Spec{Name: r.Name, Config: r.Config, Devices: r.Devices, OwnedConfigPrefixes: ownedConfigPrefixes, OpTimeout: o.limit(false)})
}
