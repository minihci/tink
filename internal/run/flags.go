package run

import (
	"fmt"
	"strings"

	"github.com/minihci/tink/internal/secrets"
)

// Spec is the fully-resolved instance-creation request produced by
// translating docker-run-style flags onto Incus primitives -- see
// DESIGN.md's "Flag mapping" table for the reasoning behind each
// translation below. Building a Spec never touches Incus; only Run does.
type Spec struct {
	Name      string
	Image     string
	Cmd       []string
	Config    map[string]string
	Devices   map[string]map[string]string
	Profiles  []string
	Ephemeral bool
	VM        bool

	// Notes are things the person should be told about what was asked for (a bind mount that will not be writable, an option that was ignored).
	Notes []string

	// VolumeConfig is the config a managed storage volume gets when this run has to CREATE it (one that exists is not changed). From --user.
	VolumeConfig map[string]string
}

// Build translates already-parsed docker-run-style flag values (Options,
// as bound by cmd/tink's cobra flags) into a Spec. This is deliberately
// the only part of this package with no Incus dependency at all, so it's
// the part carrying the real test coverage -- see flags_test.go.
func Build(opts Options) (*Spec, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("--name is required: unlike docker, tink run does not invent a random instance name")
	}
	if opts.Image == "" {
		return nil, fmt.Errorf("an image is required")
	}

	spec := &Spec{
		Name:      opts.Name,
		Image:     opts.Image,
		Cmd:       opts.Cmd,
		Config:    map[string]string{},
		Devices:   map[string]map[string]string{},
		Profiles:  opts.Profiles,
		Ephemeral: opts.Rm,
		VM:        opts.VM,
	}

	for _, e := range opts.Env {
		k, v, ok := strings.Cut(e, "=")
		if !ok {
			return nil, fmt.Errorf("--env %q: expected KEY=VALUE", e)
		}
		spec.Config["environment."+k] = v
	}

	if len(opts.Cmd) > 0 {
		// oci.entrypoint takes a single command-line string, exec'd in
		// place of the image's own entrypoint. Args containing spaces or
		// shell metacharacters aren't escaped/quoted.
		spec.Config["oci.entrypoint"] = strings.Join(opts.Cmd, " ")
	}

	for i, p := range opts.Publish {
		dev, err := publishDevice(p)
		if err != nil {
			return nil, fmt.Errorf("--publish %q: %w", p, err)
		}
		spec.Devices[fmt.Sprintf("proxy%d", i)] = dev
	}

	for i, v := range opts.Volume {
		dev, notes, err := volumeDevice(v, opts.Pool)
		if err != nil {
			return nil, fmt.Errorf("--volume %q: %w", v, err)
		}
		spec.Devices[fmt.Sprintf("volume%d", i)] = dev
		spec.Notes = append(spec.Notes, notes...)
	}

	for i, d := range opts.Device {
		dev, err := deviceFlag(d)
		if err != nil {
			return nil, fmt.Errorf("--device %q: %w", d, err)
		}
		spec.Devices[fmt.Sprintf("device%d", i)] = dev
	}

	if opts.Memory != "" {
		limit, err := parseMemory(opts.Memory)
		if err != nil {
			return nil, err
		}
		spec.Config["limits.memory"] = limit
	}
	if opts.CPUs != "" {
		allowance, err := parseCPUs(opts.CPUs)
		if err != nil {
			return nil, err
		}
		spec.Config["limits.cpu.allowance"] = allowance
	}

	if opts.IP != "" && opts.Network == "" {
		return nil, fmt.Errorf("--ip requires --network")
	}
	if opts.Network != "" {
		dev := map[string]string{
			"type":    "nic",
			"network": opts.Network,
		}
		if opts.IP != "" {
			dev["ipv4.address"] = opts.IP
		}
		spec.Devices["eth0"] = dev
	}

	if opts.Restart != "" {
		autorestart, err := restartToAutorestart(opts.Restart)
		if err != nil {
			return nil, err
		}
		spec.Config["boot.autorestart"] = autorestart
		// Docker's policies also decide whether the container comes back when the daemon (here, the host) starts; Incus keeps that in a
		// second key. Without it an instance would come back after a crash but only by luck after a reboot.
		spec.Config["boot.autostart"] = autorestart
	}

	if err := applyUser(spec, opts.User); err != nil {
		return nil, err
	}
	// The escape hatch comes last so that it can only add, never silently override.
	if err := addIncusConfig(spec, opts.IncusConfig); err != nil {
		return nil, err
	}
	if err := addIncusDevices(spec, opts.IncusDevice); err != nil {
		return nil, err
	}

	spec.Config[KeyCommand] = CommandLine(opts)

	return spec, nil
}

// KeyCommand is the instance config key tink run records the command it was given in: the only trace, on the instance, of how it
// came to be. `tink export` reads it back as a comment. Environment values that look like secrets are masked in it.
const KeyCommand = "user.tink.run.command"

// CommandLine is the tink run invocation opts stand for, in a fixed order, for KeyCommand. Quoted so that it can be pasted.
func CommandLine(opts Options) string {
	parts := []string{"tink run"}
	flag := func(name string, vals ...string) {
		for _, v := range vals {
			parts = append(parts, name, shellQuote(v))
		}
	}
	flag("--name", opts.Name)
	if opts.Project != "" {
		flag("--project", opts.Project)
	}
	if opts.VM {
		parts = append(parts, "--vm")
	}
	if opts.Rm {
		parts = append(parts, "--rm")
	}
	if opts.Restart != "" {
		flag("--restart", opts.Restart)
	}
	if opts.Network != "" {
		flag("--network", opts.Network)
	}
	if opts.Memory != "" {
		flag("--memory", opts.Memory)
	}
	if opts.CPUs != "" {
		flag("--cpus", opts.CPUs)
	}
	flag("--device", opts.Device...)
	if opts.IP != "" {
		flag("--ip", opts.IP)
	}
	flag("--profile", opts.Profiles...)
	if opts.User != "" {
		flag("--user", opts.User)
	}
	for _, e := range opts.IncusConfig {
		flag("--incus-config", incusConfigForCommand(e, secrets.SensitiveKey))
	}
	flag("--incus-device", opts.IncusDevice...)
	for _, e := range opts.Env {
		k, v, ok := strings.Cut(e, "=")
		if ok && secrets.SensitiveKey(k) {
			v = "***"
		}
		if ok {
			e = k + "=" + v
		}
		flag("-e", e)
	}
	flag("-p", opts.Publish...)
	flag("-v", opts.Volume...)
	if len(opts.Volume) > 0 && opts.Pool != "" && opts.Pool != "default" {
		flag("--pool", opts.Pool)
	}
	parts = append(parts, shellQuote(opts.Image))
	for _, c := range opts.Cmd {
		parts = append(parts, shellQuote(c))
	}
	return strings.Join(parts, " ")
}

// shellQuote leaves a plain word alone and single-quotes anything else.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789@%+=:,./-_") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// restartToAutorestart maps a docker --restart value onto Incus's plain
// boolean boot.autorestart, rejecting retry-count values rather than
// silently discarding the count -- see DESIGN.md's flag-mapping caveat.
func restartToAutorestart(restart string) (string, error) {
	switch restart {
	case "always", "unless-stopped", "on-failure":
		return "true", nil
	case "no":
		return "false", nil
	default:
		if strings.HasPrefix(restart, "on-failure:") {
			return "", fmt.Errorf("--restart %q has no Incus equivalent: boot.autorestart is a plain boolean, retry counts aren't supported -- use --restart=on-failure (infinite retries), always, unless-stopped, or no", restart)
		}
		return "", fmt.Errorf("--restart %q not recognized -- one of: always, unless-stopped, on-failure, no", restart)
	}
}
