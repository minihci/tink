package resolve

import (
	"fmt"
	"strings"
	"sync"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

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
		if r.Kind == KindInstance {
			// In-place instance reconfiguration is out of scope here,
			// matching tink run's own DESIGN.md non-goals -- report the
			// drift rather than silently ignoring or guessing at it.
			note("%s/%s: drift detected, not applied (in-place instance updates are out of scope for this spike): %v", r.Kind, r.Name, plan.Changes)
			return nil
		}
		if err := updateOne(server, r); err != nil {
			return err
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
	default:
		return fmt.Errorf("unknown kind %q", r.Kind)
	}
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
