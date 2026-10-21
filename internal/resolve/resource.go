// Package resolve is tink's resolver: a lightweight, tink-native version of the
// resolver half of the architecture in docs/resolver-architecture.md --
// scoped deliberately to what this platform actually uses (project,
// profile, storage-volume, instance, file, incus, image, exec, network, network-acl, and the declaration-only backup-target and publish), stateless
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
//
// # Extension points
//
// The resolver knows about kinds, never about capabilities (docs/roadmap.md, "Three extension points"): a capability reaches it through one of
// three things, and the resolver imports none of the capabilities (TestTheConvergerImportsNoCapability fails if it does). A kind is decoded,
// validated, planned and applied here (yaml.go, validate.go, plan.go, apply.go). A pass derives something into the resources (applyStackProject,
// derivePublishStamps, volumeBackupConfig). A check returns findings beside the resource they concern (decideVolume's warnings).
//
// Where a pass or a check is a capability's own knowledge, the resolver asks it through an interface that a package importing nothing of tink's
// declares, and the capability registers itself with. Today that is backup: backupext holds the shape of a storage volume's backup block and the
// Backup interface (the config keys it owns, validating the block, the snapshot config and copy policy it converges to, the findings about its
// copies and its restore rehearsals), and internal/backupmeta implements it. A program has backup by linking backupmeta (cmd/tink/capabilities.go);
// resolve's own tests do so in backupwiring_test.go. What stays here is what the volume's kind needs whatever the capability says: the dependency of
// a copy on its backup-target, the "no backup declared" warning, the helper's protocol and remotes (plan.go, volumebackup.go), and reading a live
// volume back into a declaration (export.go).
//
// Two imports of packages outside rings 0 and 1 are not capability-free, and the list in imports_test.go says why: run is the instance engine that
// applying an instance is (ring 1), and helper is imported by kind: helper (helperkind.go), which needs a kind extension point before it can move.
package resolve

import (
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupext"
)

// Kind identifies which Incus object a Resource describes. Scoped to
// exactly what this platform uses today; not a general-purpose registry
// the way incus-apply's or Terraform's own resource-type list is.
type Kind string

const (
	KindProject       Kind = "project"
	KindNetwork       Kind = "network"     // a managed bridge; server-global, so it never takes a project (see network.go)
	KindNetworkACL    Kind = "network-acl" // an Incus network ACL; lives in the default project like a network, so it never takes a project (see networkacl.go)
	KindProfile       Kind = "profile"
	KindStorageVolume Kind = "storage-volume"
	KindInstance      Kind = "instance"
	KindFile          Kind = "file"
	KindIncus         Kind = "incus"
	KindImage         Kind = "image"
	KindExec          Kind = "exec"
	KindBackupTarget  Kind = "backup-target"
	KindStack         Kind = "stack"   // the stack's own name; metadata, not a resource (see StackName)
	KindHelper        Kind = "helper"  // the daemon helper (an Incus instance running `tink daemon run`): adopted when present, installed when not (see helperkind.go)
	KindPublish       Kind = "publish" // a name in the site's zone that shows one offer of an instance to named groups; a declaration `tink join` reads, never converged (see publish.go)
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
	KindNetwork:       0, // before the profiles and instances whose NICs name it
	KindNetworkACL:    0, // before the networks, profiles and instances whose security.acls name it
	KindBackupTarget:  0, // pure declaration: nothing to create, but volumes refer to it
	KindStack:         0, // metadata: kept out of the graph, only here so the kind is known
	KindHelper:        2,
	KindPublish:       0, // a declaration read by `tink join`: kept out of the graph like the stack, only here so the kind is known
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
	// nextcloud-db in the real stack this package was first built to explain).
	DependsOn []string

	// Instance-only.
	Image    string
	Profiles []string
	// Ephemeral (instance-only) deletes the instance once it stops, for any reason (Incus's own flag; `tink run --rm`).
	Ephemeral bool
	VM        bool   // create a virtual machine instead of a container (passes --vm to incus init, same as tink run's own flag)
	Pool      string // storage pool a storage-volume lives in, or the pool a backup-target copies into

	// Backup-target-only. See backuptarget.go: where the target is (Location, the
	// user's claim, used by the 3-2-1 check), how copies get there (Engine), and
	// which Incus remote it is (Remote; empty means another pool on this server).
	Location string
	Engine   string
	Remote   string
	// Address and Fingerprint are an opt-in way for a target to describe the server its Remote names: where it is, and the SHA-256 of its
	// certificate. Only the public half: no credential. Without them a target is just the name, resolved from the Incus client
	// configuration of whoever runs the copy, and the stack says nothing about the server (so it can live in a public repository). With
	// them, plan and the missing-remote error can print the exact command that adds the remote. They are not part of the copy policy
	// written on the volume.
	Address     string
	Fingerprint string

	// Backup (storage-volume-only) is the volume's mandatory answer to "how is
	// this backed up?". Nil means unanswered, which plan BLOCKS. See backupext.VolumeBackup.
	Backup *backupext.VolumeBackup

	// Ownership (stack-only) is how the stack takes the objects that were there before it stamped anything: OwnershipExplicit (the default; `tink plan
	// apply --adopt` does it once) or OwnershipAdoptOnApply (every apply does). See ownership.go.
	Ownership string

	// Publish-only (see publish.go): Instance is the instance that makes the offer, Offer is which of its offers is shown, and Allow lists the GROUPS
	// that may use it. Name is the host label: the published name is Name plus the dns helper's zone.
	Offer string
	Allow []string

	// Helper-only (see helperkind.go): the network the helper's NIC joins, the time zone its schedules are evaluated in, and the Incus remotes the stack says it
	// must be able to reach. Image and Pool are shared with other kinds; the helper's instance name is Name, and its project defaults to helper.DefaultProject.
	Network  string
	Timezone string
	Remotes  []string

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

	// OnImageChange (instance-only) says what apply may do when the image the
	// instance was built from no longer matches Image: "report" (default;
	// the instance is BLOCKED and nothing on it is changed), "ignore" (drift
	// is shown but config still applies), or "rebuild" (converge it by
	// rebuilding the root filesystem -- OCI app containers only, digest-pinned
	// images only). See rebuild.go.
	OnImageChange string

	// SnapshotVolumes (instance-only, rebuild only) snapshots the
	// instance's custom volumes after the stop and before the rebuild.
	SnapshotVolumes bool

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
	// state file at the time). Converging
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

	// Exec-only, and the third of three guards (exactly one of Check, CheckOutput and Triggers). The work is done when CheckOutput.Command's
	// output equals the declared content of the file resource CheckOutput.Equals names. See OutputCheck, and docs/exec-output-check-design.md.
	CheckOutput *OutputCheck

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

	// Exec-only. How long one guest command (check, check_output's command, or command) may run before it is abandoned and the resource
	// fails. Zero means incusapi.DefaultExecTimeout. Without a bound, one check that hangs hangs the whole plan. It is per attempt, so it
	// is separate from AgentTimeout, which is how long to wait for a booting VM's agent.
	Timeout time.Duration

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

	// Description is the description of a network or a network ACL. AllowDisruption (network-only) lets apply change the configuration of a bridge
	// that instances are already using, which can interrupt their traffic; without it such a change is BLOCKED and the instances are named. See network.go.
	Description     string
	AllowDisruption bool

	// Network-ACL-only: the rules, in the order they were declared, with Incus's own field names. A nil list is "not declared" and leaves the
	// live rules of that direction alone; an empty one is "declared, and there are none". See networkacl.go. The direction is from the NIC's
	// point of view, which is Incus's meaning and not tink's ingress proxy.
	Ingress []api.NetworkACLRule
	Egress  []api.NetworkACLRule

	Config  map[string]string
	Devices map[string]map[string]string

	// Defaults (stack-only) is the boilerplate the stack fills into the resources that do not set it. See defaults.go.
	Defaults *StackDefaults

	// Set by ExpandSecrets, never from YAML. SecretKeys are the config keys whose value held a
	// secret reference (no diff prints them); SecretProblems are the reasons a reference could not be
	// resolved, which blocks the whole resource; SecretsExpanded says expansion ran on it.
	SecretKeys      map[string]bool
	SecretProblems  []string
	SecretsExpanded bool
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
	if r.CheckOutput != nil {
		add(r.CheckOutput.Equals) // the exec compares with that file, so it waits for it
	}
	for _, p := range r.Profiles {
		add(p)
	}
	for _, dev := range r.Devices {
		if dev["type"] == "disk" && dev["source"] != "" {
			add(dev["source"])
		}
		// A NIC on a network the stack declares waits for it. Naming one the stack does not declare stays lenient (it may exist on the server
		// already, like incusbr0), the same as a profile or a volume source.
		if dev["type"] == "nic" && dev["network"] != "" {
			add(dev["network"])
		}
	}
	if r.Backup != nil {
		for _, c := range r.Backup.Copies {
			add(c.Target)
		}
	}
	// A network, profile or instance NIC that names an ACL the stack declares waits for it: Incus refuses to attach an ACL that does not exist.
	// Matched by kind, so a name that happens to belong to another resource is not an edge. An ACL the stack does not declare stays lenient.
	for _, ref := range r.aclRefs() {
		if other, ok := all[ref.name]; ok && other.Kind == KindNetworkACL {
			add(ref.name)
		}
	}
	for _, d := range r.DependsOn {
		add(d)
	}
	return deps
}
