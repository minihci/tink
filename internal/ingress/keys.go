package ingress

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// The config keys an instance sets to register with the ingress. Everything tink reads or writes on an object's metadata lives under
// user.tink.*, so one filter finds all of it; these are in that namespace.
const (
	KeyEnabled = "user.tink.ingress.enabled"
	KeyDomain  = "user.tink.ingress.domain"
	KeyPort    = "user.tink.ingress.port"
	// KeyAddress is where to proxy to when the instance cannot be asked: a host name or an IP address, with no scheme and no port (the port is
	// KeyPort). Without it the address is read from the instance's eth0, which a virtual machine with no guest agent (Home Assistant OS, say) does
	// not report. With it, the instance is routed whether or not Incus can see its address.
	KeyAddress = "user.tink.ingress.address"
)

// The names these keys had before the rule was applied to them. They are still read, because instances in other repositories, on hosts that are
// in use, register with them, and a reconciler that stopped seeing them would delete their routes. A key under the new name wins; a notice
// asks for the rename. Nothing may add a key outside user.tink.*: cmd/tink's TestTinkKeysAreUnderTheTinkNamespace holds the line, and this
// is its one exception.
const (
	legacyEnabled = "user.ingress.enabled"
	legacyDomain  = "user.ingress.domain"
	legacyPort    = "user.ingress.port"
)

// setting reads an ingress key, preferring its current name. fromLegacy says the value came from the old name.
func setting(cfg map[string]string, key, legacy string) (value string, fromLegacy bool) {
	if v, ok := cfg[key]; ok {
		return v, false
	}
	v, ok := cfg[legacy]
	return v, ok
}

// backendHost makes the value of KeyAddress into the host part of a proxy URL, or says what is wrong with it. The value ends up inside generated
// Caddy configuration, so it is held to what a host is: an IPv4 or IPv6 address (the latter in brackets, as a URL needs it), or a DNS name.
// Anything else (a scheme, a port, a path, a space, a brace, a newline) would be read by Caddy as something other than a host.
func backendHost(v string) (string, error) {
	v = strings.TrimSpace(v)
	if ip := net.ParseIP(v); ip != nil {
		if ip.To4() == nil {
			return "[" + v + "]", nil
		}
		return v, nil
	}
	if len(v) > 253 || !hostName.MatchString(v) {
		return "", fmt.Errorf("%s is %q, which is not a host name or an IP address; give just the host, with no scheme and no port (the port is %s)", KeyAddress, v, KeyPort)
	}
	return v, nil
}

var hostName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
