package run

import (
	"fmt"
	"math"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// The translators for the flags that take a small language of their own: -p, -v, -m, --cpus, --device. Each is pure (strings in, an Incus
// device or config value out) so the table of what is accepted lives in the tests. They accept what Docker's own install lines write and
// refuse, with the reason, what they cannot honour, rather than reading it as something else.

// publishDevice translates -p [IP:]HOST[-HOST]:CONTAINER[-CONTAINER][/tcp|/udp] into a proxy device. IPv6 addresses are written in
// brackets, as Docker does. A container port alone is refused: Docker would pick a random host port, and tink has no way to report it.
func publishDevice(p string) (map[string]string, error) {
	spec, proto := p, "tcp"
	if i := strings.LastIndex(p, "/"); i >= 0 {
		spec, proto = p[:i], strings.ToLower(p[i+1:])
		if proto != "tcp" && proto != "udp" {
			return nil, fmt.Errorf("protocol %q: expected tcp or udp", p[i+1:])
		}
	}

	ip, rest := "0.0.0.0", spec
	if strings.HasPrefix(spec, "[") {
		end := strings.Index(spec, "]")
		if end < 0 || !strings.HasPrefix(spec[end+1:], ":") {
			return nil, fmt.Errorf("expected [ADDRESS]:HOST:CONTAINER")
		}
		ip, rest = spec[:end+1], spec[end+2:]
		if net.ParseIP(strings.Trim(ip, "[]")) == nil {
			return nil, fmt.Errorf("%q is not an IP address", ip)
		}
	}
	parts := strings.Split(rest, ":")
	var host, container string
	switch {
	case len(parts) == 2:
		host, container = parts[0], parts[1]
	case len(parts) == 3 && ip == "0.0.0.0":
		ip, host, container = parts[0], parts[1], parts[2]
		if net.ParseIP(ip) == nil {
			return nil, fmt.Errorf("%q is not an IP address (a host name cannot be bound)", ip)
		}
	case len(parts) == 1:
		return nil, fmt.Errorf("expected HOST:CONTAINER: a container port alone would be published on a random host port")
	default:
		return nil, fmt.Errorf("expected [IP:]HOST:CONTAINER")
	}

	hostLen, err := portSpec("host port", host)
	if err != nil {
		return nil, err
	}
	contLen, err := portSpec("container port", container)
	if err != nil {
		return nil, err
	}
	if hostLen != contLen && contLen != 1 {
		return nil, fmt.Errorf("the host range %s has %d ports and the container range %s has %d: they must match (or the container side be one port)", host, hostLen, container, contLen)
	}
	return map[string]string{
		"type":    "proxy",
		"listen":  fmt.Sprintf("%s:%s:%s", proto, ip, host),
		"connect": fmt.Sprintf("%s:127.0.0.1:%s", proto, container),
	}, nil
}

// portSpec checks "N" or "A-B" and returns how many ports it names.
func portSpec(what, s string) (int, error) {
	lo, hi, isRange := strings.Cut(s, "-")
	port := func(v string) (int, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 65535 {
			return 0, fmt.Errorf("%s %q is not a port number (1-65535) or range (A-B)", what, s)
		}
		return n, nil
	}
	a, err := port(lo)
	if err != nil {
		return 0, err
	}
	if !isRange {
		return 1, nil
	}
	b, err := port(hi)
	if err != nil {
		return 0, err
	}
	if b < a {
		return 0, fmt.Errorf("%s %q: the range runs backwards", what, s)
	}
	return b - a + 1, nil
}

// volumeDevice translates -v SRC:DST[:OPTIONS] into a disk device, and returns anything the person should be told.
//
// A source containing "/" is a host path (Docker's own rule), and with --remote it is a path on the SERVER, so it must be absolute. A bare
// name is a managed Incus volume in pool. Options are comma-separated: ro and rw as Docker has them, and shift, which is Incus's: it maps
// the ids of a host path into the container, so that files owned by a host user are owned by the same numbers inside, instead of "nobody"
// (an unprivileged container cannot write to a host path otherwise). shift is opt-in because it needs idmapped-mount support from the
// filesystem, and a mount that cannot be made would stop the instance starting. Docker's z, Z and the macOS cache hints change nothing here
// and are accepted with a note.
func volumeDevice(v, pool string) (dev map[string]string, notes []string, err error) {
	parts := strings.Split(v, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return nil, nil, fmt.Errorf("expected SRC:DST[:ro|rw|shift]")
	}
	src, dst := parts[0], parts[1]
	if src == "" {
		return nil, nil, fmt.Errorf("the source is empty")
	}
	if !strings.HasPrefix(dst, "/") {
		return nil, nil, fmt.Errorf("the container path %q must be absolute", dst)
	}

	var ro, rw, shift bool
	if len(parts) == 3 {
		for _, o := range strings.Split(parts[2], ",") {
			switch o {
			case "ro":
				ro = true
			case "rw":
				rw = true
			case "shift":
				shift = true
			case "z", "Z", "nocopy", "cached", "delegated", "consistent":
				notes = append(notes, fmt.Sprintf("--volume %q: option %q means nothing to Incus and is ignored", v, o))
			default:
				return nil, nil, fmt.Errorf("option %q is not supported: use ro, rw or shift (z, Z, nocopy, cached, delegated and consistent are accepted and ignored)", o)
			}
		}
	}
	if ro && rw {
		return nil, nil, fmt.Errorf("ro and rw together")
	}

	isPath := strings.Contains(src, "/")
	if isPath && !strings.HasPrefix(src, "/") {
		return nil, nil, fmt.Errorf("%q is a relative path: a host path must be absolute, and it is a path on the machine Incus runs on (the server, with --remote)", src)
	}
	if shift && !isPath {
		return nil, nil, fmt.Errorf("shift applies to a host path; a managed volume is already mapped")
	}

	dev = map[string]string{"type": "disk", "source": src, "path": dst}
	if !isPath {
		dev["pool"] = pool
	}
	if ro {
		dev["readonly"] = "true"
	}
	if shift {
		dev["shift"] = "true"
	}
	if isPath && !ro && !shift {
		notes = append(notes, fmt.Sprintf("bind mount %s is mounted as it is: files owned by a host user show up as \"nobody\" in the container, so the app can read it but probably not write to it. Add :ro if it only reads, or :shift to map the ids (needs idmapped-mount support on that filesystem)", src))
	}
	return dev, notes, nil
}

var memoryRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([a-zA-Z]*)$`)

// parseMemory turns -m (Docker's 512m, 1g, 1.5g, 256mb, a plain number of bytes) into limits.memory, which takes Incus's binary suffixes.
func parseMemory(s string) (string, error) {
	m := memoryRe.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("--memory %q: expected a size such as 512m or 1g", s)
	}
	n, _ := strconv.ParseFloat(m[1], 64)
	mult := map[string]float64{"": 1, "b": 1, "k": 1 << 10, "kb": 1 << 10, "m": 1 << 20, "mb": 1 << 20, "g": 1 << 30, "gb": 1 << 30, "t": 1 << 40, "tb": 1 << 40}
	unit, ok := mult[strings.ToLower(m[2])]
	if !ok {
		return "", fmt.Errorf("--memory %q: unit %q is not one of b, k, m, g, t", s, m[2])
	}
	bytes := math.Round(n * unit)
	if bytes <= 0 {
		return "", fmt.Errorf("--memory %q: a limit must be more than zero (leave the flag off for none)", s)
	}
	b := int64(bytes)
	switch {
	case b%(1<<30) == 0:
		return fmt.Sprintf("%dGiB", b>>30), nil
	case b%(1<<20) == 0:
		return fmt.Sprintf("%dMiB", b>>20), nil
	case b%(1<<10) == 0:
		return fmt.Sprintf("%dKiB", b>>10), nil
	}
	return "", fmt.Errorf("--memory %q is not a whole number of KiB, which is what Incus takes", s)
}

// parseCPUs turns --cpus (a possibly fractional number of CPUs worth of time) into limits.cpu.allowance's hard form, "<ms>/100ms": that many
// milliseconds of CPU time in every 100. This is Docker's own meaning (CFS quota over a 100ms period), not limits.cpu, which is how many
// CPUs the instance can see.
func parseCPUs(s string) (string, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("--cpus %q: expected a number of CPUs greater than zero, such as 0.5 or 2", s)
	}
	ms := int(math.Round(f * 100))
	if ms < 1 {
		return "", fmt.Errorf("--cpus %q is less than 0.01 of a CPU", s)
	}
	return fmt.Sprintf("%dms/100ms", ms), nil
}

var permsRe = regexp.MustCompile(`^[rwm]+$`)

// deviceFlag translates --device HOST[:CONTAINER[:rwm]] into a unix-char device, which is what a serial adapter, /dev/kvm, /dev/net/tun and
// the like are. tink cannot look at the host (it may be a remote), so it cannot tell a block device: those, and a directory such as /dev/dri,
// are refused here, with the --incus-device that does it. The device is required, as in Docker: the instance does not start without it.
func deviceFlag(d string) (map[string]string, error) {
	parts := strings.Split(d, ":")
	if len(parts) > 3 {
		return nil, fmt.Errorf("expected HOST[:CONTAINER[:rwm]]")
	}
	src, dst := parts[0], parts[0]
	if len(parts) >= 2 && parts[1] != "" {
		dst = parts[1]
	}
	if len(parts) == 3 && !permsRe.MatchString(parts[2]) {
		return nil, fmt.Errorf("permissions %q: expected r, w and m (as in rwm); Incus decides the mode itself", parts[2])
	}
	if !strings.HasPrefix(src, "/dev/") || !strings.HasPrefix(dst, "/") {
		return nil, fmt.Errorf("the host device must be a path under /dev/ and the container path absolute")
	}
	if strings.HasSuffix(src, "/") || src == "/dev/dri" {
		return nil, fmt.Errorf("%q is a directory, not a device: for a GPU use --incus-device 'gpu type=gpu', for a single node name it", src)
	}
	for _, block := range []string{"/dev/sd", "/dev/hd", "/dev/vd", "/dev/xvd", "/dev/nvme", "/dev/loop", "/dev/mapper/", "/dev/dm-", "/dev/md", "/dev/mmcblk"} {
		if strings.HasPrefix(src, block) {
			return nil, fmt.Errorf("%q looks like a block device, which --device cannot pass (it makes a character device): use --incus-device 'NAME type=unix-block source=%s path=%s'", src, src, dst)
		}
	}
	return map[string]string{"type": "unix-char", "source": src, "path": dst}, nil
}

// readEnvFile reads a Docker env file: KEY=VALUE per line, blank lines and lines starting with # skipped, values taken literally (Docker does
// not strip quotes), and a bare KEY taken from the environment of the process running tink if it is set there (and skipped if not).
func readEnvFile(path string) ([][2]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("--env-file: %w", err)
	}
	var out [][2]string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		k, v, hasValue := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, " \t") {
			return nil, fmt.Errorf("--env-file %s:%d: %q is not a variable name", path, i+1, k)
		}
		if !hasValue {
			if inherited, ok := os.LookupEnv(k); ok {
				out = append(out, [2]string{k, inherited})
			}
			continue
		}
		out = append(out, [2]string{k, v})
	}
	return out, nil
}
