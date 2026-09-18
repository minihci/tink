// Package run implements tink run: translating a docker-run-shaped
// invocation onto real Incus primitives, instead of the manual "mental
// docker-run image+flags into incus launch plus a sequence of incus
// config set/incus config device add calls" dance every tenant app on
// this platform has been built with so far. See DESIGN.md for the full
// design rationale and the flag-mapping table this package implements.
package run

import (
	"fmt"
	"os/exec"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
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

	DryRun bool
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

// Run builds a Spec from opts and applies it: launches the image, then
// sets the translated config and devices on the new instance. With
// opts.DryRun, it computes and returns the same Spec and a description of
// what would happen, without touching the daemon or launching anything.
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
		result.Actions = append(result.Actions, fmt.Sprintf(format, args...))
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
		note("would create %s from %s in project %s (not started yet%s)", spec.Name, spec.Image, project, ephemeral)
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
			note("would set config: %v", spec.Config)
		}
		note("would start %s", spec.Name)
		return result, nil
	}

	if err := create(spec, opts.Project); err != nil {
		return result, err
	}
	note("created %s from %s", spec.Name, spec.Image)

	server, err := incusapi.Connect(opts.Socket)
	if err != nil {
		return result, fmt.Errorf("connecting to incus: %w", err)
	}
	if opts.Project != "" {
		server = server.UseProject(opts.Project)
	}

	if err := applyConfig(server, spec); err != nil {
		return result, err
	}
	note("applied config and devices to %s", spec.Name)

	if err := ensureRunning(server, spec.Name); err != nil {
		return result, err
	}
	note("started %s", spec.Name)

	return result, nil
}

// create shells out to `incus init` (create without starting) rather
// than `incus launch`, the same choice nextcloud-app's and
// nextcloud-db's own profile comments already prescribe by hand: an
// image whose entrypoint does a one-shot, config-gated action on first
// boot (Postgres's own init scripts need POSTGRES_PASSWORD already
// present; Nextcloud's own installer needs its DB credentials already
// present) can't be launched bare and configured afterward -- by the
// time config lands via UpdateInstance, the one-shot moment has already
// passed, or the container has already failed to start at all. Creating
// first, then applying config, then starting once (see ensureRunning)
// mirrors Docker's own single atomic "docker run" semantics far more
// faithfully than the original launch-then-reconfigure-then-restart
// design did, and removes that design's restart-vs-start ambiguity
// entirely: the instance is never running before its config is already
// in place, so this is always a first start, never a restart.
//
// The remote/image-resolution shell-out itself is unavoidable for the
// same reason internal/bootstrap/instances.go already documents:
// resolving "docker-oci:redis:7" means talking to that named remote as
// an ImageServer, a client-config concept with no daemon API to call
// instead.
func create(spec *Spec, project string) error {
	args := []string{"init", spec.Image, spec.Name}
	for _, p := range spec.Profiles {
		args = append(args, "--profile", p)
	}
	if project != "" {
		args = append(args, "--project", project)
	}
	if spec.Ephemeral {
		args = append(args, "--ephemeral")
	}
	out, err := exec.Command("incus", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating %s: %w: %s", spec.Name, err, out)
	}
	return nil
}

// applyConfig sets spec's translated Config and Devices onto the
// just-created (not yet started) instance. Unlike create, this *is*
// modeled cleanly by the daemon's own API, so it goes through Incus's
// real Go client rather than a second shell-out -- this mirrors exactly
// the manual sequence of `incus config set`/`incus config device add`
// calls used by hand today, just composed into one update instead of
// several.
func applyConfig(server incus.InstanceServer, spec *Spec) error {
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
// references that doesn't already exist. Docker auto-creates a named
// volume on first use (`-v name:path`); Incus's own managed storage
// volumes don't -- attaching a disk device to one that's never been
// created fails validation outright, which UpdateInstance applies
// atomically, so one missing volume would otherwise silently drop every
// other translated config/device change too. Confirmed against a real,
// disposable test host, not assumed. tink run matches Docker's
// ergonomics here rather than Incus's own stricter default, since
// replicating that gap would defeat the point of translating docker-run
// flags in the first place.
func ensureManagedVolumes(server incus.InstanceServer, spec *Spec) error {
	for _, dev := range spec.Devices {
		if dev["type"] != "disk" || dev["pool"] == "" {
			continue // a bind mount (no pool) or a non-disk device
		}

		pool, name := dev["pool"], dev["source"]
		if _, _, err := server.GetStoragePoolVolume(pool, "custom", name); err == nil {
			continue // already exists
		}

		post := api.StorageVolumesPost{
			Name:        name,
			Type:        "custom",
			ContentType: "filesystem",
		}
		if err := server.CreateStoragePoolVolume(pool, post); err != nil {
			return fmt.Errorf("creating managed volume %s/%s: %w", pool, name, err)
		}
	}
	return nil
}

// ensureRunning starts name -- in practice always a first start, since
// create() never starts the instance itself, so its config is always
// already in place by the time this runs. Still checks current status
// and picks start-vs-restart rather than assuming Stopped, and retries
// on Incus's own transient "instance is busy" rejection (its operation
// queue can briefly reject a state change mid-transition) -- the same
// two real races internal/bootstrap.ensureRunning already found and
// handles, reproduced here rather than shared across packages.
func ensureRunning(server incus.InstanceServer, name string) error {
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
