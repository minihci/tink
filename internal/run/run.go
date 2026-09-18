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

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// Options configures both how flags are translated (Build) and how the
// resulting Spec is applied (Run). Bound directly to cobra flags in
// cmd/tink -- this package never parses raw argv itself, matching how
// every other tink capability's Options struct works.
type Options struct {
	Socket string

	Name     string
	Image    string
	Cmd      []string
	Env      []string
	Publish  []string
	Volume   []string
	Network  string
	Restart  string
	Pool     string
	Profiles []string

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
		note("would launch %s as %s", spec.Image, spec.Name)
		for _, p := range spec.Profiles {
			note("would layer profile %s", p)
		}
		for name, dev := range spec.Devices {
			note("would add device %s: %v", name, dev)
		}
		if len(spec.Config) > 0 {
			note("would set config: %v", spec.Config)
		}
		return result, nil
	}

	if err := launch(spec); err != nil {
		return result, err
	}
	note("launched %s from %s", spec.Name, spec.Image)

	server, err := incusapi.Connect(opts.Socket)
	if err != nil {
		return result, fmt.Errorf("connecting to incus: %w", err)
	}

	if err := applyConfig(server, spec); err != nil {
		return result, err
	}
	note("applied config and devices to %s", spec.Name)

	return result, nil
}

// launch shells out to `incus launch`, the same choice
// internal/bootstrap/instances.go already makes and documents: resolving
// a remote+image reference like "docker-oci:redis:7" means connecting to
// that named remote as an ImageServer, which is a client-config concept
// with no daemon API to call instead. Reimplementing that resolution here
// would be new, under-verified code standing in front of a live launch
// for no benefit over one exec call to something already proven correct.
func launch(spec *Spec) error {
	args := []string{"launch", spec.Image, spec.Name}
	for _, p := range spec.Profiles {
		args = append(args, "--profile", p)
	}
	out, err := exec.Command("incus", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launching %s: %w: %s", spec.Name, err, out)
	}
	return nil
}

// applyConfig sets spec's translated Config and Devices onto the
// just-launched instance. Unlike launch, this *is* modeled cleanly by
// the daemon's own API, so it goes through Incus's real Go client rather
// than a second shell-out -- this mirrors exactly the manual sequence of
// `incus config set`/`incus config device add` calls used by hand today,
// just composed into one update instead of several.
func applyConfig(server incus.InstanceServer, spec *Spec) error {
	inst, etag, err := server.GetInstance(spec.Name)
	if err != nil {
		return fmt.Errorf("reading %s after launch: %w", spec.Name, err)
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
