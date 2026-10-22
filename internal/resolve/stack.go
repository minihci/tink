package resolve

import (
	"fmt"
	"net"
	"regexp"
)

var stackNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// validateStack rejects a `kind: stack` whose name could not be kept as a config value people read: lower case letters,
// digits, '.', '_' and '-'. The declaration is a name and, optionally, the project the stack's resources default to
// (stackproject.go), and whether tink gives it a bridge of its own (network: own); it says nothing else.
func validateStack(r Resource) error {
	if r.Kind != KindStack {
		return nil
	}
	if !stackNamePattern.MatchString(r.Name) {
		return fmt.Errorf("resource %q: a stack's name is lower case letters, digits, '.', '_' and '-' (at most 63, starting with a letter or digit)", r.Name)
	}
	if err := validateStackSite(r); err != nil {
		return err
	}
	return validateStackNetwork(r)
}

// The bridge a stack asks for with `network: own` (internal/derive makes it). DefaultNetworkPool and DefaultNetworkPrefix are where its subnet comes from
// when the stack says nothing: a /24 out of 10.0.0.0/8, taken the way Incus's own `auto` does, but chosen by tink so that the address of each app can be
// written into the plan before the bridge exists.
const (
	StackNetworkOwn      = "own"
	DefaultNetworkPool   = "10.0.0.0/8"
	DefaultNetworkPrefix = 24
	// MaxNetworkPrefix is the longest subnet the derive stage will make: it keeps a few static addresses and a DHCP range apart (a /29 has six hosts).
	MaxNetworkPrefix = 29
)

func validateStackNetwork(r Resource) error {
	if r.Network == "" {
		if r.NetworkPool != "" || r.NetworkPrefix != 0 {
			return fmt.Errorf("resource %q: network_pool and network_prefix need network: %s", r.Name, StackNetworkOwn)
		}
		return nil
	}
	if r.Network != StackNetworkOwn {
		return fmt.Errorf("resource %q: network %q is not understood on a stack: the only value is %q (a bridge of the stack's own); write a kind: network to name one", r.Name, r.Network, StackNetworkOwn)
	}
	pool := r.NetworkPool
	if pool == "" {
		pool = DefaultNetworkPool
	}
	ip, ipnet, err := net.ParseCIDR(pool)
	if err != nil || ip.To4() == nil {
		return fmt.Errorf("resource %q: network_pool %q is not an IPv4 range such as %s", r.Name, pool, DefaultNetworkPool)
	}
	if !ip.Equal(ipnet.IP) {
		return fmt.Errorf("resource %q: network_pool %q has host bits set: did you mean %s?", r.Name, pool, ipnet)
	}
	bits, _ := ipnet.Mask.Size()
	prefix := r.NetworkPrefix
	if prefix == 0 {
		prefix = DefaultNetworkPrefix
	}
	if prefix < bits || prefix > MaxNetworkPrefix {
		return fmt.Errorf("resource %q: network_prefix %d must be between the pool's %d and %d", r.Name, prefix, bits, MaxNetworkPrefix)
	}
	return nil
}

// StackName is the name the stack gives itself with a `kind: stack` document, or "" when it does not. A stack is one
// thing: two declarations, even in different files, are an error.
//
// The declaration is metadata, not a resource: it is left out of the dependency graph (Levels), so a stack can be named
// after an instance or a volume it contains without the two colliding.
func StackName(resources []Resource) (string, error) {
	name := ""
	for _, r := range resources {
		if r.Kind != KindStack {
			continue
		}
		if name != "" {
			return "", fmt.Errorf("a stack is named once: found kind: stack %q and %q", name, r.Name)
		}
		name = r.Name
	}
	return name, nil
}
