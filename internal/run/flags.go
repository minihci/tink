package run

import (
	"fmt"
	"strconv"
	"strings"
)

// Spec is the fully-resolved instance-creation request produced by
// translating docker-run-style flags onto Incus primitives -- see
// DESIGN.md's "Flag mapping" table for the reasoning behind each
// translation below. Building a Spec never touches Incus; only Run does.
type Spec struct {
	Name     string
	Image    string
	Cmd      []string
	Config   map[string]string
	Devices  map[string]map[string]string
	Profiles []string
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
		Name:     opts.Name,
		Image:    opts.Image,
		Cmd:      opts.Cmd,
		Config:   map[string]string{},
		Devices:  map[string]map[string]string{},
		Profiles: opts.Profiles,
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
		// place of the image's own entrypoint -- exactly what scoped
		// nextcloud-mcp to `webdav`+`calendar` (see DESIGN.md). Sufficient
		// for every real case exercised on this platform so far; args
		// containing spaces or shell metacharacters aren't handled since
		// none of those real cases needed it.
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
		dev, err := volumeDevice(v, opts.Pool)
		if err != nil {
			return nil, fmt.Errorf("--volume %q: %w", v, err)
		}
		spec.Devices[fmt.Sprintf("volume%d", i)] = dev
	}

	if opts.Network != "" {
		spec.Devices["eth0"] = map[string]string{
			"type":    "nic",
			"network": opts.Network,
		}
	}

	if opts.Restart != "" {
		autorestart, err := restartToAutorestart(opts.Restart)
		if err != nil {
			return nil, err
		}
		spec.Config["boot.autorestart"] = autorestart
	}

	return spec, nil
}

// publishDevice translates a docker -p HOST:CONTAINER value into a proxy
// device. Only the plain HOST:CONTAINER form is supported -- no protocol
// suffix, no bind-address prefix -- matching every -p this platform has
// actually used so far (see DESIGN.md).
func publishDevice(p string) (map[string]string, error) {
	hostPort, containerPort, ok := strings.Cut(p, ":")
	if !ok {
		return nil, fmt.Errorf("expected HOST:CONTAINER")
	}
	if _, err := strconv.Atoi(hostPort); err != nil {
		return nil, fmt.Errorf("host port %q is not a number", hostPort)
	}
	if _, err := strconv.Atoi(containerPort); err != nil {
		return nil, fmt.Errorf("container port %q is not a number", containerPort)
	}

	return map[string]string{
		"type":    "proxy",
		"listen":  "tcp:0.0.0.0:" + hostPort,
		"connect": "tcp:127.0.0.1:" + containerPort,
	}, nil
}

// volumeDevice translates a docker -v value into a disk device. A source
// containing "/" is a host-path bind mount (matching Docker's own
// disambiguation rule); a bare name is a managed Incus storage volume,
// which -- unlike a Docker named volume -- needs a storage pool, so it's
// attached in whichever pool --pool names (see DESIGN.md's open
// questions: this is a real Docker/Incus model mismatch, not an
// oversight).
func volumeDevice(v, pool string) (map[string]string, error) {
	src, dst, ok := strings.Cut(v, ":")
	if !ok {
		return nil, fmt.Errorf("expected SRC:DST")
	}

	if strings.Contains(src, "/") {
		return map[string]string{
			"type":   "disk",
			"source": src,
			"path":   dst,
		}, nil
	}

	return map[string]string{
		"type":   "disk",
		"pool":   pool,
		"source": src,
		"path":   dst,
	}, nil
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
