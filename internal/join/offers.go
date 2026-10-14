package join

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/minihci/tink/internal/resolve"
)

// The keys an instance states about itself. They all live under user.tink.* (cmd/tink/keynamespace_test.go holds the line), and they say what the
// app PROVIDES, never who may use it: that is a publish's allow list.
const (
	roleKey    = "user.tink.role"
	offerKey   = "user.tink.offer."
	legKey     = "user.tink.leg."
	zoneKey    = "user.tink.dns.zone"
	protoHTTP  = "http"
	maxPortNum = 65535
)

// An Offer is one thing an instance provides: a port speaking a protocol, on one of its NICs.
//
//	user.tink.offer.web.port: "8123"
//	user.tink.offer.web.proto: http
//	user.tink.offer.web.listen: eth0     # optional: the NIC it listens on; needed only when the instance has more than one
type Offer struct {
	Name   string
	Port   int
	Proto  string
	Listen string // the NIC's device name, or "" when the instance has exactly one
}

// parseOffers reads the offers an instance states, strictly: a key tink does not know is an error and not something to skip, in the same spirit as the
// resolver's KnownFields, because a misspelled attribute would otherwise leave an offer half-stated without a word.
func parseOffers(inst resolve.Resource) (map[string]Offer, []Finding) {
	subject := "instance/" + inst.Name
	var findings []Finding
	raw := map[string]map[string]string{}
	keys := make([]string, 0, len(inst.Config))
	for k := range inst.Config {
		if strings.HasPrefix(k, offerKey) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		name, attr, ok := strings.Cut(strings.TrimPrefix(k, offerKey), ".")
		if !ok || !resolve.OfferNamePattern(name) {
			findings = append(findings, Finding{Error, subject, fmt.Sprintf("%s: expected %s<offer>.<port|proto|listen>, with an offer name of lower case letters, digits and '-'", k, offerKey)})
			continue
		}
		switch attr {
		case "port", "proto", "listen":
			if raw[name] == nil {
				raw[name] = map[string]string{}
			}
			raw[name][attr] = strings.TrimSpace(inst.Config[k])
		default:
			findings = append(findings, Finding{Error, subject, fmt.Sprintf("%s: %q is not an offer attribute (known: port, proto, listen)", k, attr)})
		}
	}
	out := map[string]Offer{}
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		a := raw[n]
		if a["port"] == "" || a["proto"] == "" {
			findings = append(findings, Finding{Error, subject, fmt.Sprintf("offer %q needs both %s%s.port and %s%s.proto", n, offerKey, n, offerKey, n)})
			continue
		}
		port, err := strconv.Atoi(a["port"])
		if err != nil || port < 1 || port > maxPortNum {
			findings = append(findings, Finding{Error, subject, fmt.Sprintf("offer %q: port %q is not a port number (1-%d)", n, a["port"], maxPortNum)})
			continue
		}
		out[n] = Offer{Name: n, Port: port, Proto: a["proto"], Listen: a["listen"]}
	}
	return out, findings
}

// effectiveDevices is what Incus will give the instance: the devices of its profiles in order, then its own, a device of the same name replacing the
// earlier one whole (as in Incus, and as the resolver's own NIC checks read it). A profile the stack files do not declare cannot be seen, and is not guessed at.
func effectiveDevices(inst resolve.Resource, profiles map[key]resolve.Resource) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, p := range inst.Profiles {
		prof, ok := profiles[key{projectOf(inst), p}]
		if !ok {
			prof, ok = profiles[key{"default", p}]
		}
		if !ok {
			continue
		}
		for n, d := range prof.Devices {
			out[n] = d
		}
	}
	for n, d := range inst.Devices {
		out[n] = d
	}
	return out
}

// staticAddress returns the IPv4 address a NIC device is given, without its prefix length, or "" when it takes one from DHCP (or is not valid).
func staticAddress(dev map[string]string) string {
	a, _, _ := strings.Cut(strings.TrimSpace(dev["ipv4.address"]), "/")
	ip, err := netip.ParseAddr(a)
	if err != nil || !ip.Is4() {
		return ""
	}
	return ip.String()
}
