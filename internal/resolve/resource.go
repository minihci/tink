// Package resolve is a spike: a lightweight, tink-native version of the
// resolver half of the architecture in docs/resolver-architecture.md --
// scoped deliberately to what this platform actually uses (project,
// profile, storage-volume, instance), stateless (diffs live Incus state
// directly, no separate state file to protect secrets in), and using
// explicit name-matching for structural dependencies (an instance's own
// Project/Profiles/volume-backed devices) instead of a Terraform-style
// expression language with deferred-value resolution -- real usage on
// this platform has never needed a computed cross-resource value, only
// "this must exist before that."
package resolve

// Kind identifies which Incus object a Resource describes. Scoped to
// exactly what this platform uses today; not a general-purpose registry
// the way incus-apply's or Terraform's own resource-type list is.
type Kind string

const (
	KindProject       Kind = "project"
	KindProfile       Kind = "profile"
	KindStorageVolume Kind = "storage-volume"
	KindInstance      Kind = "instance"
	KindFile          Kind = "file"
)

// kindPriority orders resource creation by type, matching the same
// coarse ordering incus-apply's own internal/resource/sort.go uses:
// container objects before the things placed inside them. File is last
// on purpose -- it can only ever be pushed into an instance that already
// exists and is running.
var kindPriority = map[Kind]int{
	KindProject:       0,
	KindProfile:       1,
	KindStorageVolume: 1,
	KindInstance:      2,
	KindFile:          3,
}

// Resource is the shared, resolver-agnostic description of one thing
// that should exist. Every field a translator (tink run's own
// flags.go, or a future Kubernetes-Pod-spec translator) would produce
// lands here before this package ever touches Incus.
type Resource struct {
	Kind    Kind
	Name    string
	Project string // empty means the daemon's own default project

	// DependsOn names resources this one must wait for, beyond what's
	// already inferable from Project/Profiles/device sources below --
	// e.g. "this instance's installer needs that instance actually
	// running, not just created" (see nextcloud-app depending on
	// nextcloud-db in the real stack this spike was built to explain).
	DependsOn []string

	// Instance-only.
	Image    string
	Profiles []string
	Pool     string // storage pool a storage-volume resource lives in

	// File-only: push Content to Path inside the named Instance. Restart
	// controls whether the target instance is restarted after a push
	// that actually changed something -- needed for exactly the case
	// found live building this stack with Terraform: Caddy (running with
	// admin off, so no API-based reload) keeps serving stale config
	// otherwise. Not automatic for every file, since not every process a
	// file might be pushed to needs a restart to notice -- an explicit,
	// declared choice per resource, the same way Terraform's own fix for
	// this needed its own separate, explicitly-triggered step rather
	// than something the file upload did unconditionally.
	Instance string
	Path     string
	Content  string
	Restart  bool

	Config  map[string]string
	Devices map[string]map[string]string
}

// dependencies returns every resource name this one must wait for:
// explicit DependsOn plus structural references inferred by simple name
// matching against the full resource set -- the Project this resource
// lives in, each profile it lists, and each device's volume source, if
// any of those names a resource in the same apply. This is the
// deliberately small substitute for Terraform's own expression-based
// reference tracking: no unknown-value propagation, no AST, just "does
// this field's string value match another resource's Name."
func (r Resource) dependencies(all map[string]*Resource) []string {
	seen := map[string]bool{}
	var deps []string
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		if other, ok := all[name]; ok && other.Name != r.Name {
			seen[name] = true
			deps = append(deps, name)
		}
	}

	add(r.Project)
	add(r.Instance)
	for _, p := range r.Profiles {
		add(p)
	}
	for _, dev := range r.Devices {
		if dev["type"] == "disk" && dev["source"] != "" {
			add(dev["source"])
		}
	}
	for _, d := range r.DependsOn {
		add(d)
	}
	return deps
}
