package derive

import (
	"fmt"
	"hash/fnv"
	"net/netip"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/resolve"
)

// BridgePrefix is the start of the name of every bridge the derive stage makes. tink owns the prefix: a host that does not trust unknown interfaces
// needs one firewall rule for `tink-*` and covers every present and future stack (#177).
const BridgePrefix = "tink-"

// BridgeName is the name of the bridge a stack gets: the prefix and up to ten characters of the stack's name, so the whole fits the fifteen
// characters of an interface name. A longer name is cut to five characters and a short hash of the whole, so that two long names do not meet.
func BridgeName(stack string) string {
	if len(stack) <= 10 {
		return BridgePrefix + stack
	}
	h := fnv.New32a()
	h.Write([]byte(stack))
	return fmt.Sprintf("%s%s-%04x", BridgePrefix, stack[:5], h.Sum32()&0xffff)
}

// OwnNetwork gives a stack that says `network: own` a bridge of its own and what the join needs from it (docs/network-acl.md, #177):
//
//   - a `kind: network` named BridgeName(stack), with a subnet, NAT out and no IPv6, with the ACL attached;
//   - a `kind: network-acl` named after the bridge with -in, whose ingress rules are derived from the publishes (ACLRules);
//   - the NIC of each instance that names no network, pointing at the bridge (an instance with no NIC at all gets eth0);
//   - a static address on that NIC for each instance that has an offer, because the proxy and the ACL name the app by address.
//
// Whatever the stack writes itself wins: a network or ACL of those names that the stack declares is used as it is; a NIC that names a network, or
// a device that has an address, is left alone.
//
// The subnet is chosen by tink and written into the plan, because a static address has to be known before the bridge exists, which Incus's own
// `auto` does not allow. It is taken from the stack's pool (default 10.0.0.0/8, a /24 at a time) starting at a place worked out from the stack's name
// and moving on past every subnet a network on the server already holds. Once the bridge exists its address is read back from the server and
// never chosen again, so a later plan says no changes. The addresses are the same: an instance that already has one on the server keeps it, and a
// new one takes the lowest free address of a range kept apart from the DHCP range, below every address any NIC on the server holds (every project is
// read, because Incus refuses a duplicate address across projects). Nothing is kept anywhere but on the objects.
//
// What it does not know: the host's own routes and the LAN's range. A pool that overlaps them is the person's to move (network_pool).
func OwnNetwork(server incus.InstanceServer, resources []resolve.Resource) ([]resolve.Resource, []string, error) {
	var stack resolve.Resource
	found := false
	for _, r := range resources {
		if r.Kind == resolve.KindStack && r.Network == resolve.StackNetworkOwn {
			stack, found = r, true
		}
	}
	if !found {
		return resources, nil, nil
	}
	if stack.Name == "" {
		return nil, nil, fmt.Errorf("network: own needs the stack to be named")
	}
	if server == nil {
		return nil, nil, errNoServer("the bridge, the subnet and the addresses of a stack that says network: own")
	}
	bridge := BridgeName(stack.Name)
	aclName := bridge + "-in"

	w, err := readWorld(server)
	if err != nil {
		return nil, nil, fmt.Errorf("stack %s: network: own: %w", stack.Name, err)
	}

	// What the stack declares for itself wins.
	var declaredNet *resolve.Resource
	declaredACL := false
	for i := range resources {
		switch {
		case resources[i].Kind == resolve.KindNetwork && resources[i].Name == bridge:
			declaredNet = &resources[i]
		case resources[i].Kind == resolve.KindNetworkACL && resources[i].Name == aclName:
			declaredACL = true
		}
	}

	var notices []string
	var subnet netip.Prefix
	var gateway netip.Addr
	state := "new"
	live, exists := w.networks[bridge]
	switch {
	case declaredNet != nil:
		cidr := declaredNet.Config["ipv4.address"]
		p, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, nil, fmt.Errorf("stack %s: network: own: the network %s the stack declares needs an ipv4.address in CIDR form (%q), because the apps' addresses are taken from it", stack.Name, bridge, cidr)
		}
		gateway, subnet, state = p.Addr(), p.Masked(), "declared by the stack"
	case exists:
		if !live.Managed {
			return nil, nil, fmt.Errorf("stack %s: network: own: a network called %s exists and Incus does not manage it; tink will not use it", stack.Name, bridge)
		}
		if owner := ownership.Owner(live.Config); owner != "" && owner != stack.Name {
			return nil, nil, fmt.Errorf("stack %s: network: own: the network %s belongs to the stack %q (its %s stamp); the name is taken from the stack's name, so rename one of them", stack.Name, bridge, owner, ownership.Key)
		}
		p, err := netip.ParsePrefix(live.Config["ipv4.address"])
		if err != nil {
			return nil, nil, fmt.Errorf("stack %s: network: own: the network %s exists with no IPv4 address (%q), so there is no subnet to take the apps' addresses from", stack.Name, bridge, live.Config["ipv4.address"])
		}
		gateway, subnet, state = p.Addr(), p.Masked(), "exists"
	default:
		pool := stack.NetworkPool
		if pool == "" {
			pool = resolve.DefaultNetworkPool
		}
		prefix := stack.NetworkPrefix
		if prefix == 0 {
			prefix = resolve.DefaultNetworkPrefix
		}
		subnet, err = chooseSubnet(stack.Name, netip.MustParsePrefix(pool), prefix, w.subnets)
		if err != nil {
			return nil, nil, fmt.Errorf("stack %s: network: own: %w", stack.Name, err)
		}
		gateway = subnet.Addr().Next()
	}

	static, dhcpRange, err := divide(subnet)
	if err != nil {
		return nil, nil, fmt.Errorf("stack %s: network: own: %w", stack.Name, err)
	}

	var added []resolve.Resource
	if declaredNet == nil {
		added = append(added, resolve.Resource{
			Kind:        resolve.KindNetwork,
			Name:        bridge,
			Description: fmt.Sprintf("the %s stack's bridge", stack.Name),
			Config: map[string]string{
				"ipv4.address":     fmt.Sprintf("%s/%d", gateway, subnet.Bits()),
				"ipv4.nat":         "true",
				"ipv4.dhcp.ranges": dhcpRange,
				"ipv6.address":     "none",
				"security.acls":    aclName,
			},
		})
	}
	if !declaredACL {
		added = append(added, resolve.Resource{
			Kind:          resolve.KindNetworkACL,
			Name:          aclName,
			Description:   fmt.Sprintf("only the ingress helper may reach the %s stack's apps", stack.Name),
			DeriveIngress: true,
		})
	}
	for _, a := range added {
		if err := resolve.Validate(a); err != nil {
			return nil, nil, fmt.Errorf("stack %s: network: own: the %s it would make is not valid: %w", stack.Name, a.Kind, err)
		}
	}
	notices = append(notices, fmt.Sprintf("stack %s: network: own: bridge %s %s (%s), ACL %s", stack.Name, bridge, subnet, state, aclName))

	// The NICs, and the addresses of what is published.
	taken := map[netip.Addr]bool{}
	for a := range w.used {
		taken[a] = true
	}
	for i := range resources {
		r := &resources[i]
		if r.Kind != resolve.KindInstance {
			continue
		}
		nic := manageNIC(r, bridge)
		if nic == "" || !hasOffer(*r) || r.Devices[nic]["ipv4.address"] != "" {
			continue
		}
		addr, reused := w.liveAddress(*r, nic, subnet)
		if !reused {
			var ok bool
			addr, ok = lowestFree(static, taken)
			if !ok {
				return nil, nil, fmt.Errorf("stack %s: network: own: no free static address left in %s for instance/%s (%d are kept apart from the DHCP range)", stack.Name, subnet, r.Name, len(static))
			}
		}
		taken[addr] = true
		r.Devices[nic]["ipv4.address"] = addr.String()
		if r.Devices[nic]["name"] == "" {
			r.Devices[nic]["name"] = nic // an OCI container's NIC with a static address wants its name said (docs/oci-network.md)
		}
		verb := "allocated"
		if reused {
			verb = "kept"
		}
		notices = append(notices, fmt.Sprintf("instance/%s: address %s on %s %s", r.Name, addr, bridge, verb))
	}
	return append(resources, added...), notices, nil
}

// manageNIC points an instance at the bridge when it names no network, and returns the name of the NIC that is on the bridge ("" when the
// instance has a NIC of its own choosing that is left alone). A NIC device with no network and no parent is the one a stack's defaults: or a person
// left unfinished; with no NIC at all, eth0 is added.
func manageNIC(r *resolve.Resource, bridge string) string {
	if r.Devices == nil {
		r.Devices = map[string]map[string]string{}
	}
	names := make([]string, 0, len(r.Devices))
	for n, d := range r.Devices {
		if d["type"] == "nic" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if r.Devices[n]["network"] == bridge {
			return n
		}
	}
	for _, n := range names {
		d := r.Devices[n]
		if d["network"] == "" && d["parent"] == "" {
			copied := map[string]string{}
			for k, v := range d {
				copied[k] = v
			}
			copied["network"] = bridge
			r.Devices[n] = copied
			return n
		}
	}
	if len(names) > 0 {
		return ""
	}
	r.Devices["eth0"] = map[string]string{"type": "nic", "name": "eth0", "network": bridge}
	return "eth0"
}

func hasOffer(r resolve.Resource) bool {
	for k := range r.Config {
		if strings.HasPrefix(k, offerKey) {
			return true
		}
	}
	return false
}

const offerKey = "user.tink.offer."

// world is what the server already holds that a new bridge and address must stay clear of.
type world struct {
	networks map[string]api.Network // by name
	subnets  []netip.Prefix         // the IPv4 subnet of every network that has one
	used     map[netip.Addr]bool    // every static IPv4 address on a NIC of any instance of any project
	live     map[string]map[string]string
}

func readWorld(server incus.InstanceServer) (*world, error) {
	w := &world{networks: map[string]api.Network{}, used: map[netip.Addr]bool{}, live: map[string]map[string]string{}}
	nets, err := server.GetNetworks()
	if err != nil {
		return nil, fmt.Errorf("listing the networks: %w", err)
	}
	for _, n := range nets {
		w.networks[n.Name] = n
		if p, err := netip.ParsePrefix(n.Config["ipv4.address"]); err == nil {
			w.subnets = append(w.subnets, p.Masked())
		}
	}
	insts, err := server.GetInstancesAllProjects(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("listing the instances of every project: %w", err)
	}
	for _, in := range insts {
		devs := in.ExpandedDevices
		if len(devs) == 0 {
			devs = in.Devices
		}
		for _, d := range devs {
			if d["type"] != "nic" {
				continue
			}
			if a, ok := parseAddr(d["ipv4.address"]); ok {
				w.used[a] = true
			}
		}
		for name, d := range in.Devices { // the instance's own NICs: an address it already has is kept
			if d["type"] == "nic" && d["ipv4.address"] != "" {
				w.live[liveKey(in.Project, in.Name, name)] = d
			}
		}
	}
	return w, nil
}

func liveKey(project, instance, nic string) string {
	if project == "" {
		project = "default"
	}
	return project + "/" + instance + "/" + nic
}

// liveAddress is the address the instance's NIC already has on the server, if it is inside the subnet. It is how a second plan reads the first one's
// choice instead of choosing again.
func (w *world) liveAddress(r resolve.Resource, nic string, subnet netip.Prefix) (netip.Addr, bool) {
	d, ok := w.live[liveKey(r.Project, r.Name, nic)]
	if !ok {
		return netip.Addr{}, false
	}
	a, ok := parseAddr(d["ipv4.address"])
	if !ok || !subnet.Contains(a) {
		return netip.Addr{}, false
	}
	return a, true
}

func parseAddr(s string) (netip.Addr, bool) {
	if s == "" {
		return netip.Addr{}, false
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Addr(), true
	}
	a, err := netip.ParseAddr(s)
	return a, err == nil && a.Is4()
}

// chooseSubnet takes a subnet of the given length out of pool: the one a hash of the stack's name points at, or the next one that overlaps nothing in taken.
func chooseSubnet(stack string, pool netip.Prefix, prefix int, taken []netip.Prefix) (netip.Prefix, error) {
	n := 1 << (prefix - pool.Bits())
	h := fnv.New32a()
	h.Write([]byte(stack))
	start := int(h.Sum32() % uint32(n))
	base := addrToUint(pool.Addr())
	for i := 0; i < n; i++ {
		idx := (start + i) % n
		cand := netip.PrefixFrom(uintToAddr(base+uint32(idx)<<(32-prefix)), prefix)
		clash := false
		for _, t := range taken {
			if cand.Overlaps(t) {
				clash = true
				break
			}
		}
		if !clash {
			return cand, nil
		}
	}
	return netip.Prefix{}, fmt.Errorf("every /%d subnet of %s overlaps a network the server already has: give the stack a network_pool with room", prefix, pool)
}

// divide divides a subnet's addresses: the gateway is the first, then a run kept for static addresses (a quarter of the hosts, at least four), then the
// DHCP range for everything else, so a lease handed out later never lands on an address tink allocated.
func divide(subnet netip.Prefix) (static []netip.Addr, dhcp string, err error) {
	hosts := 1<<(32-subnet.Bits()) - 2
	if hosts < 6 {
		return nil, "", fmt.Errorf("%s has too few addresses", subnet)
	}
	n := hosts / 4
	if n < 4 {
		n = 4
	}
	first := addrToUint(subnet.Addr()) + 2 // the gateway is the first host
	for i := 0; i < n; i++ {
		static = append(static, uintToAddr(first+uint32(i)))
	}
	start := uintToAddr(first + uint32(n))
	last := uintToAddr(addrToUint(subnet.Addr()) + uint32(hosts)) // the last host
	return static, fmt.Sprintf("%s-%s", start, last), nil
}

func lowestFree(static []netip.Addr, taken map[netip.Addr]bool) (netip.Addr, bool) {
	for _, a := range static {
		if !taken[a] {
			return a, true
		}
	}
	return netip.Addr{}, false
}

func addrToUint(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func uintToAddr(u uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(u >> 24), byte(u >> 16), byte(u >> 8), byte(u)})
}
