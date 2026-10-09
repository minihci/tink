// Package run implements tink run: translating docker-run-style flags
// into an Incus instance -- the config keys and devices they correspond
// to -- instead of hand-composing `incus init`/`config set`/`config
// device add` calls. See DESIGN.md for the flag-mapping table and its
// deliberate non-goals.
package run

import (
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/incusconf"
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

	// User is --user: a numeric UID[:GID]. IncusConfig and IncusDevice are the escape hatch (escape.go).
	User        string
	IncusConfig []string
	IncusDevice []string

	DryRun bool

	// Out, when set, gets each action line as it happens (the first pull of an image can take a minute, and a silent minute reads as a
	// hang). The lines are still collected in Result.Actions.
	Out io.Writer
}

// DefaultOptions returns sensible defaults: the local socket every tink
// capability talks to, and "default" as the storage pool a bare -v
// name:path managed-volume mount attaches in, matching Incus's own
// default pool name on a freshly initialized host.
func DefaultOptions() Options {
	return Options{
		Socket: incusapi.DefaultSocket,
		Pool:   "default",
	}
}

// Result reports what Build produced and, unless DryRun, what Run
// actually did -- one line per action, matching tink deploy's and tink
// ingress reconcile's existing --dry-run convention.
type Result struct {
	Spec    *Spec
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
		for name, dev := range spec.Devices {
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
	q, err := Qualify(scoped, spec.Image)
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
	if warning, err := checkNetwork(scoped, spec); err != nil {
		return result, err
	} else if warning != "" {
		note("warning: %s", warning)
	}

	note("creating %s from %s (a first pull of the image can take a minute)", spec.Name, spec.Image)
	if err := Create(server, spec, opts.Project); err != nil {
		return result, err
	}
	note("created %s", spec.Name)

	server = scoped

	for _, line := range describeVolumes(server, spec) {
		note("%s", line)
	}
	if err := configureOrRemove(server, spec); err != nil {
		return result, err
	}
	note("applied config and devices to %s", spec.Name)

	if err := EnsureRunning(server, spec.Name); err != nil {
		return result, err
	}
	note("started %s", spec.Name)

	return result, nil
}

// Create creates spec's instance without starting it (create, never
// `incus launch`): some images (e.g. Postgres, Nextcloud) run a one-shot,
// config-gated action on first boot that needs env vars already present,
// so config has to be in place before the instance ever starts (see
// ApplyConfig, EnsureRunning). Goes through Incus's real Go client end to
// end -- CreateInstanceFromImage (client/incus.go) already handles
// local-vs-remote image resolution internally (the same same-server fast
// path the daemon's own optimisation uses), so the only piece this still
// needs to do itself is turning spec.Image's remote:ref syntax into an
// ImageServer plus a resolved api.Image, the same way incus's own CLI
// does it (cmd/incus/create.go's getImgInfo) -- read directly rather
// than reimplemented from guesswork. Client config (remotes) is read via
// cliconfig.LoadConfig, a real Go API for the same file `incus remote`
// itself reads and writes -- see internal/bootstrap/registries.go for
// the same reasoning applied to a read-only remote lookup.
func Create(server incus.InstanceServer, spec *Spec, project string) error {
	scoped := server
	if project != "" {
		scoped = server.UseProject(project)
	}

	conf, err := incusconf.Load()
	if err != nil {
		return fmt.Errorf("creating %s: loading incus client config: %w", spec.Name, err)
	}

	imgServer, imgInfo, err := resolveImage(scoped, conf, spec.Image)
	if err != nil {
		return fmt.Errorf("creating %s: %w", spec.Name, err)
	}

	instanceType := api.InstanceTypeContainer
	if spec.VM {
		instanceType = api.InstanceTypeVM
	}

	op, err := scoped.CreateInstanceFromImage(imgServer, imgInfo, api.InstancesPost{
		Name: spec.Name,
		Type: instanceType,
		InstancePut: api.InstancePut{
			Profiles:  spec.Profiles,
			Ephemeral: spec.Ephemeral,
		},
	})
	if err != nil {
		return fmt.Errorf("creating %s: %w", spec.Name, err)
	}
	return op.Wait()
}

// resolveImage splits image the same way incus's own CLI parses a
// REMOTE:REF image reference (cmd/incus/create.go's getImgInfo) -- but
// only treats a colon-prefix as a remote name when it actually matches
// one configured in conf; otherwise the whole string is a bare local
// reference (this platform's own kind: image resources always resolve
// to one of these -- an alias with no colon at all -- so this is the
// common path for tink itself, not just a fallback).
func resolveImage(scoped incus.InstanceServer, conf *cliconfig.Config, image string) (incus.ImageServer, api.Image, error) {
	remoteName, ref, hasPrefix := strings.Cut(image, ":")
	remote, isKnownRemote := conf.Remotes[remoteName]
	if !hasPrefix || !isKnownRemote {
		return resolveLocalImage(scoped, image)
	}

	imgServer, err := conf.GetImageServer(remoteName)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("connecting to remote %q: %w", remoteName, err)
	}

	if remote.Protocol != "incus" {
		// Public image servers (simplestreams, oci, ...): the reference
		// itself is the fingerprint/tag to pull, not an alias to resolve
		// first -- confirmed by reading getImgInfo's own "optimisation
		// for public image servers" branch rather than assumed.
		return imgServer, api.Image{Fingerprint: ref, ImagePut: api.ImagePut{Public: true}}, nil
	}

	alias, _, found, err := incusapi.LookupImageAlias(imgServer, ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving %s: %w", image, err)
	}
	if found {
		ref = alias.Target
	}
	imgInfo, _, err := imgServer.GetImage(ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving %s: %w", image, err)
	}
	return imgServer, *imgInfo, nil
}

// resolveLocalImage resolves ref (an alias or a bare fingerprint)
// against scoped directly -- scoped is already project-scoped by
// Create, and a local image alias lives inside that same project once
// features.images is enabled, the same isolation confirmed live while
// building this platform's own haos test project.
func resolveLocalImage(scoped incus.InstanceServer, ref string) (incus.ImageServer, api.Image, error) {
	alias, _, found, err := incusapi.LookupImageAlias(scoped, ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving local image %q: %w", ref, err)
	}
	if found {
		ref = alias.Target
	}
	imgInfo, _, err := scoped.GetImage(ref)
	if err != nil {
		return nil, api.Image{}, fmt.Errorf("resolving local image %q: %w", ref, err)
	}
	return scoped, *imgInfo, nil
}

// ApplyConfig sets spec's translated Config and Devices onto the
// just-created (not yet started) instance. Unlike Create, this is
// modeled cleanly by the daemon's own API, so it goes through Incus's
// Go client rather than a second shell-out.
func ApplyConfig(server incus.InstanceServer, spec *Spec) error {
	if err := ensureManagedVolumes(server, spec); err != nil {
		return err
	}

	inst, etag, err := server.GetInstance(spec.Name)
	if err != nil {
		return fmt.Errorf("reading %s after creation: %w", spec.Name, err)
	}

	put := inst.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	for k, v := range spec.Config {
		put.Config[k] = v
	}
	if put.Devices == nil {
		put.Devices = api.DevicesMap{}
	}
	for name, dev := range spec.Devices {
		put.Devices[name] = dev
	}

	op, err := server.UpdateInstance(spec.Name, put, etag)
	if err != nil {
		return fmt.Errorf("updating %s config/devices: %w", spec.Name, err)
	}
	return op.Wait()
}

// ensureManagedVolumes creates any managed storage volume a disk device
// references that doesn't already exist yet. Docker auto-creates a named
// volume on first use (`-v name:path`); Incus's own managed volumes
// don't -- attaching a disk device to one that's never been created
// fails validation outright, and UpdateInstance applies atomically, so
// one missing volume would otherwise silently drop every other
// translated config/device change too.
func ensureManagedVolumes(server incus.InstanceServer, spec *Spec) error {
	for _, dev := range spec.Devices {
		if dev["type"] != "disk" || dev["pool"] == "" || dev["source"] == "" {
			continue // a bind mount (no pool), a non-disk device, or a root disk (a pool, but no volume of its own to create)
		}

		pool, name := dev["pool"], dev["source"]
		_, _, found, err := incusapi.LookupVolume(server, pool, "custom", name)
		if err != nil {
			// Not "does not exist": creating on a failed read would fail confusingly, or hide the real problem.
			return fmt.Errorf("checking whether managed volume %s/%s exists: %w", pool, name, err)
		}
		if found {
			continue // already exists
		}

		post := api.StorageVolumesPost{
			Name:             name,
			Type:             "custom",
			ContentType:      "filesystem",
			StorageVolumePut: api.StorageVolumePut{Config: spec.VolumeConfig},
		}
		if err := server.CreateStoragePoolVolume(pool, post); err != nil {
			return fmt.Errorf("creating managed volume %s/%s: %w", pool, name, err)
		}
	}
	return nil
}

// EnsureRunning starts name -- in practice always a first start, since
// Create() never starts the instance itself. Picks start-vs-restart from
// current status rather than assuming Stopped, and retries on Incus's
// own transient "instance is busy" rejection (its operation queue can
// briefly reject a state change mid-transition); mirrors
// internal/bootstrap.ensureRunning's own logic, reproduced here rather
// than shared across packages.
func EnsureRunning(server incus.InstanceServer, name string) error {
	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		inst, _, err := server.GetInstance(name)
		if err != nil {
			return fmt.Errorf("checking %s's state: %w", name, err)
		}

		action := "start"
		if inst.Status == "Running" {
			action = "restart"
		}

		op, err := server.UpdateInstanceState(name, api.InstanceStatePut{Action: action, Timeout: 30}, "")
		if err == nil {
			if err = op.Wait(); err == nil {
				return nil
			}
		}
		lastErr = err
		time.Sleep(time.Second)
	}
	return fmt.Errorf("starting/restarting %s to apply its config: giving up after %d attempts: %w", name, maxAttempts, lastErr)
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
// or in any profile it will get (Incus uses "default" when none is named). An Incus profile does not have to carry a NIC, and tron's
// default does not, so an instance made without --network is RUNNING with no address and a proxy that forwards to nothing, which looks
// like a bug in the app. Published ports without a NIC are refused; no NIC and no ports only warns, since an isolated app is legitimate.
func checkNetwork(scoped incus.InstanceServer, spec *Spec) (warning string, err error) {
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

// ImageQualification is what Qualify decided about an image reference.
type ImageQualification struct {
	// Image is the reference to use: the input when nothing needed doing, else REMOTE:REF.
	Image string
	// Note says what was done when the person had been explicit (a registry host written Docker's way, mapped to its remote).
	Note string
	// Warning is set when the registry was a guess: the reference named none, so Docker Hub was assumed.
	Warning string
}

// Qualify turns a docker-run-style reference into one Incus can resolve, when nothing else would: it is not REMOTE:REF with a configured
// remote, and not a local alias or fingerprint (a name that resolves locally is left alone, so this only ever turns an error into an
// attempt). Used by `tink run` and, for a stack's instances, by `plan` and `plan apply`, so one reference means one thing everywhere.
//
//	ghcr.io/advplyr/audiobookshelf:latest   -> ghcr:advplyr/audiobookshelf:latest        a registry host: that host's OCI remote
//	louislam/uptime-kuma:2                  -> docker-oci:louislam/uptime-kuma:2         no registry: Docker Hub, with a Warning
//	registry.example.com/team/app:1         -> an error saying how to add a remote for it
//
// An unknown host is an error, not a guess: sending the whole name to Docker Hub fails with a message about docker.io/registry.example.com/...
// that hides the cause.
func Qualify(scoped incus.InstanceServer, image string) (ImageQualification, error) {
	same := ImageQualification{Image: image}
	conf, err := incusconf.Load()
	if err != nil {
		return same, nil
	}
	if remoteName, _, has := strings.Cut(image, ":"); has {
		if _, known := conf.Remotes[remoteName]; known {
			return same, nil
		}
	}
	if _, _, found, err := incusapi.LookupImageAlias(scoped, image); err != nil || found {
		return same, nil
	}
	if _, _, found, err := incusapi.LookupImage(scoped, image); err != nil || found {
		return same, nil
	}
	if host, rest, ok := registryHost(image); ok {
		for _, name := range sortedRemoteNames(conf.Remotes) {
			if r := conf.Remotes[name]; r.Protocol == "oci" && remoteHostOf(r) == host {
				return ImageQualification{Image: name + ":" + rest, Note: fmt.Sprintf("%q is the registry %s: using the remote %s", image, host, name)}, nil
			}
		}
		return same, fmt.Errorf("image %q is on the registry %s, and no OCI remote is configured for it: add one (for example `incus remote add NAME https://%s --protocol=oci`) and write the image as NAME:%s", image, host, host, rest)
	}
	if _, known := conf.Remotes[dockerRemote]; !known {
		return same, nil
	}
	hub := image
	if !strings.Contains(image, "/") {
		hub = "library/" + image // Docker Hub's official images live under library/
	}
	return ImageQualification{
		Image:   dockerRemote + ":" + image,
		Warning: fmt.Sprintf("%q names no registry: assuming Docker Hub. Say so with docker.io/%s (or %s:%s) to silence this", image, hub, dockerRemote, image),
	}, nil
}

// registryHost splits "host/path" when the first path segment looks like a registry host, as Docker decides it: it has a dot or a colon,
// or is localhost.
func registryHost(image string) (host, rest string, ok bool) {
	host, rest, found := strings.Cut(image, "/")
	if !found || rest == "" {
		return "", "", false
	}
	if strings.ContainsAny(host, ".:") || host == "localhost" {
		return host, rest, true
	}
	return "", "", false
}

// remoteHostOf is the registry host an OCI remote points at, without scheme, path or login.
func remoteHostOf(r cliconfig.Remote) string {
	if len(r.Addrs) == 0 {
		return ""
	}
	h := r.Addrs[0]
	if u, err := url.Parse(h); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://"), "/")
}

func sortedRemoteNames(m map[string]cliconfig.Remote) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// dockerRemote is the built-in OCI remote for Docker Hub (see internal/incusconf).
const dockerRemote = "docker-oci"

// describeVolumes says, for each managed volume the spec attaches, whether it is about to be created or already there.
func describeVolumes(server incus.InstanceServer, spec *Spec) []string {
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
			lines = append(lines, fmt.Sprintf("reusing volume %s/%s (its ownership is not changed: if the app cannot write to it, chown it to %s)", dev["pool"], dev["source"], ownerOf(spec.VolumeConfig)))
		case found:
			lines = append(lines, fmt.Sprintf("reusing volume %s/%s", dev["pool"], dev["source"]))
		case len(spec.VolumeConfig) > 0:
			lines = append(lines, fmt.Sprintf("creating volume %s/%s, owned by %s", dev["pool"], dev["source"], ownerOf(spec.VolumeConfig)))
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
func configureOrRemove(server incus.InstanceServer, spec *Spec) error {
	err := ApplyConfig(server, spec)
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
