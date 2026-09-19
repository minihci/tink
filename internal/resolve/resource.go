// Package resolve is a spike: a lightweight, tink-native version of the
// resolver half of the architecture in docs/resolver-architecture.md --
// scoped deliberately to what this platform actually uses (project,
// profile, storage-volume, instance, file, incus, image, exec), stateless
// (diffs live Incus state directly, no separate state file to protect
// secrets in) with one narrow, explicit exception -- an exec resource
// with no observable Check falls back to a Triggers hash recorded on its
// own target Instance via the same API everything else here already
// uses, never a new artifact on disk; see Resource.Triggers' own doc
// comment for why that's scoped to just this one field rather than a
// general state store -- and using
// explicit name-matching for structural dependencies (an instance's own
// Project/Profiles/volume-backed devices) instead of a Terraform-style
// expression language with deferred-value resolution -- real usage on
// this platform has never needed a computed cross-resource value, only
// "this must exist before that."
package resolve

import "time"

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
	KindIncus         Kind = "incus"
	KindImage         Kind = "image"
	KindExec          Kind = "exec"
)

// kindPriority orders resource creation by type, matching the same
// coarse ordering incus-apply's own internal/resource/sort.go uses:
// container objects before the things placed inside them. File is last
// on purpose -- it can only ever be pushed into an instance that already
// exists and is running. KindIncus defaults to the same tier as
// profile/storage-volume (a prerequisite, not a consequence) since its
// most common real use is preparing something an instance needs before
// it exists (importing a VM image under a local alias, say) -- an incus
// resource that instead needs to run after an instance is up states
// that with an explicit depends_on, the same way any other real
// ordering need on this platform already does; this default is only a
// tiebreak among resources with no dependency relationship to each
// other.
var kindPriority = map[Kind]int{
	KindProject:       0,
	KindProfile:       1,
	KindStorageVolume: 1,
	KindIncus:         1,
	KindImage:         1,
	KindInstance:      2,
	KindFile:          3,
	KindExec:          4,
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
	VM       bool   // create a virtual machine instead of a container (passes --vm to incus init, same as tink run's own flag)
	Pool     string // storage pool a storage-volume resource lives in

	// File-only: push Content to Path inside the named Instance.
	Instance string
	Path     string
	Content  string

	// Restart controls whether converging this resource also restarts
	// the instance it affects, for the cases where Incus doesn't apply
	// the change live on its own. Shared between two kinds, both found
	// live rather than assumed upfront:
	//   - File: restart the target Instance after a push that actually
	//     changed something -- Caddy (running with admin off, so no
	//     API-based reload) keeps serving stale config otherwise, the
	//     original real case (see docs/resolver-architecture.md's "one
	//     real gap, found and fixed" section).
	//   - Instance: restart after an in-place config/device update --
	//     most device changes (a NIC's ipv4.address, say) don't take
	//     effect on an already-running instance until it restarts;
	//     confirmed live rebuilding this platform's own haos test
	//     project, where a profile's static-IP change alone left the
	//     running VM on its old DHCP lease until an explicit restart.
	// False by default in both cases -- resolve never bounces a running
	// instance you didn't explicitly say it's fine to. A config/device
	// change Incus itself refuses to apply to a running instance at all
	// (some keys require it stopped first) surfaces as a plain apply
	// error either way; Restart doesn't attempt a stop/update/start
	// sequence -- that's real downtime, a bigger and more disruptive
	// decision than a restart, and out of scope for what this field
	// does.
	Restart bool

	// Incus-only: the escape hatch for anything the `incus` CLI itself can
	// do that resolve has no first-class resource for -- named, not a
	// general scripting layer, matching the one real precedent this
	// platform already found a need for while evaluating Terraform (see
	// docs/resolver-architecture.md's "one real gap, found and fixed"
	// section): importing a VM image under a local alias is exactly this
	// shape of problem, deliberately not solved by teaching resolve or
	// tink run about image building/registry management (an explicit
	// non-goal -- see internal/run/DESIGN.md). Both Check and Command are
	// argv for the `incus` binary specifically (exec.Command("incus",
	// ...), never a shell) -- narrower on purpose than a bare shell
	// string: this can only ever do what the incus CLI itself can do,
	// nothing else. Check runs first; a zero exit means already converged
	// (ActionNone) and Command never runs. A non-zero exit means Command
	// runs to converge it. Both are required -- an incus resource with no
	// Check would re-run Command on every apply, which is exactly the
	// "was this already done" question every other resource kind here
	// answers by reading live Incus state instead.
	//
	// Exec-only: Check and Command are reused here for the same
	// zero-exit-means-converged convention, but run *inside* Instance
	// over Incus's own WebSocket exec (ExecInstance) instead of shelling
	// out to the incus binary on the host resolve itself runs on -- the
	// real primitive this needed (a USB-mode-switched device's driver
	// finishing its bind, a Caddy reload after a Caddyfile push) lives on
	// the guest, not the host. See Resource.Triggers' own doc comment for
	// the one case Check can't cover and what Exec does instead.
	Check   []string
	Command []string

	// Exec-only, and mutually exclusive with Check (yaml.go enforces
	// exactly one of the two, never both, never neither -- an exec
	// resource with no way at all to tell "already done" from "needs to
	// run" would silently re-run Command on every apply, the same
	// footgun Check already exists to close for kind: incus). Triggers
	// exists for the narrower case Check can't reach: a Command whose
	// effect leaves nothing live to inspect afterward (a signal, not a
	// state change -- the Caddy reload this platform already hit once,
	// documented in docs/resolver-architecture.md's "one real gap, found
	// and fixed" section, needed exactly this and got it from Terraform's
	// state file at the time; internal/ingress's own real Caddy reload,
	// by contrast, never needed this at all -- its apply() diffs its own
	// generated route files against what's already on disk, a Check in
	// every way that matters, just computed by the caller before apply()
	// is ever called rather than passed in as a field here). Converging
	// here still costs a state file nowhere else in resolve pays -- see
	// execConverged's own doc comment in exec.go for what that costs and
	// why it's scoped to only this one field rather than reopening the
	// package doc comment's stateless claim generally: a sha256 of
	// Triggers joined by "\x00" is
	// stored as a user.tink.exec.<Name>.trigger-hash config key on
	// Instance itself (through the same Incus API every other resource
	// here already reads/writes, not a new artifact on disk) and compared
	// on the next plan/apply. A zero-exit Check is preferred wherever one
	// can be written at all, precisely because it re-derives "already
	// done" from live reality every time and so self-heals from a
	// Command whose effect can be undone by something outside resolve's
	// view entirely -- confirmed live, not hypothetical: the aic8800 USB
	// WiFi dongle this was built against reverts its own multi-stage
	// mode-switch back to square one on nothing more than the owning
	// VM's restart, which a remembered "already ran" marker would have
	// stayed wrongly confident about.
	Triggers []string

	// Exec-only. Overrides incusapi's own default agent-boot-readiness
	// retry budget (see DefaultAgentRetryAttempts/DefaultAgentRetryDelay's
	// own doc comment) for this specific instance -- zero means "use the
	// default." A single duration, not separate attempts/delay knobs: the
	// 1-second cadence is a reasonable default in its own right (nothing
	// here shares a resource that fixed polling would overload, and there
	// was no measurement behind picking any other shape), the number that
	// actually varies by guest is the total budget, and a YAML author
	// naming a specific instance is exactly who'd know it boots slower
	// than the 11 seconds this was tuned against -- not something worth
	// gating behind a tink recompile. AgentTimeout is converted to a
	// whole number of DefaultAgentRetryDelay-sized attempts (rounded up)
	// in exec.go's agentRetry, so a caller reasons about one number in
	// wall-clock terms instead of two coupled, cadence-shaped ones.
	AgentTimeout time.Duration

	// Image-only: wraps an already-complete VM disk image (a qcow2 or
	// similar file -- never built from a rootfs; that stays out of
	// scope on purpose, the same non-goal internal/run/DESIGN.md already
	// draws around image building/registry management) into Incus's own
	// split-image format and imports it under Alias. Source is resolved
	// relative to this YAML file's own directory at load time, the same
	// convention File's own SourcePath already uses -- a real, named
	// field resolve can anchor, unlike a path buried inside an incus
	// resource's opaque Check/Command argv. Architecture defaults to
	// "x86_64" when left empty.
	//
	// Alias, not Name, is the actual Incus identity checked and created
	// -- Name stays the tink-internal graph identity depends_on/project
	// matching (and, below, an instance's own Image field) uses, the
	// same split every other kind already has between its Incus
	// identity and its tink-graph identity. Presence of Alias is the
	// whole check: an Incus alias is expected to be a stable pointer to
	// one specific artifact by convention -- bake the version into the
	// alias itself (this platform's own haos-x86-64-18.3 does exactly
	// this) rather than ever repointing an existing alias at different
	// content -- so there's no drift to diff beyond "does it exist,"
	// the same create-only reasoning project and storage-volume already
	// use.
	// Properties becomes metadata.yaml's own properties block
	// (description/os/release, ...) -- named for what it actually maps
	// to at the Incus layer (ImagePut.Properties, metadata.yaml's own
	// properties:), not reusing Config's name the way an earlier version
	// of this field did: an image has no "config" concept in Incus at
	// all, only properties, and calling it config: here read as a
	// mismatch against Incus's own vocabulary once pointed out.
	Alias        string
	Source       string
	Architecture string
	Properties   map[string]string

	Config  map[string]string
	Devices map[string]map[string]string
}

// dependencies returns every resource name this one must wait for:
// explicit DependsOn plus structural references inferred by simple name
// matching against the full resource set -- the Project this resource
// lives in, each profile it lists, each device's volume source, and (for
// an instance) the image resource it names, if any of those names a
// resource in the same apply. This is the deliberately small substitute
// for Terraform's own expression-based reference tracking: no
// unknown-value propagation, no AST, just "does this field's string
// value match another resource's Name." An instance's Image is expected
// to hold that Name too, not a raw Incus alias -- resolveImageAlias
// (graph.go) rewrites it to the real alias afterward, once dependencies
// are already computed, so this can treat Image exactly like every
// other reference here rather than needing its own Alias-matching case.
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
	add(r.Image)
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
