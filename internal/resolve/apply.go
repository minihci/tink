package resolve

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/archive"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/run"
)

// createOne creates r. Every call that changes the server has a deadline (opbounds.go); where one kind takes several steps, each step has its own,
// so a step that is abandoned stops the ones after it.
func createOne(server incus.InstanceServer, r Resource, env volumeEnv) error {
	bound := func(slow bool, fn func() error) error {
		return incusapi.BoundedDo(env.limit(slow), fmt.Sprintf("creating %s/%s", r.Kind, r.Name), fn)
	}
	switch r.Kind {
	case KindHelper:
		return bound(true, func() error { return createHelper(server, r) })
	case KindProject:
		return bound(false, func() error {
			return server.CreateProject(api.ProjectsPost{
				Name:       r.Name,
				ProjectPut: api.ProjectPut{Config: ownership.With(r.Config, env.stack)},
			})
		})
	case KindNetwork:
		return bound(false, func() error { return createNetwork(server, r, env.stack) })
	case KindNetworkACL:
		return bound(false, func() error { return createNetworkACL(server, r, env.stack) })
	case KindProfile:
		return bound(false, func() error {
			return scopedServer(server, r).CreateProfile(api.ProfilesPost{
				Name:       r.Name,
				ProfilePut: api.ProfilePut{Config: ownership.With(r.Config, env.stack), Devices: r.Devices},
			})
		})
	case KindStorageVolume:
		pool := r.Pool
		if pool == "" {
			pool = "default"
		}
		config, _, err := volumeBackupConfig(r, env)
		if err != nil {
			return err
		}
		config = ownership.With(config, env.stack)
		return bound(false, func() error {
			return scopedServer(server, r).CreateStoragePoolVolume(pool, api.StorageVolumesPost{
				StorageVolumePut: api.StorageVolumePut{Config: config},
				Name:             r.Name,
				Type:             "custom",
				ContentType:      "filesystem",
			})
		})
	case KindInstance:
		return createInstance(server, r, env)
	case KindFile:
		return pushFile(server, r, env.limit(false))
	case KindIncus:
		return runIncus(r)
	case KindImage:
		return createImage(server, r, env)
	case KindExec:
		return runExec(env.context(), server, r)
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
}

// runIncus runs `incus` with r.Command's args -- reached only when planIncus
// already found r.Check failing, so this is the converging half of the
// same pair.
func runIncus(r Resource) error {
	out, err := exec.Command("incus", r.Command...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("running incus %v: %w: %s", r.Command, err, out)
	}
	return nil
}

// createImage imports Source (an already-complete VM disk image -- see
// Resource.Alias's own doc comment for why this never builds anything,
// only wraps and uploads what's already there) via Incus's own image
// upload API, then points Alias at the resulting fingerprint. Goes
// through the real Go client end to end, no shell-out: the metadata
// tarball built below needs no compression at all -- Incus's own
// compression sniffing (shared/archive.DetectCompressionFile, the same
// routine reused just below for VM-vs-container detection) accepts a
// plain uncompressed tar directly (ustar magic), confirmed by reading
// that function's own source rather than assumed.
func createImage(server incus.InstanceServer, r Resource, env volumeEnv) error {
	s := scopedServer(server, r)

	metaYAML, err := yaml.Marshal(api.ImageMetadata{
		Architecture: r.Architecture,
		CreationDate: time.Now().Unix(),
		Properties:   ownership.With(r.Properties, env.stack), // the stamp is an image property, the one place Incus lets a client keep a key of its own
	})
	if err != nil {
		return fmt.Errorf("building metadata.yaml for %s: %w", r.Name, err)
	}

	metaBuf := &bytes.Buffer{}
	tw := tar.NewWriter(metaBuf)
	if err := tw.WriteHeader(&tar.Header{Name: "metadata.yaml", Mode: 0o644, Size: int64(len(metaYAML))}); err != nil {
		return fmt.Errorf("building metadata tarball for %s: %w", r.Name, err)
	}
	if _, err := tw.Write(metaYAML); err != nil {
		return fmt.Errorf("building metadata tarball for %s: %w", r.Name, err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("building metadata tarball for %s: %w", r.Name, err)
	}

	rootfs, err := os.Open(r.Source)
	if err != nil {
		return fmt.Errorf("opening %s: %w", r.Source, err)
	}
	defer rootfs.Close()

	// Incus determines container vs virtual-machine from the rootfs
	// file's own format, not anything stated in metadata.yaml -- the
	// same detection incus's own CLI uses for `image import`
	// (cmd/incus/image.go), confirmed live while building this
	// platform's own haos test project.
	imageType := "container"
	if _, ext, _, err := archive.DetectCompressionFile(rootfs); err == nil && (ext == ".qcow2" || ext == ".vmdk") {
		imageType = "virtual-machine"
	}
	if _, err := rootfs.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewinding %s: %w", r.Source, err)
	}

	// The upload and the wait for the import are one bounded call (slow: the disk is uploaded whole); the alias that follows is a quick one of its own.
	fingerprint, err := incusapi.Bounded(env.limit(true), "importing image for "+r.Name, func() (string, error) {
		op, err := s.CreateImage(api.ImagesPost{Filename: "metadata.yaml"}, &incus.ImageCreateArgs{
			MetaFile:   metaBuf,
			MetaName:   "metadata.yaml",
			RootfsFile: rootfs,
			RootfsName: filepath.Base(r.Source),
			Type:       imageType,
		})
		if err != nil {
			return "", fmt.Errorf("importing image for %s: %w", r.Name, err)
		}
		if err := op.Wait(); err != nil {
			return "", fmt.Errorf("importing image for %s: %w", r.Name, err)
		}
		fingerprint, _ := op.Get().Metadata["fingerprint"].(string)
		return fingerprint, nil
	})
	if err != nil {
		return err
	}
	if fingerprint == "" {
		return fmt.Errorf("importing image for %s: daemon returned no fingerprint", r.Name)
	}

	return incusapi.BoundedDo(env.limit(false), "creating the alias "+r.Alias, func() error {
		return s.CreateImageAlias(api.ImageAliasesPost{
			ImageAliasesEntry: api.ImageAliasesEntry{
				Name:                 r.Alias,
				ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: fingerprint},
			},
		})
	})
}

// createInstance is the concrete proof of the reuse claim in
// docs/resolver-architecture.md: the actual leaf operation -- create,
// then apply config/devices, then start -- is tink run's own exported
// Create/ApplyConfig/EnsureRunning, not a reimplementation. The
// first-boot-config-ordering fix tink run needed for Postgres/Nextcloud
// comes along for free.
//
// The instance's own files are placed between configuring it and starting it (see firststart.go, and firststartmount.go for the ones a mount would
// hide). env.report, when set, is told what was done with them.
//
// Each step has its own deadline (opbounds.go), and a step that is abandoned ends the creation there: the later steps are not run on top of work whose
// state is unknown. Creating is the slow one (the server may fetch the image first); the rest are quick.
func createInstance(server incus.InstanceServer, r Resource, env volumeEnv) error {
	spec := &run.Spec{
		Name:     r.Name,
		Image:    r.Image,
		Config:   r.Config,
		Devices:  r.Devices,
		Profiles: r.Profiles,
		VM:       r.VM,

		Ephemeral: r.Ephemeral,
		OpTimeout: env.limit(false),
	}
	// the stamp goes in the creation request, so an instance never exists unstamped (and a crash between two calls cannot leave one)
	spec.CreateConfig = ownership.With(nil, env.stack)
	creating := *spec
	creating.OpTimeout = env.limit(true)
	s := scopedServer(server, r)
	var pl placement
	if candidates := env.firstStart[instanceKey(r.Project, r.Name)]; len(candidates) > 0 {
		pl = placeBy(r, candidates, profileLookup(s, env.profiles))
	}
	pr := newPlacer(server, pl)
	names, err := createInOrder(instanceSteps{
		create: func() error { return run.Create(server, &creating, r.Project) },
		config: func() error { return run.ApplyConfig(s, spec) },
		place: func(f Resource) error {
			return incusapi.BoundedDo(env.limit(false), "placing file/"+f.Name+" before the first start", func() error { return pr.place(f) })
		},
		start: func() error { return run.EnsureRunningWithin(s, spec.Name, env.limit(false)) },
	}, pl.files())
	if out := pr.outcome(names); env.report != nil && (len(out.Placed) > 0 || len(out.Skipped) > 0) {
		env.report(r.Name, out)
	}
	return err
}

// updateInstance converges an already-existing instance's config/devices
// via tink run's own ApplyConfig -- the same live-updating logic
// createInstance already reuses for a fresh instance, not a second
// implementation of the same GetInstance/merge/UpdateInstance dance (and
// ensureManagedVolumes' disk-volume-auto-create comes along for free,
// too). Devices merge as whole blocks by name (see diffDevices' own doc
// comment), so this can only ever add or fully replace a device, never
// patch one field of an existing one -- matching how Incus's own API
// works, not a limitation added here. A config/device change Incus
// itself refuses against a running instance (some keys require it
// stopped first) surfaces as a plain error, the same as a raw `incus
// config set` would give -- see Resource.Restart's own doc comment for
// why this doesn't attempt a stop/update/start sequence to work around
// that; that's real downtime, a bigger decision than this function
// makes for you.
//
// limit is the deadline of each call to the server (zero: none); a call abandoned at it ends the update there.
func updateInstance(server incus.InstanceServer, r Resource, limit time.Duration) error {
	spec := &run.Spec{Name: r.Name, Config: r.Config, Devices: r.Devices, Ephemeral: r.Ephemeral, OwnedConfigPrefixes: ownedConfigPrefixes, OpTimeout: limit}
	if err := run.ApplyConfig(server, spec); err != nil {
		return err
	}
	if r.Restart {
		return run.EnsureRunningWithin(server, r.Name, limit)
	}
	// A stopped instance is planned a start (planInstance); EnsureRunning starts what is not running.
	inst, err := incusapi.Bounded(limit, "checking "+r.Name+"'s state", func() (*api.Instance, error) {
		inst, _, err := server.GetInstance(r.Name)
		return inst, err
	})
	if err != nil {
		return fmt.Errorf("checking %s's state: %w", r.Name, err)
	}
	if inst.Status != "Stopped" {
		return nil
	}
	return run.EnsureRunningWithin(server, r.Name, limit)
}

func updateOne(server incus.InstanceServer, r Resource, env volumeEnv) error {
	s := scopedServer(server, r)
	// An instance and a file take several steps, each with its own deadline; the rest are one change, bounded as a whole.
	switch r.Kind {
	case KindInstance:
		return updateInstance(s, r, env.limit(false))
	case KindFile:
		// Pushing is the same operation whether the file is new or just
		// changed -- CreateInstanceFile overwrites either way.
		return pushFile(server, r, env.limit(false))
	}
	return incusapi.BoundedDo(env.limit(false), fmt.Sprintf("updating %s/%s", r.Kind, r.Name), func() error { return updateInPlace(s, r, env) })
}

// updateInPlace is updateOne for the kinds whose update is one read and one write.
func updateInPlace(s incus.InstanceServer, r Resource, env volumeEnv) error {
	switch r.Kind {
	case KindNetwork:
		return updateNetwork(s, r)
	case KindNetworkACL:
		return updateNetworkACL(s, r)
	case KindProfile:
		current, etag, err := s.GetProfile(r.Name)
		if err != nil {
			return err
		}
		put := current.Writable()
		if put.Config == nil {
			put.Config = map[string]string{}
		}
		for k, v := range r.Config {
			put.Config[k] = v
		}
		if put.Devices == nil {
			put.Devices = api.DevicesMap{}
		}
		for name, dev := range r.Devices {
			put.Devices[name] = dev
		}
		return s.UpdateProfile(r.Name, put, etag)
	case KindStorageVolume:
		pool := r.Pool
		if pool == "" {
			pool = "default"
		}
		current, etag, err := s.GetStoragePoolVolume(pool, "custom", r.Name)
		if err != nil {
			return err
		}
		put := current.Writable()
		if put.Config == nil {
			put.Config = map[string]string{}
		}
		set, remove, err := volumeBackupConfig(r, env)
		if err != nil {
			return err
		}
		splitCreationOnly(set) // how a volume was made is not changed afterwards (decideVolume warns about it)
		for k, v := range set {
			put.Config[k] = v
		}
		for _, k := range remove {
			delete(put.Config, k)
		}
		return s.UpdateStoragePoolVolume(pool, "custom", r.Name, put, etag)
	default:
		// Not implemented yet: projects are create-only so far,
		// matching every real deployment on this platform to date --
		// nothing has ever needed to mutate one in place. (Storage volumes
		// became updatable for their backup snapshot policy.)
		return fmt.Errorf("update is not implemented for kind %q", r.Kind)
	}
}

// pushFile writes r.Content to r.Path inside r.Instance, then -- only if
// r.Restart asks for it -- restarts that instance via tink run's own
// EnsureRunning, reused exactly as-is (it already knows how to pick
// start-vs-restart from current status and retry on Incus's transient
// "instance is busy" rejection). This is the actual fix for the one gap
// found live building this same stack with Terraform: a pushed file
// alone doesn't make a running process notice it changed.
//
// Where the file goes follows the instance (filemount.go): a file under a disk-device mount of an instance that is not running goes into the volume
// behind the mount, never into the root filesystem beneath it. Whether the instance is restarted afterwards is not affected by that.
//
// limit is the deadline of the push and of the restart, each (zero: none). A push abandoned at it is not followed by the restart.
func pushFile(server incus.InstanceServer, r Resource, limit time.Duration) error {
	err := incusapi.BoundedDo(limit, fmt.Sprintf("pushing %s to %s", r.Path, r.Instance), func() error { return placeFileAt(server, r) })
	if err != nil {
		return err
	}
	if r.Derive != "" {
		// A derived file is read back: a write that did not take must not look like a name that is routed. (Before the restart, which would
		// otherwise hide it.)
		back, err := planPlacedFile(scopedServer(server, r), r)
		if err != nil {
			return fmt.Errorf("the write to %s on %s succeeded but reading it back failed: %w", r.Path, r.Instance, err)
		}
		if back.Action != ActionNone {
			return fmt.Errorf("the write to %s on %s succeeded but the file does not hold what was written", r.Path, r.Instance)
		}
	}
	if !r.Restart {
		return nil
	}
	return run.EnsureRunningWithin(scopedServer(server, r), r.Instance, limit)
}

// placeFile writes r.Content to r.Path inside r.Instance and does nothing more: no restart. It is what pushFile does first (through placeFileAt), and
// what creating an instance does for its files before the first start (the instance is stopped then, and takes the push) -- except the files a
// disk-device mount would hide, which go into the volume (placeVolumeFile) or wait for the start.
func placeFile(server incus.InstanceServer, r Resource) error {
	s := scopedServer(server, r)
	err := pushCreatingParents(path.Clean(r.Path), instanceDirOps(s, r.Instance), func() error {
		return s.CreateInstanceFile(r.Instance, r.Path, incus.InstanceFileArgs{
			Content:   strings.NewReader(r.Content),
			Type:      "file",
			WriteMode: "overwrite",
			Mode:      0o644,
		})
	})
	if err != nil {
		return fmt.Errorf("pushing %s to %s: %w", r.Path, r.Instance, err)
	}
	return nil
}
