package resolve

import (
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/backupext"
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
	// ActionDeferred: apply leaves this resource alone this time, on purpose, and that is not a failure. It is a file whose instance is not running and
	// whose place is a mount that only a running instance can take it into (filemount.go). Its warning says why; the next apply that finds the
	// instance running writes it. Neither "no changes" (nobody looked in the right place) nor blocked (it does not fail the apply).
	ActionDeferred
)

// PlannedResource is one resource's computed action, with human-readable
// changes for preview -- the same idea as tink deploy's own dry-run
// output, generalized across resource kinds.
type PlannedResource struct {
	Resource Resource
	Action   Action
	Changes  []string
	// Drift: confirmed image drift. Blocked: why apply will not converge this
	// resource. Warnings: things that could not be verified or were ignored.
	Drift    []string
	Blocked  []string
	Warnings []string
	// Notes: facts about what apply will do that are not problems, such as a file a mount hides from the instance's first start. Informational only.
	Notes []string
	// Extras: for an instance, profile, storage volume, network or project that exists and was planned WithExtras, what it carries that the stack
	// does not declare. Informational only: it never changes the action, and nothing is removed because of it. Nil when not asked for, or for
	// any other kind.
	Extras *Extras

	// Ownership is whose the object is, for a kind that carries the stamp, an object that exists and a stack with a name (ownership.go). It is its own
	// dimension: it never makes an action, a change line or an exit status, however it reads.
	Ownership StampState
	// live is the config (an image's properties) of the object as the plan read it, for withOwnership to classify. Unexported and cleared once used: it
	// can hold values that must not be printed.
	live     map[string]string
	liveRead bool
	// liveContent is a file's content as the plan read it, for the guards of a derived file (derivedfile.go). Unexported: it can hold anything.
	liveContent string
}

// Plan computes what would happen for each resource without touching
// Incus. Deliberately stateless: every check queries the daemon directly
// rather than comparing against a separately stored record of what was
// created last time -- Incus's own API is always cheap and complete to
// read, unlike many cloud provider APIs Terraform has to work around, so
// there's nothing a state file would buy here that a live read doesn't
// already give for free. The real cost of that choice: there's no
// "generated at create time" value to remember (an auto-assigned IP,
// say) -- tink accepts that limitation, matching how every real
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
			// Planning only reads, so an interrupted run stops here without a half-done change to account for.
			if err := opts.context().Err(); err != nil {
				errs[i] = fmt.Errorf("planning interrupted: %w", err)
				return
			}
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
// project -- and a network or a network ACL, which are server-global.
func scopedServer(server incus.InstanceServer, r Resource) incus.InstanceServer {
	if r.Kind != KindProject && r.Kind != KindNetwork && r.Kind != KindNetworkACL && r.Project != "" {
		return server.UseProject(r.Project)
	}
	return server
}

// planKind is the plan of one resource: what its kind decides (planKindBody) and, beside it and never part of it, whose the object is (ownership.go).
func planKind(server incus.InstanceServer, r Resource, opts PlanOptions) (PlannedResource, error) {
	p, err := planKindBody(server, r, opts)
	if err != nil {
		return p, err
	}
	return p.withOwnership(opts.stack), nil
}

func planKindBody(server incus.InstanceServer, r Resource, opts PlanOptions) (PlannedResource, error) {
	// A secret that cannot be resolved blocks the WHOLE resource, never part of it.
	if len(r.SecretProblems) > 0 {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: r.SecretProblems}, nil
	}
	if unexpandedSecretRef(r) {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{
			"holds a ${secret:...} reference that was never expanded (this is a tink bug: refusing to apply the literal text as a value)"}}, nil
	}
	s := scopedServer(server, r)
	venv := volumeEnv{targets: opts.targets, stack: opts.stack, helperReads: opts.helperReads, helperLabel: opts.helperLabel, helperRemotes: opts.helperRemotes}
	switch r.Kind {
	case KindHelper:
		// the helper is found in every project, so it is read with the unscoped server
		return planHelper(server, r, time.Now(), opts)
	case KindProject:
		p, err := planProject(s, r)
		return withOtherExtras(s, r, venv, opts, p, err)
	case KindNetwork:
		p, err := planNetwork(s, r)
		if err != nil {
			return p, err
		}
		p.Warnings = append(p.Warnings, aclReferenceWarnings(s, r, opts.acls)...)
		return withOtherExtras(s, r, venv, opts, p, nil)
	case KindNetworkACL:
		return planNetworkACL(s, r)
	case KindProfile:
		p, err := planProfile(s, r)
		if err != nil {
			return p, err
		}
		p.Warnings = append(p.Warnings, aclReferenceWarnings(s, r, opts.acls)...)
		return withOtherExtras(s, r, venv, opts, p, nil)
	case KindStorageVolume:
		p, err := planStorageVolume(s, r, venv)
		return withOtherExtras(s, r, venv, opts, p, err)
	case KindBackupTarget:
		// A declaration only: there is no Incus object to create or converge.
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	case KindInstance:
		p, err := planInstance(s, r, opts)
		if err != nil {
			return p, err
		}
		p.Warnings = append(p.Warnings, ociNICWarnings(r, opts.imageRemotes(), opts.profiles)...)
		p.Warnings = append(p.Warnings, natProxyWarnings(r, opts.profiles)...)
		p.Warnings = append(p.Warnings, opts.listenerNotes[r.Name]...)
		p.Warnings = append(p.Warnings, rootDiskWarnings(s, r, opts.profiles)...)
		p.Warnings = append(p.Warnings, aclReferenceWarnings(s, r, opts.acls)...)
		return p, nil
	case KindFile:
		return planPlacedFile(s, r)
	case KindIncus:
		return planIncus(r)
	case KindImage:
		return planImage(s, r, opts.stack != "")
	case KindExec:
		return planExec(opts.context(), s, r)
	default:
		return PlannedResource{}, fmt.Errorf("unknown kind %q", r.Kind)
	}
}

// withOtherExtras adds, to the plan of a profile, storage volume, network or project that exists, what it carries that the stack does not
// declare (see Extras), when planning was asked for it. It is informational: the plan's action and changes are left as they were.
func withOtherExtras(server incus.InstanceServer, r Resource, env volumeEnv, opts PlanOptions, p PlannedResource, err error) (PlannedResource, error) {
	if err != nil || !opts.extras || p.Action == ActionCreate {
		return p, err
	}
	ex, err := otherExtras(server, r, env)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading what the live %s carries: %w", r.Kind, err)
	}
	p.Extras = ex
	return p, nil
}

// planProject only ever creates. This platform's own memory already
// documents why: a project's feature flags are a one-way door that locks
// the moment it holds any instances, so there's no real in-place update
// story to diff toward, and none of this platform's real usage has ever
// needed one.
func planProject(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, found, err := incusapi.LookupProject(server, r.Name)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live project: %w", err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	return observed(PlannedResource{Resource: r, Action: ActionNone}, current.Config), nil
}

func planProfile(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, found, err := incusapi.LookupProfile(server, r.Name)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live profile: %w", err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	changes := diffConfig(current.Config, r.Config, nil)
	changes = append(changes, diffDevices(current.Devices, r.Devices)...)
	if len(changes) == 0 {
		return observed(PlannedResource{Resource: r, Action: ActionNone}, current.Config), nil
	}
	return observed(PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes}, current.Config), nil
}

func planStorageVolume(server incus.InstanceServer, r Resource, env volumeEnv) (PlannedResource, error) {
	pool := r.Pool
	if pool == "" {
		pool = "default"
	}
	current, _, _, err := incusapi.LookupVolume(server, pool, "custom", r.Name)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live volume in pool %q: %w", pool, err)
	}
	// not found leaves current nil: decideVolume plans a create (or blocks it)
	// A policy this tink would write, and the helper cannot read, is not written: the volume's copies would stop and nothing would say so.
	if why := env.helperCannotRead(); why != "" {
		policy := backupext.Get().Keys().Policy
		if want, _, err := volumeBackupConfig(r, env); err == nil && want[policy] != "" && (current == nil || current.Config[policy] != want[policy]) {
			return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{why}}, nil
		}
	}
	p := decideVolume(r, current, env)
	if current != nil {
		p = observed(p, current.Config)
	}
	p.Warnings = append(p.Warnings, backupWarnings(r, env.targets)...)
	p.Warnings = append(p.Warnings, env.helperRemoteWarnings(r)...)
	return p, nil
}

// planFile reads the file's actual current byte content from inside the
// target instance and compares it directly -- the same live-diff
// principle as every other kind here, just against instance-file
// content instead of instance/profile config. A file under a mount of an
// instance that is not running is not read from here (planPlacedFile).
func planFile(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	rc, _, found, err := incusapi.LookupInstanceFile(server, r.Instance, r.Path)
	if err != nil {
		// Not "absent": planning a create here would have apply overwrite a file it merely failed to read
		// (and, with restart: true, restart the instance).
		return PlannedResource{}, fmt.Errorf("reading current content of %s on %s: %w", r.Path, r.Instance, err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	if rc == nil {
		return PlannedResource{}, fmt.Errorf("%s on %s is a directory", r.Path, r.Instance)
	}
	defer rc.Close()

	current, err := io.ReadAll(rc)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading current content of %s on %s: %w", r.Path, r.Instance, err)
	}

	if string(current) == r.Content {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}
	if r.Seed {
		return seededFile(r), nil
	}
	return PlannedResource{Resource: r, Action: ActionUpdate, Changes: []string{fmt.Sprintf("content of %s on %s differs", r.Path, r.Instance)}, liveContent: string(current)}, nil
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
//
// readStamp says to also read the image the alias points at, for its properties, where the ownership stamp is (ownership.go); it is off for a stack with
// no name, which has no use for them.
func planImage(server incus.InstanceServer, r Resource, readStamp bool) (PlannedResource, error) {
	alias, _, found, err := incusapi.LookupImageAlias(server, r.Alias)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live image alias %q: %w", r.Alias, err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}
	p := PlannedResource{Resource: r, Action: ActionNone}
	if !readStamp {
		return p, nil
	}
	img, _, imgFound, err := incusapi.LookupImage(server, alias.Target)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live image %s behind the alias %q: %w", alias.Target, r.Alias, err)
	}
	if !imgFound { // an alias that points at nothing: there is nothing to read a stamp from, and nothing here is changed
		return p, nil
	}
	return observed(p, img.Properties), nil
}

// diffConfig reports desired keys that are missing or different in
// current. Deliberately one-directional: a live object carries plenty of
// keys we don't own (image.*, volatile.*) and never reports those as
// drift, the same "only touch what we set" idea tink run's own
// ApplyConfig already uses.
//
// A desired "" for a key the live object does not have is not a difference. Incus never stores an empty config value: it reads one as "unset" and
// writes nothing (#214), for every kind this function serves, so a "" that was applied reads back as a missing key and would otherwise be reported
// again on every plan. A desired "" against a live key that has a value is still a change; applying it removes the key.
//
// Neither side of a change to a sensitive key is printed: not the new value, and not the old one
// (replacing a plain password with a ${secret:} reference would otherwise print the old one in
// clear). A key is sensitive when its name looks like one (secrets.SensitiveKey) or when it is in
// hidden, the keys whose value was expanded from a secret.
func diffConfig(current, desired map[string]string, hidden map[string]bool) []string {
	var changes []string
	for k, v := range desired {
		cur, ok := current[k]
		if ok && cur == v || !ok && v == "" {
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
// current, one line per field that differs. Devices merge as whole blocks by
// name, not field-by-field (confirmed against real Incus docs while building
// tink run), so applying replaces the whole device; but a reader asking "what
// is different" is helped by seeing which field, so that is what is printed.
func diffDevices(current, desired map[string]map[string]string) []string {
	var changes []string
	for name, dev := range desired {
		cur, exists := current[name]
		if exists && reflect.DeepEqual(cur, dev) {
			continue
		}
		if !exists {
			changes = append(changes, fmt.Sprintf("device.%s: added (%s)", name, deviceFields(dev)))
			continue
		}
		for _, k := range sortedKeys(unionKeys(cur, dev)) {
			have, hasHave := cur[k]
			want, hasWant := dev[k]
			switch {
			case hasHave && hasWant && have != want:
				changes = append(changes, fmt.Sprintf("device.%s.%s: %q -> %q", name, k, have, want))
			case !hasHave:
				changes = append(changes, fmt.Sprintf("device.%s.%s: (unset) -> %q", name, k, want))
			case !hasWant:
				changes = append(changes, fmt.Sprintf("device.%s.%s: %q -> (unset)", name, k, have))
			}
		}
	}
	sort.Strings(changes)
	return changes
}

// deviceFields writes a device's fields as k=v pairs, type first.
func deviceFields(dev map[string]string) string {
	var parts []string
	if t, ok := dev["type"]; ok {
		parts = append(parts, "type="+t)
	}
	for _, k := range sortedKeys(dev) {
		if k != "type" {
			parts = append(parts, k+"="+dev[k])
		}
	}
	return strings.Join(parts, " ")
}

func unionKeys(a, b map[string]string) map[string]bool {
	u := map[string]bool{}
	for k := range a {
		u[k] = true
	}
	for k := range b {
		u[k] = true
	}
	return u
}

// sortedKeys returns m's keys in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
