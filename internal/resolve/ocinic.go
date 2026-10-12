package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

// An OCI app container's network is set up by Incus's own `incusd forknet dhcp`, not by an init system in the image. Incus writes a record of what each
// interface should look like (network/interfaces.json) and that process runs a DHCP client on every interface unless the record says the interface is
// statically configured. The record is built from the container's NIC devices, but ONLY the ones that carry a `name:` key (the interface's name inside the
// container); a device without one is left out, and an interface that is not in the record is treated as fully dynamic: DHCP runs, and the lease's default
// route and nameservers apply. The settings that would have said otherwise are never read:
//
//	ipv4.address / ipv6.address   a CIDR (static) or `none` (no DHCP for that family)
//	ipv4.gateway / ipv6.gateway   an address, or `none` (no default route from the lease)
//
// Nothing tells the person: the Incus NIC reference documents those keys without saying that `name:` is required, the instance starts, `plan` compares only the
// Incus configuration so it says `no changes`, and the container simply has a second address, the lease's default route and the router's nameservers next to
// the static ones. Found in the Incus v7.5.1 source (internal/server/instance/drivers/driver_lxc.go, cmd/incusd/main_forknet.go) and on a live host, where
// the record was `{}`; see docs/oci-network.md.
//
// ociNICWarnings is the check that closes that gap on the tink side. It is a warning, not an error, and it speaks only when it would matter: an unnamed NIC with
// none of those four settings behaves the same with or without a name, and a bare address on a managed network is a DHCP reservation, which is dynamic either way.

// staticNICKeys are the NIC settings an OCI container honours only on a named interface.
var staticNICKeys = []string{"ipv4.address", "ipv4.gateway", "ipv6.address", "ipv6.gateway"}

// declaredProfiles indexes the stack's profile resources by name, for the NIC devices an instance gets from them. Names are unique across a stack.
func declaredProfiles(resources []Resource) map[string]Resource {
	out := map[string]Resource{}
	for _, r := range resources {
		if r.Kind == KindProfile {
			out[r.Name] = r
		}
	}
	return out
}

// isOCIImage reports whether image names a remote that is an OCI registry. Images are already qualified when planning (QualifyImages), so a registry image
// is written remote:ref; a local alias or an image the stack builds is not.
func isOCIImage(image string, remotes map[string]cliconfig.Remote) bool {
	name, _, ok := strings.Cut(image, ":")
	if !ok {
		return false
	}
	remote, known := remotes[name]
	return known && remote.Protocol == "oci"
}

// setsStaticNIC lists which of the static settings a NIC device carries with a value that differs from what an unnamed (fully dynamic) interface does anyway.
func setsStaticNIC(dev map[string]string) []string {
	var set []string
	for _, k := range staticNICKeys {
		v := dev[k]
		switch {
		case v == "":
		case strings.HasSuffix(k, ".address"):
			if v == "none" || strings.Contains(v, "/") {
				set = append(set, k)
			}
		default: // a gateway: an address, or none
			set = append(set, k)
		}
	}
	return set
}

// ociNICWarnings returns one warning per NIC device of an OCI instance that sets a static setting but has no name. It looks at the instance's own devices and at
// the devices of the profiles the stack declares (an instance's device of the same name replaces the profile's whole device, as in Incus); a profile that is
// not in the stack cannot be seen, and is not guessed at.
func ociNICWarnings(r Resource, remotes map[string]cliconfig.Remote, profiles map[string]Resource) []string {
	if r.Kind != KindInstance || !isOCIImage(r.Image, remotes) {
		return nil
	}
	type nic struct {
		dev  map[string]string
		from string // "" for the instance's own device, else the profile
	}
	effective := map[string]nic{}
	for _, p := range r.Profiles {
		for name, dev := range profiles[p].Devices {
			if dev["type"] == "nic" {
				effective[name] = nic{dev: dev, from: p}
			} else {
				delete(effective, name)
			}
		}
	}
	for name, dev := range r.Devices {
		if dev["type"] == "nic" {
			effective[name] = nic{dev: dev}
		} else {
			delete(effective, name)
		}
	}

	names := make([]string, 0, len(effective))
	for name := range effective {
		names = append(names, name)
	}
	sort.Strings(names)

	var out []string
	for _, name := range names {
		n := effective[name]
		if n.dev["name"] != "" {
			continue
		}
		set := setsStaticNIC(n.dev)
		if len(set) == 0 {
			continue
		}
		where := fmt.Sprintf("NIC device %q", name)
		if n.from != "" {
			where += fmt.Sprintf(" (from profile %q)", n.from)
		}
		out = append(out, fmt.Sprintf("%s sets %s but has no name:. Incus's OCI network setup records only named NICs, so this interface is treated as fully dynamic: DHCP runs, the lease's default route and nameservers apply, and those settings are ignored. Add name: with the interface's name inside the container (eth1, say); see docs/oci-network.md",
			where, strings.Join(set, ", ")))
	}
	return out
}

// imageRemotes returns the remotes planning knows, from the shared image environment when there is one.
func (o PlanOptions) imageRemotes() map[string]cliconfig.Remote {
	if o.env != nil {
		return o.env.remotes()
	}
	return newImageEnv(true).remotes()
}
