package resolve

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/archive"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/run"
)

// Apply walks resources level by level (see Levels): every resource
// within one level runs concurrently, since none of them depend on each
// other, and the next level only starts once the current one finishes
// entirely. This is the same parallel-within-a-level, serial-across-
// levels shape we watched Terraform's own engine execute against the
// real nextcloud-tink-test stack -- nobody writes the order by hand.
func Apply(socket string, resources []Resource) ([]string, error) {
	levels, err := Levels(resources)
	if err != nil {
		return nil, err
	}

	server, err := incusapi.Connect(socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to incus: %w", err)
	}

	var actions []string
	var mu sync.Mutex
	note := func(format string, args ...any) {
		mu.Lock()
		actions = append(actions, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	for _, level := range levels {
		var wg sync.WaitGroup
		errs := make([]error, len(level))
		for i, r := range level {
			wg.Add(1)
			go func(i int, r Resource) {
				defer wg.Done()
				errs[i] = applyOne(server, r, note)
			}(i, r)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				return actions, fmt.Errorf("%s/%s: %w", level[i].Kind, level[i].Name, err)
			}
		}
	}
	return actions, nil
}

func applyOne(server incus.InstanceServer, r Resource, note func(string, ...any)) error {
	plan, err := planOne(server, r)
	if err != nil {
		return err
	}
	switch plan.Action {
	case ActionNone:
		note("%s/%s: no changes", r.Kind, r.Name)
		return nil
	case ActionCreate:
		if err := createOne(server, r); err != nil {
			return err
		}
		note("%s/%s: created", r.Kind, r.Name)
		return nil
	case ActionUpdate:
		if err := updateOne(server, r); err != nil {
			return err
		}
		if r.Kind == KindInstance && r.Restart {
			note("%s/%s: updated and restarted (%v)", r.Kind, r.Name, plan.Changes)
			return nil
		}
		note("%s/%s: updated (%v)", r.Kind, r.Name, plan.Changes)
		return nil
	}
	return nil
}

func createOne(server incus.InstanceServer, r Resource) error {
	switch r.Kind {
	case KindProject:
		return server.CreateProject(api.ProjectsPost{
			Name:       r.Name,
			ProjectPut: api.ProjectPut{Config: r.Config},
		})
	case KindProfile:
		return scopedServer(server, r).CreateProfile(api.ProfilesPost{
			Name:       r.Name,
			ProfilePut: api.ProfilePut{Config: r.Config, Devices: r.Devices},
		})
	case KindStorageVolume:
		pool := r.Pool
		if pool == "" {
			pool = "default"
		}
		return scopedServer(server, r).CreateStoragePoolVolume(pool, api.StorageVolumesPost{
			Name:        r.Name,
			Type:        "custom",
			ContentType: "filesystem",
		})
	case KindInstance:
		return createInstance(server, r)
	case KindFile:
		return pushFile(server, r)
	case KindIncus:
		return runIncus(r)
	case KindImage:
		return createImage(server, r)
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
func createImage(server incus.InstanceServer, r Resource) error {
	s := scopedServer(server, r)

	metaYAML, err := yaml.Marshal(api.ImageMetadata{
		Architecture: r.Architecture,
		CreationDate: time.Now().Unix(),
		Properties:   r.Properties,
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

	op, err := s.CreateImage(api.ImagesPost{Filename: "metadata.yaml"}, &incus.ImageCreateArgs{
		MetaFile:   metaBuf,
		MetaName:   "metadata.yaml",
		RootfsFile: rootfs,
		RootfsName: filepath.Base(r.Source),
		Type:       imageType,
	})
	if err != nil {
		return fmt.Errorf("importing image for %s: %w", r.Name, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("importing image for %s: %w", r.Name, err)
	}

	fingerprint, _ := op.Get().Metadata["fingerprint"].(string)
	if fingerprint == "" {
		return fmt.Errorf("importing image for %s: daemon returned no fingerprint", r.Name)
	}

	return s.CreateImageAlias(api.ImageAliasesPost{
		ImageAliasesEntry: api.ImageAliasesEntry{
			Name:                 r.Alias,
			ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: fingerprint},
		},
	})
}

// createInstance is the concrete proof of the reuse claim in
// docs/resolver-architecture.md: the actual leaf operation -- create,
// then apply config/devices, then start -- is tink run's own exported
// Create/ApplyConfig/EnsureRunning, not a reimplementation. The
// first-boot-config-ordering fix tink run needed for Postgres/Nextcloud
// comes along for free.
func createInstance(server incus.InstanceServer, r Resource) error {
	spec := &run.Spec{
		Name:     r.Name,
		Image:    r.Image,
		Config:   r.Config,
		Devices:  r.Devices,
		Profiles: r.Profiles,
		VM:       r.VM,
	}
	if err := run.Create(spec, r.Project); err != nil {
		return err
	}
	s := scopedServer(server, r)
	if err := run.ApplyConfig(s, spec); err != nil {
		return err
	}
	return run.EnsureRunning(s, spec.Name)
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
func updateInstance(server incus.InstanceServer, r Resource) error {
	spec := &run.Spec{Name: r.Name, Config: r.Config, Devices: r.Devices}
	if err := run.ApplyConfig(server, spec); err != nil {
		return err
	}
	if !r.Restart {
		return nil
	}
	return run.EnsureRunning(server, r.Name)
}

func updateOne(server incus.InstanceServer, r Resource) error {
	s := scopedServer(server, r)
	switch r.Kind {
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
	case KindInstance:
		return updateInstance(s, r)
	case KindFile:
		// Pushing is the same operation whether the file is new or just
		// changed -- CreateInstanceFile overwrites either way.
		return pushFile(server, r)
	default:
		// Not exercised in this spike: storage volumes and projects are
		// create-only so far, matching every real deployment on this
		// platform to date -- nothing has ever needed to mutate one in
		// place.
		return fmt.Errorf("update not implemented for kind %q in this spike", r.Kind)
	}
}

// pushFile writes r.Content to r.Path inside r.Instance, then -- only if
// r.Restart asks for it -- restarts that instance via tink run's own
// EnsureRunning, reused exactly as-is (it already knows how to pick
// start-vs-restart from current status and retry on Incus's transient
// "instance is busy" rejection). This is the actual fix for the one gap
// found live building this same stack with Terraform: a pushed file
// alone doesn't make a running process notice it changed.
func pushFile(server incus.InstanceServer, r Resource) error {
	s := scopedServer(server, r)
	err := s.CreateInstanceFile(r.Instance, r.Path, incus.InstanceFileArgs{
		Content:   strings.NewReader(r.Content),
		Type:      "file",
		WriteMode: "overwrite",
		Mode:      0o644,
	})
	if err != nil {
		return fmt.Errorf("pushing %s to %s: %w", r.Path, r.Instance, err)
	}
	if !r.Restart {
		return nil
	}
	return run.EnsureRunning(s, r.Instance)
}
