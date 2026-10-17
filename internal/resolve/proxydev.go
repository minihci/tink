package resolve

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

// An instance's `proxy` device with `nat: "true"` is how a host with one public address carries a port to an instance without a userspace relay: Incus writes a
// destination-scoped nftables rule (`ip daddr <listen> tcp dport <port> dnat to <connect>`), and the instance sees the client's real address. Measured on
// Incus 7.5.1; see docs/public-exposure.md. Incus then says nothing about two mistakes that are easy to make in a stack:
//
//   - A NAT proxy connects to one of the instance's STATIC addresses. With a connect address that is not an IP, Incus refuses the device when it is added; with an
//     IP the instance does not have, or an instance whose NIC has no static address (a DHCP lease is not one), the instance is created and then cannot start
//     ("Connect IP ... must be one of the instance's static IPv4 addresses"). `plan` called it `would create`, and `apply` retried the start and left it stopped.
//
//   - Two host-side listeners on the same address and port are accepted. Both instances run, both rules exist, and the one that matches first answers: the
//     other never sees traffic, and nothing says so.
//
// natProxyWarnings and proxyListenerNotes close those on the tink side. Like ociNICWarnings they are warnings, not errors, and they are about the stack: they
// appear on the next plan whether or not the instance exists yet. What they cannot see is not guessed at: a profile the stack does not declare, an instance in
// another stack, and a rule that lives outside Incus.

// portRange is an inclusive range of ports; a single port has lo == hi.
type portRange struct{ lo, hi int }

// proxyEndpoint is a parsed proxy device `listen` or `connect` value: proto:address:ports.
type proxyEndpoint struct {
	proto string
	addr  string // an IP address, a name, or "" when none was written; a wildcard is "0.0.0.0" or "::"
	ports []portRange
}

// parseProxyEndpoint reads proto:address:ports, where ports is a port, a range (8000-8010) or a comma list of them, and an IPv6 address is in brackets. A unix
// socket, or anything that does not parse, is reported as not ok: Incus validates the value, and tink does not second-guess what it cannot read.
func parseProxyEndpoint(s string) (proxyEndpoint, bool) {
	proto, rest, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok || proto == "" || proto == "unix" {
		return proxyEndpoint{}, false
	}
	var addr, ports string
	if strings.HasPrefix(rest, "[") {
		end := strings.Index(rest, "]")
		if end < 0 || !strings.HasPrefix(rest[end+1:], ":") {
			return proxyEndpoint{}, false
		}
		addr, ports = rest[1:end], rest[end+2:]
	} else {
		i := strings.LastIndex(rest, ":")
		if i < 0 {
			return proxyEndpoint{}, false
		}
		addr, ports = rest[:i], rest[i+1:]
	}
	ep := proxyEndpoint{proto: proto, addr: addr}
	for _, item := range strings.Split(ports, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(item), "-")
		l, err := strconv.Atoi(lo)
		if err != nil || l < 1 || l > 65535 {
			return proxyEndpoint{}, false
		}
		h := l
		if isRange {
			if h, err = strconv.Atoi(hi); err != nil || h < l || h > 65535 {
				return proxyEndpoint{}, false
			}
		}
		ep.ports = append(ep.ports, portRange{l, h})
	}
	return ep, len(ep.ports) > 0
}

// family is 4 or 6 for an address of that family (a wildcard included), and 0 for none written or one that is not an IP address.
func (e proxyEndpoint) family() int {
	a, err := netip.ParseAddr(e.addr)
	switch {
	case err != nil:
		return 0
	case a.Unmap().Is4():
		return 4
	default:
		return 6
	}
}

func (e proxyEndpoint) wildcard() bool {
	if a, err := netip.ParseAddr(e.addr); err == nil {
		return a.IsUnspecified()
	}
	return e.addr == ""
}

// overlaps reports whether two listeners would take the same traffic: the same protocol, addresses that are equal or one a wildcard of the other's family,
// and ports that intersect.
func (e proxyEndpoint) overlaps(o proxyEndpoint) bool {
	if e.proto != o.proto || !e.addressOverlaps(o) {
		return false
	}
	for _, a := range e.ports {
		for _, b := range o.ports {
			if a.lo <= b.hi && b.lo <= a.hi {
				return true
			}
		}
	}
	return false
}

func (e proxyEndpoint) addressOverlaps(o proxyEndpoint) bool {
	ea, eerr := netip.ParseAddr(e.addr)
	oa, oerr := netip.ParseAddr(o.addr)
	if eerr != nil || oerr != nil {
		return false // a name is not a listen address Incus accepts; there is nothing to compare
	}
	if ea.Unmap() == oa.Unmap() {
		return true
	}
	if e.wildcard() && (o.family() == e.family() || e.family() == 0) {
		return true
	}
	return o.wildcard() && (e.family() == o.family() || o.family() == 0)
}

// isTrueValue reads an Incus boolean the way Incus does.
func isTrueValue(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// effectiveInstanceDevices returns the devices an instance ends up with: the devices of the profiles it lists the stack declares, in order, then its own, where a
// device of the same name replaces the earlier one whole, as in Incus. A profile the stack does not declare contributes nothing.
func effectiveInstanceDevices(r Resource, profiles map[string]Resource) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, p := range r.Profiles {
		for name, dev := range profiles[p].Devices {
			out[name] = dev
		}
	}
	for name, dev := range r.Devices {
		out[name] = dev
	}
	return out
}

// hostProxy reports whether dev is a proxy device that listens on the host, not inside the instance.
func hostProxy(dev map[string]string) bool {
	return dev["type"] == "proxy" && dev["bind"] != "container" && dev["bind"] != "instance"
}

// undeclaredProfiles lists the profiles r names that the stack does not declare, which a check on r's devices could not see.
func undeclaredProfiles(r Resource, profiles map[string]Resource) []string {
	var out []string
	for _, p := range r.Profiles {
		if _, ok := profiles[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// staticAddresses lists the static addresses the NIC devices in devs declare: an ipv4.address or ipv6.address that is an address, with or without a prefix.
// `none` and the empty value are not addresses.
func staticAddresses(devs map[string]map[string]string) []netip.Addr {
	var out []netip.Addr
	for _, dev := range devs {
		if dev["type"] != "nic" {
			continue
		}
		for _, k := range []string{"ipv4.address", "ipv6.address"} {
			v, _, _ := strings.Cut(strings.TrimSpace(dev[k]), "/")
			if a, err := netip.ParseAddr(v); err == nil {
				out = append(out, a.Unmap())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// natProxyWarnings returns one warning per NAT proxy device of an instance whose connect address Incus will refuse or cannot start with: one that is not an IP
// address, or that is not a static address of a NIC the instance declares (its own, or from a profile the stack declares).
func natProxyWarnings(r Resource, profiles map[string]Resource) []string {
	if r.Kind != KindInstance {
		return nil
	}
	devs := effectiveInstanceDevices(r, profiles)
	names := make([]string, 0, len(devs))
	for name, dev := range devs {
		if hostProxy(dev) && isTrueValue(dev["nat"]) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var out []string
	for _, name := range names {
		connect, ok := parseProxyEndpoint(devs[name]["connect"])
		if !ok {
			continue
		}
		ip, err := netip.ParseAddr(connect.addr)
		if err != nil {
			out = append(out, fmt.Sprintf("proxy device %q has nat on but connects to %q, which is not an IP address. Incus refuses a NAT proxy whose connect address is not an IP address, so the device cannot be added; see docs/public-exposure.md",
				name, connect.addr))
			continue
		}
		static := staticAddresses(devs)
		found := false
		for _, a := range static {
			if a == ip.Unmap() {
				found = true
			}
		}
		if found {
			continue
		}
		have := "none of its NICs declares a static address (a DHCP lease is not one)"
		if len(static) > 0 {
			shown := make([]string, len(static))
			for i, a := range static {
				shown[i] = a.String()
			}
			have = "the static addresses its NICs declare are " + strings.Join(shown, ", ")
		}
		msg := fmt.Sprintf("proxy device %q has nat on and connects to %s, which is not a static address of this instance: %s. Incus's NAT mode needs one and cannot start the instance without it (tink retries the start and leaves it stopped). Give the NIC an ipv4.address, or connect to an address it has; see docs/public-exposure.md",
			name, ip, have)
		if un := undeclaredProfiles(r, profiles); len(un) > 0 {
			msg += fmt.Sprintf(" (profiles %s are not declared in this stack, so their NICs were not consulted)", strings.Join(un, ", "))
		}
		out = append(out, msg)
	}
	return out
}

// proxyListenerNotes compares the host-side listeners of every instance of the stack and returns, by instance name, a note for each other listener it overlaps.
// A listener is global to the host, so the comparison ignores projects. A pair is reported once to each instance involved, and a pair inside one instance is
// reported to it once.
func proxyListenerNotes(resources []Resource, profiles map[string]Resource) map[string][]string {
	type listener struct {
		instance, device, raw string
		ep                    proxyEndpoint
	}
	var all []listener
	instances := make([]Resource, 0, len(resources))
	for _, r := range resources {
		if r.Kind == KindInstance {
			instances = append(instances, r)
		}
	}
	sort.Slice(instances, func(i, j int) bool { return instances[i].Name < instances[j].Name })
	for _, r := range instances {
		devs := effectiveInstanceDevices(r, profiles)
		names := make([]string, 0, len(devs))
		for name := range devs {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if !hostProxy(devs[name]) {
				continue
			}
			if ep, ok := parseProxyEndpoint(devs[name]["listen"]); ok {
				all = append(all, listener{r.Name, name, strings.TrimSpace(devs[name]["listen"]), ep})
			}
		}
	}
	notes := map[string][]string{}
	for i := range all {
		for j := i + 1; j < len(all); j++ {
			a, b := all[i], all[j]
			if !a.ep.overlaps(b.ep) {
				continue
			}
			const tail = "Incus accepts two proxies on overlapping listen addresses and ports without complaint, and the rule written first answers, so one of them never sees traffic. This check covers this stack only"
			if a.instance == b.instance {
				notes[a.instance] = append(notes[a.instance], fmt.Sprintf("proxy devices %q (%s) and %q (%s) of this instance listen on overlapping addresses and ports. %s",
					a.device, a.raw, b.device, b.raw, tail))
				continue
			}
			notes[a.instance] = append(notes[a.instance], fmt.Sprintf("proxy device %q listens on %s, which overlaps instance/%s's device %q (%s). %s",
				a.device, a.raw, b.instance, b.device, b.raw, tail))
			notes[b.instance] = append(notes[b.instance], fmt.Sprintf("proxy device %q listens on %s, which overlaps instance/%s's device %q (%s). %s",
				b.device, b.raw, a.instance, a.device, a.raw, tail))
		}
	}
	if len(notes) == 0 {
		return nil
	}
	return notes
}
