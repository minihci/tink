package resolve

import (
	"fmt"
	"io"
	"reflect"
	"sort"
	"sync"

	incus "github.com/lxc/incus/v7/client"
)

// Action is what Plan decided needs to happen for one resource.
type Action int

const (
	ActionNone Action = iota
	ActionCreate
	ActionUpdate
)

// PlannedResource is one resource's computed action, with human-readable
// changes for preview -- the same idea as tink deploy's/tink ingress
// reconcile's own dry-run output, generalized across resource kinds.
type PlannedResource struct {
	Resource Resource
	Action   Action
	Changes  []string
}

// Plan computes what would happen for each resource without touching
// Incus. Deliberately stateless: every check queries the daemon directly
// rather than comparing against a separately stored record of what was
// created last time -- Incus's own API is always cheap and complete to
// read, unlike many cloud provider APIs Terraform has to work around, so
// there's nothing a state file would buy here that a live read doesn't
// already give for free. The real cost of that choice: there's no
// "generated at create time" value to remember (an auto-assigned IP,
// say) -- this spike accepts that limitation, matching how every real
// resource built on this platform so far uses static, human-chosen
// names and addresses anyway.
//
// Every resource is checked concurrently. Unlike Apply, a plan is pure
// reads against live state -- nothing here depends on another resource
// existing yet, so unlike Levels there's no ordering to respect, only a
// result slot to fill in.
func Plan(server incus.InstanceServer, resources []Resource) ([]PlannedResource, error) {
	plans := make([]PlannedResource, len(resources))
	errs := make([]error, len(resources))

	var wg sync.WaitGroup
	for i, r := range resources {
		wg.Add(1)
		go func(i int, r Resource) {
			defer wg.Done()
			plans[i], errs[i] = planOne(server, r)
		}(i, r)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", resources[i].Kind, resources[i].Name, err)
		}
	}
	return plans, nil
}

// scopedServer returns server scoped to r's own project, except for a
// project resource itself -- a project is never "inside" another
// project.
func scopedServer(server incus.InstanceServer, r Resource) incus.InstanceServer {
	if r.Kind != KindProject && r.Project != "" {
		return server.UseProject(r.Project)
	}
	return server
}

func planOne(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	s := scopedServer(server, r)
	switch r.Kind {
	case KindProject:
		return planProject(s, r)
	case KindProfile:
		return planProfile(s, r)
	case KindStorageVolume:
		return planStorageVolume(s, r)
	case KindInstance:
		return planInstance(s, r)
	case KindFile:
		return planFile(s, r)
	default:
		return PlannedResource{}, fmt.Errorf("unknown kind %q", r.Kind)
	}
}

// planProject only ever creates. This platform's own memory already
// documents why: a project's feature flags are a one-way door that locks
// the moment it holds any instances, so there's no real in-place update
// story to diff toward, and none of this platform's real usage has ever
// needed one.
func planProject(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	if _, _, err := server.GetProject(r.Name); err != nil {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	return PlannedResource{Resource: r, Action: ActionNone}, nil
}

func planProfile(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, err := server.GetProfile(r.Name)
	if err != nil {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	changes := diffConfig(current.Config, r.Config)
	changes = append(changes, diffDevices(current.Devices, r.Devices)...)
	if len(changes) == 0 {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}
	return PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes}, nil
}

func planStorageVolume(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	pool := r.Pool
	if pool == "" {
		pool = "default"
	}
	if _, _, err := server.GetStoragePoolVolume(pool, "custom", r.Name); err != nil {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	return PlannedResource{Resource: r, Action: ActionNone}, nil
}

// planInstance reports drift but Apply doesn't act on ActionUpdate for
// instances in this spike -- tink run itself has no in-place
// reconfigure-an-existing-instance story either (see its own DESIGN.md
// non-goals), so this matches rather than exceeds that scope.
func planInstance(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, err := server.GetInstance(r.Name)
	if err != nil {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	changes := diffConfig(current.Config, r.Config)
	changes = append(changes, diffDevices(current.Devices, r.Devices)...)
	if len(changes) == 0 {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}
	return PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes}, nil
}

// planFile reads the file's actual current byte content from inside the
// target instance and compares it directly -- the same live-diff
// principle as every other kind here, just against instance-file
// content instead of instance/profile config.
func planFile(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	rc, _, err := server.GetInstanceFile(r.Instance, r.Path)
	if err != nil {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	defer rc.Close()

	current, err := io.ReadAll(rc)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading current content of %s on %s: %w", r.Path, r.Instance, err)
	}

	if string(current) == r.Content {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}
	return PlannedResource{Resource: r, Action: ActionUpdate, Changes: []string{fmt.Sprintf("content of %s on %s differs", r.Path, r.Instance)}}, nil
}

// diffConfig reports desired keys that are missing or different in
// current. Deliberately one-directional: a live object carries plenty of
// keys we don't own (image.*, volatile.*) and never reports those as
// drift, the same "only touch what we set" idea tink run's own
// ApplyConfig already uses.
func diffConfig(current, desired map[string]string) []string {
	var changes []string
	for k, v := range desired {
		if cur, ok := current[k]; !ok || cur != v {
			changes = append(changes, fmt.Sprintf("config.%s: %q -> %q", k, current[k], v))
		}
	}
	sort.Strings(changes)
	return changes
}

// diffDevices reports desired devices that are missing or different from
// current. Devices merge as whole blocks by name, not field-by-field
// (confirmed against real Incus docs while building tink run), so a
// device is either exactly right or needs full replacement -- there's no
// partial-field diff to compute.
func diffDevices(current, desired map[string]map[string]string) []string {
	var changes []string
	for name, dev := range desired {
		if !reflect.DeepEqual(current[name], dev) {
			changes = append(changes, fmt.Sprintf("device.%s: %v -> %v", name, current[name], dev))
		}
	}
	sort.Strings(changes)
	return changes
}
