package run

import (
	"fmt"
	"strconv"
	"strings"
)

// --user and the escape hatch (--incus-config, --incus-device).
//
// The escape hatch is Incus's own vocabulary, for what no flag says: anything an instance's config or devices can hold. It exists because
// most of what a Docker install line asks for beyond ports, volumes and environment (limits, sysctls, devices, tmpfs, privileged, dns...)
// is one config key or one device in Incus, and a flag each would turn tink run into the Docker clone DESIGN.md says it is not. The names are
// long on purpose: Docker already uses -c (cpu shares) and --device (a host device path), and an escape hatch should look like one.
//
// It may not silently override a flag: a key or device name something else already set is an error that names both.

// parseUser reads --user. Only numbers: a user NAME is looked up in the image's /etc/passwd, which tink cannot read, and guessing would run
// the app as the wrong user. The group is optional; when left out the image's own (normally 0) stays.
func parseUser(u string) (uid, gid string, err error) {
	us, gs, hasGroup := strings.Cut(u, ":")
	num := func(what, v string) (string, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return "", fmt.Errorf("--user %q: %s %q is not a number: only numeric UID[:GID] can be used, because a name is looked up in the image's /etc/passwd, which tink cannot read (try --user $(id -u):$(id -g))", u, what, v)
		}
		return strconv.Itoa(n), nil
	}
	if uid, err = num("the user", us); err != nil {
		return "", "", err
	}
	if hasGroup {
		if gid, err = num("the group", gs); err != nil {
			return "", "", err
		}
	}
	return uid, gid, nil
}

// applyUser sets the process user, and the ownership of any managed volume this run has to create (a new volume is root-owned, which a
// non-root process cannot write to; Incus applies initial.uid/gid when the volume is created, and never to one that exists).
func applyUser(spec *Spec, user string) error {
	if user == "" {
		return nil
	}
	uid, gid, err := parseUser(user)
	if err != nil {
		return err
	}
	spec.Config["oci.uid"] = uid
	spec.VolumeConfig = map[string]string{"initial.uid": uid}
	if gid != "" {
		spec.Config["oci.gid"] = gid
		spec.VolumeConfig["initial.gid"] = gid
	}
	return nil
}

// addIncusConfig adds --incus-config KEY=VALUE entries.
func addIncusConfig(spec *Spec, entries []string) error {
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return fmt.Errorf("--incus-config %q: expected KEY=VALUE", e)
		}
		if strings.HasPrefix(k, "volatile.") {
			return fmt.Errorf("--incus-config %q: volatile.* keys belong to Incus", k)
		}
		if k == KeyCommand {
			return fmt.Errorf("--incus-config %q: tink run records the command there itself", k)
		}
		if _, set := spec.Config[k]; set {
			return fmt.Errorf("--incus-config %q is already set by %s: use one or the other", k, flagFor(k))
		}
		spec.Config[k] = v
	}
	return nil
}

// flagFor names what sets a config key, for an error message.
func flagFor(key string) string {
	switch {
	case strings.HasPrefix(key, "environment."):
		return "-e"
	case key == "oci.entrypoint":
		return "the command after the image"
	case key == "oci.uid" || key == "oci.gid":
		return "--user"
	case key == "boot.autorestart":
		return "--restart"
	}
	return "another flag"
}

// addIncusDevices adds --incus-device 'NAME type=TYPE key=value ...' entries, the words separated by spaces as `incus config device add` takes
// them (so a value cannot contain a space). The name may not be one a flag made (eth0, proxyN, volumeN), nor repeat.
func addIncusDevices(spec *Spec, entries []string) error {
	for _, e := range entries {
		words := strings.Fields(e)
		if len(words) == 0 || strings.Contains(words[0], "=") {
			return fmt.Errorf("--incus-device %q: expected 'NAME type=TYPE key=value ...' (the device's name first)", e)
		}
		name, dev := words[0], map[string]string{}
		for _, w := range words[1:] {
			k, v, ok := strings.Cut(w, "=")
			if !ok || k == "" {
				return fmt.Errorf("--incus-device %q: %q is not key=value", e, w)
			}
			if _, dup := dev[k]; dup {
				return fmt.Errorf("--incus-device %q: %q is given twice", e, k)
			}
			dev[k] = v
		}
		if dev["type"] == "" {
			return fmt.Errorf("--incus-device %q: type=TYPE is required (nic, disk, proxy, unix-char, usb, gpu...)", e)
		}
		if _, taken := spec.Devices[name]; taken {
			return fmt.Errorf("--incus-device %q: the name %q is already used by a device another flag made (or an earlier --incus-device)", e, name)
		}
		spec.Devices[name] = dev
	}
	return nil
}

// incusConfigForCommand writes an --incus-config entry for the recorded command line, masking a value whose key looks like a secret.
func incusConfigForCommand(e string, sensitive func(string) bool) string {
	k, v, ok := strings.Cut(e, "=")
	if !ok {
		return e
	}
	if sensitive(k) {
		v = "***"
	}
	return k + "=" + v
}
