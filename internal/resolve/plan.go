package resolve

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"reflect"
	"sort"
	"sync"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/secrets"
)

// Action is what Plan decided needs to happen for one resource.
type Action int

const (
	ActionNone Action = iota
	ActionCreate
	ActionUpdate
	// ActionRebuild: image drift on an on_image_change: rebuild instance.
	ActionRebuild
	// ActionBlocked: differences exist that apply will deliberately not fix.
	ActionBlocked
)

// PlannedResource is one resource's computed action, with human-readable
// changes for preview -- the same idea as tink deploy's/tink ingress
// reconcile's own dry-run output, generalized across resource kinds.
type PlannedResource struct {
	Resource Resource
	Action   Action
	Changes  []string
	// Drift: confirmed image drift. Blocked: why apply will not converge this
	// resource. Warnings: things that could not be verified or were ignored.
	Drift    []string
	Blocked  []string
	Warnings []string
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
	return PlanWithOptions(server, resources, PlanOptions{})
}

// PlanWithOptions is Plan with explicit options.
func PlanWithOptions(server incus.InstanceServer, resources []Resource, opts PlanOptions) ([]PlannedResource, error) {
	if opts.env == nil {
		opts.env = newImageEnv(opts.Offline)
	}
	opts = opts.withTargets(resources)
	plans := make([]PlannedResource, len(resources))
	errs := make([]error, len(resources))

	var wg sync.WaitGroup
	for i, r := range resources {
		wg.Add(1)
		go func(i int, r Resource) {
			defer wg.Done()
			plans[i], errs[i] = planOne(server, r, opts)
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

// isNotFound reports whether err is Incus saying the object does not exist (an HTTP 404). It is the
// only error a planner may read as "absent, plan a create": a 403 (a revoked or restricted client), a
// 5xx or a dropped connection says nothing about whether the object exists, and must stop the plan
// rather than turn into a create that apply would then try to run over a live object.
func isNotFound(err error) bool {
	return api.StatusErrorCheck(err, http.StatusNotFound)
}

func planOne(server incus.InstanceServer, r Resource, opts PlanOptions) (PlannedResource, error) {
	// A secret that cannot be resolved blocks the WHOLE resource, never part of it.
	if len(r.SecretProblems) > 0 {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: r.SecretProblems}, nil
	}
	if unexpandedSecretRef(r) {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{
			"holds a ${secret:...} reference that was never expanded (this is a tink bug: refusing to apply the literal text as a value)"}}, nil
	}
	s := scopedServer(server, r)
	switch r.Kind {
	case KindProject:
		return planProject(s, r)
	case KindProfile:
		return planProfile(s, r)
	case KindStorageVolume:
		return planStorageVolume(s, r, volumeEnv{targets: opts.targets, stack: opts.stack})
	case KindBackupTarget:
		// A declaration only: there is no Incus object to create or converge.
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	case KindInstance:
		return planInstance(s, r, opts)
	case KindFile:
		return planFile(s, r)
	case KindIncus:
		return planIncus(r)
	case KindImage:
		return planImage(s, r)
	case KindExec:
		return planExec(s, r)
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
		if !isNotFound(err) {
			return PlannedResource{}, fmt.Errorf("reading the live project: %w", err)
		}
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	return PlannedResource{Resource: r, Action: ActionNone}, nil
}

func planProfile(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, err := server.GetProfile(r.Name)
	if err != nil {
		if !isNotFound(err) {
			return PlannedResource{}, fmt.Errorf("reading the live profile: %w", err)
		}
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	changes := diffConfig(current.Config, r.Config, nil)
	changes = append(changes, diffDevices(current.Devices, r.Devices)...)
	if len(changes) == 0 {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}
	return PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes}, nil
}

func planStorageVolume(server incus.InstanceServer, r Resource, env volumeEnv) (PlannedResource, error) {
	pool := r.Pool
	if pool == "" {
		pool = "default"
	}
	current, _, err := server.GetStoragePoolVolume(pool, "custom", r.Name)
	if err != nil {
		if !isNotFound(err) {
			return PlannedResource{}, fmt.Errorf("reading the live volume in pool %q: %w", pool, err)
		}
		current = nil // not found: decideVolume plans a create (or blocks it)
	}
	p := decideVolume(r, current, env)
	p.Warnings = append(p.Warnings, backupWarnings(r, env.targets)...)
	return p, nil
}

// planFile reads the file's actual current byte content from inside the
// target instance and compares it directly -- the same live-diff
// principle as every other kind here, just against instance-file
// content instead of instance/profile config.
func planFile(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	rc, _, err := server.GetInstanceFile(r.Instance, r.Path)
	if err != nil {
		if !isNotFound(err) {
			// Not "absent": planning a create here would have apply overwrite a file it merely failed to read
			// (and, with restart: true, restart the instance).
			return PlannedResource{}, fmt.Errorf("reading current content of %s on %s: %w", r.Path, r.Instance, err)
		}
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

// planIncus runs `incus` with r.Check's args to decide whether r.Command
// still needs to run -- no daemon API call involved, unlike every other
// kind here, since an incus resource's whole point is describing
// convergence in terms resolve has no first-class resource for yet (see
// Resource.Check's own doc comment). A zero exit means already converged.
func planIncus(r Resource) (PlannedResource, error) {
	// A kind: incus resource is argv for the local `incus` CLI, against that CLI's own default remote, often
	// with paths on the machine tink runs on. Under --remote it would act on whatever server the CLI happens
	// to point at, not the one tink was told to manage, so it is blocked, not guessed at.
	if incusapi.IsRemote() {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{fmt.Sprintf(
			"kind: incus runs the local `incus` CLI (with local paths), which is not the remote %q tink is pointed at (--remote or $TINK_REMOTE): run this stack on the host, without a remote", incusapi.Remote())}}, nil
	}
	if err := exec.Command("incus", r.Check...).Run(); err != nil {
		return PlannedResource{Resource: r, Action: ActionCreate, Changes: []string{fmt.Sprintf("check failed: %v", err)}}, nil
	}
	return PlannedResource{Resource: r, Action: ActionNone}, nil
}

// planImage checks only presence of Alias -- see Resource.Alias's own
// doc comment for why an Incus alias is expected to be a stable pointer
// to one specific artifact by convention, leaving nothing to diff beyond
// "does it exist," the same reasoning project and storage-volume already
// use to stay create-only.
func planImage(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	if _, _, err := server.GetImageAlias(r.Alias); err != nil {
		if !isNotFound(err) {
			return PlannedResource{}, fmt.Errorf("reading the live image alias %q: %w", r.Alias, err)
		}
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	return PlannedResource{Resource: r, Action: ActionNone}, nil
}

// diffConfig reports desired keys that are missing or different in
// current. Deliberately one-directional: a live object carries plenty of
// keys we don't own (image.*, volatile.*) and never reports those as
// drift, the same "only touch what we set" idea tink run's own
// ApplyConfig already uses.
//
// Neither side of a change to a sensitive key is printed: not the new value, and not the old one
// (replacing a plain password with a ${secret:} reference would otherwise print the old one in
// clear). A key is sensitive when its name looks like one (secrets.SensitiveKey) or when it is in
// hidden, the keys whose value was expanded from a secret.
func diffConfig(current, desired map[string]string, hidden map[string]bool) []string {
	var changes []string
	for k, v := range desired {
		cur, ok := current[k]
		if ok && cur == v {
			continue
		}
		switch {
		case hidden[k] || secrets.SensitiveKey(k):
			verb := "changed"
			if !ok {
				verb = "set"
			}
			changes = append(changes, fmt.Sprintf("config.%s: %s %s", k, secrets.HiddenValue, verb))
		default:
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
