// Package run implements tink run: translating docker-run-style flags
// into an Incus instance -- the config keys and devices they correspond
// to -- instead of hand-composing `incus init`/`config set`/`config
// device add` calls. See DESIGN.md for the flag-mapping table and its
// deliberate non-goals.
package dockerrun

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/instance"
	"github.com/minihci/tink/internal/secrets"
)

// Options configures both how flags are translated (Build) and how the
// resulting Spec is applied (Run). Bound directly to cobra flags in
// cmd/tink -- this package never parses raw argv itself, matching how
// every other tink capability's Options struct works.
type Options struct {
	Socket  string
	Project string

	Name     string
	Image    string
	Cmd      []string
	Env      []string
	Publish  []string
	Volume   []string
	Network  string
	IP       string
	Restart  string
	Pool     string
	Profiles []string
	Rm       bool
	VM       bool

	// EnvFile is --env-file PATH (read on the machine running tink); Privileged is --privileged.
	EnvFile    []string
	Privileged bool

	// Memory is -m (512m, 1g), CPUs is --cpus (1.5), Device is --device HOST[:CONTAINER[:rwm]].
	Memory string
	CPUs   string
	Device []string

	// User is --user: a numeric UID[:GID]. IncusConfig and IncusDevice are the escape hatch (escape.go).
	User        string
	IncusConfig []string
	IncusDevice []string

	DryRun bool

	// Out, when set, gets each action line as it happens (the first pull of an image can take a minute, and a silent minute reads as a
	// hang). The lines are still collected in Result.Actions.
	Out io.Writer

	// Settle is how long Run waits after a start before it asks whether the instance is still running (earlyexit.go). Zero asks at once.
	Settle time.Duration
}

// DefaultOptions returns sensible defaults: "default" as the storage pool a
// bare -v name:path managed-volume mount attaches in, matching Incus's own
// default pool name on a freshly initialized host. Socket stays empty, which
// is Incus's own resolution of the local daemon's socket.
func DefaultOptions() Options {
	return Options{
		Pool:   "default",
		Settle: DefaultSettle,
	}
}

// Result reports what Build produced and, unless DryRun, what Run
// actually did -- one line per action, matching tink deploy's existing
// --dry-run convention.
type Result struct {
	Spec    *instance.Spec
	Actions []string
}

// Run builds a Spec from opts and applies it: creates the instance
// (without starting it), sets its translated config and devices, then
// starts it. With opts.DryRun, it computes and returns the same Spec and
// a description of what would happen, without touching the daemon.
func Run(opts Options) (*Result, error) {
	spec, err := Build(opts)
	if err != nil {
		// No Spec exists yet -- nothing to report, but still a non-nil
		// Result so callers can range over .Actions unconditionally,
		// matching every other tink capability's convention.
		return &Result{}, err
	}

	result := &Result{Spec: spec}
	note := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		result.Actions = append(result.Actions, line)
		if opts.Out != nil {
			fmt.Fprintln(opts.Out, line)
		}
	}

	for _, n := range spec.Notes {
		note("note: %s", n)
	}
	if incusapi.IsRemote() {
		var paths []string
		seen := map[string]bool{}
		for _, name := range sortedDeviceNames(spec.Devices) {
			if d := spec.Devices[name]; d["type"] == "disk" && d["pool"] == "" && strings.HasPrefix(d["source"], "/") && !seen[d["source"]] {
				seen[d["source"]] = true
				paths = append(paths, d["source"])
			}
		}
		if len(paths) > 0 {
			verb := "is a path"
			if len(paths) > 1 {
				verb = "are paths"
			}
			note("note: %s %s on the server %s, not on this machine", strings.Join(paths, ", "), verb, incusapi.Remote())
		}
	}

	if opts.DryRun {
		project := opts.Project
		if project == "" {
			project = "(daemon default)"
		}
		ephemeral := ""
		if spec.Ephemeral {
			ephemeral = ", ephemeral: deleted automatically the moment it stops"
		}
		kind := "container"
		if spec.VM {
			kind = "virtual machine"
		}
		note("would create %s (%s) from %s in project %s (not started yet%s)", spec.Name, kind, spec.Image, project, ephemeral)
		for _, p := range spec.Profiles {
			note("would layer profile %s", p)
		}
		for _, name := range sortedDeviceNames(spec.Devices) {
			dev := spec.Devices[name]
			if dev["type"] == "disk" && dev["pool"] != "" {
				note("would ensure managed volume %s/%s exists (creating it if needed)", dev["pool"], dev["source"])
			}
			note("would add device %s: %v", name, dev)
		}
		if len(spec.Config) > 0 {
			note("would set config: %v", secrets.MaskedConfig(spec.Config))
		}
		if !hasNIC(spec.Devices) {
			note("no --network given: the profiles layered on %s must supply a NIC (checked for real when run, and refused if ports are published without one)", spec.Name)
		}
		if len(opts.Cmd) > 0 {
			note("a command given after the image goes after the image's own entrypoint, which the real run reads from the image's registry; this dry run does not, so the line above shows it alone")
		}
		note("would start %s", spec.Name)
		return result, nil
	}

	server, err := incusapi.Connect(opts.Socket)
	if err != nil {
		return result, fmt.Errorf("connecting to incus: %w", err)
	}

	scoped := server
	if opts.Project != "" {
		scoped = server.UseProject(opts.Project)
	}

	// Everything that can be refused is refused before anything exists: a half-made instance is the worse outcome.
	if opts.Project != "" {
		if _, _, found, err := incusapi.LookupProject(server, opts.Project); err != nil {
			return result, fmt.Errorf("reading project %q: %w", opts.Project, err)
		} else if !found {
			return result, fmt.Errorf("project %q does not exist: tink run does not create projects (they carry choices of their own). Create it first, for example `incus project create %s -c features.profiles=false` to share the default profile", opts.Project, opts.Project)
		}
	}
	q, err := instance.Qualify(scoped, spec.Image)
	if err != nil {
		return result, err
	}
	if q.Note != "" {
		note("%s", q.Note)
	}
	if q.Warning != "" {
		note("warning: %s", q.Warning)
	}
	spec.Image = q.Image
	if len(opts.Cmd) > 0 {
		// A command goes after the image's own entrypoint, as docker run puts it (#230); oci.entrypoint, as Build set it, would replace the entrypoint.
		switch entry, err := imageEntrypoint(server, spec.Image); {
		case errors.Is(err, errNotOCI):
		case err != nil:
			note("warning: could not read the image's entrypoint from its registry (%v), so the command replaces it, where docker run puts it after it. If the image has an entrypoint it will not start; write the entrypoint out before the command", err)
		case len(entry) > 0:
			spec.Config["oci.entrypoint"] = withEntrypoint(entry, opts.Cmd)
			note("the command goes after the image's entrypoint (%s), as docker run puts it", strings.Join(entry, " "))
		}
	}
	if warning, err := checkNetwork(scoped, spec); err != nil {
		return result, err
	} else if warning != "" {
		note("warning: %s", warning)
	}

	note("creating %s from %s (a first pull of the image can take a minute)", spec.Name, spec.Image)
	if err := instance.Create(server, spec, opts.Project); err != nil {
		return result, err
	}
	note("created %s", spec.Name)

	server = scoped

	if warning := inheritImageUser(server, spec); warning != "" {
		note("warning: %s", warning)
	}
	for _, line := range describeVolumes(server, spec) {
		note("%s", line)
	}
	if err := configureOrRemove(server, spec); err != nil {
		return result, err
	}
	note("applied config and devices to %s", spec.Name)

	if err := instance.EnsureRunning(server, spec.Name); err != nil {
		explainConsole(server, spec, note, fmt.Sprintf("%s did not start", spec.Name))
		return result, err
	}
	note("started %s", spec.Name)
	if err := checkStillRunning(server, spec, opts.Settle, note); err != nil {
		return result, err
	}

	return result, nil
}

// hasNIC reports whether devices holds a network device.
func hasNIC(devices map[string]map[string]string) bool {
	for _, d := range devices {
		if d["type"] == "nic" {
			return true
		}
	}
	return false
}

// checkNetwork looks for the one thing that makes a published port impossible: no network device at all, in the instance's own devices
// or in any profile it will get (Incus uses "default" when none is named). An Incus profile does not have to carry a NIC, and a
// default profile does not, so an instance made without --network is RUNNING with no address and a proxy that forwards to nothing, which looks
// like a bug in the app. Published ports without a NIC are refused; no NIC and no ports only warns, since an isolated app is legitimate.
func checkNetwork(scoped incus.InstanceServer, spec *instance.Spec) (warning string, err error) {
	if hasNIC(spec.Devices) {
		return "", nil
	}
	profiles := spec.Profiles
	if len(profiles) == 0 {
		profiles = []string{"default"}
	}
	for _, name := range profiles {
		p, _, found, err := incusapi.LookupProfile(scoped, name)
		if err != nil {
			return "", fmt.Errorf("reading profile %q: %w", name, err)
		}
		if !found {
			return "", fmt.Errorf("profile %q does not exist", name)
		}
		if hasNIC(p.Devices) {
			return "", nil
		}
	}
	for _, d := range spec.Devices {
		if d["type"] == "proxy" {
			return "", fmt.Errorf("%s would have no network device: neither profile %s has a NIC and --network was not given, so the published port(s) would forward to an instance with no address; add --network NAME (for example --network incusbr0)", spec.Name, strings.Join(profiles, ", "))
		}
	}
	return fmt.Sprintf("%s has no network device (no NIC in profile %s and no --network): it will have no address", spec.Name, strings.Join(profiles, ", ")), nil
}

// describeVolumes says, for each managed volume the spec attaches, whether it is about to be created or already there.
func describeVolumes(server incus.InstanceServer, spec *instance.Spec) []string {
	var lines []string
	for _, name := range sortedDeviceNames(spec.Devices) {
		dev := spec.Devices[name]
		if dev["type"] != "disk" || dev["pool"] == "" || dev["source"] == "" {
			continue
		}
		_, _, found, err := incusapi.LookupVolume(server, dev["pool"], "custom", dev["source"])
		switch {
		case err != nil:
			// ApplyConfig reports the real error; this is only a courtesy line
		case found && len(spec.VolumeConfig) > 0:
			lines = append(lines, fmt.Sprintf("reusing volume %s/%s (its ownership is not changed: if the app cannot write to it, chown it to %s%s)", dev["pool"], dev["source"], ownerOf(spec.VolumeConfig), ownerSource(spec)))
		case found:
			lines = append(lines, fmt.Sprintf("reusing volume %s/%s", dev["pool"], dev["source"]))
		case len(spec.VolumeConfig) > 0:
			lines = append(lines, fmt.Sprintf("creating volume %s/%s, owned by %s%s", dev["pool"], dev["source"], ownerOf(spec.VolumeConfig), ownerSource(spec)))
		default:
			lines = append(lines, fmt.Sprintf("creating volume %s/%s", dev["pool"], dev["source"]))
		}
	}
	return lines
}

func sortedDeviceNames(devices map[string]map[string]string) []string {
	names := make([]string, 0, len(devices))
	for n := range devices {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ownerOf writes the owner a volume is created with, as uid or uid:gid.
func ownerOf(volumeConfig map[string]string) string {
	if gid := volumeConfig["initial.gid"]; gid != "" {
		return volumeConfig["initial.uid"] + ":" + gid
	}
	return volumeConfig["initial.uid"]
}

// configureOrRemove applies spec's config and devices to the instance Run has just created, and removes that instance if it cannot. Incus
// applies a config update atomically and validates it only then, so a bad device or key (a relative host path, a misspelt --incus-config) is
// reported after the instance exists; left alone it would be a stopped shell that a second run answers with "already exists". Only Run does
// this: the instance is one it made a moment ago under a name that was free, and managed volumes it created stay, as Docker keeps volumes.
func configureOrRemove(server incus.InstanceServer, spec *instance.Spec) error {
	err := instance.ApplyConfig(server, spec)
	if err == nil {
		return nil
	}
	op, derr := server.DeleteInstance(spec.Name)
	if derr == nil {
		derr = op.Wait()
	}
	if derr != nil {
		return fmt.Errorf("%w (and %s, which was just created, could not be removed: %v)", err, spec.Name, derr)
	}
	return fmt.Errorf("%w (%s was removed again: nothing is left of this run but any volume it made)", err, spec.Name)
}
