package helper

// How the helper reaches the host's Incus API. There are two ways, and `install` chooses one when it creates the helper:
//
//   - network (the default where it is possible): the helper's NIC is on a managed bridge, the host has an address on that bridge (the gateway),
//     and the API listener covers it. The helper's `host` remote is registered at https://<gateway>:<port>, and the instance has NO proxy device.
//   - proxy: a `proxy` device (bind container) gives the helper the host's API on its own loopback, and the `host` remote is registered at
//     https://127.0.0.1:<port>. It works wherever the API is reachable from the host itself, whatever the helper's network is.
//
// Why network is the default. A restricted Incus project refuses proxy devices unless it is told to allow them (restricted.devices.proxy), and
// while an instance carries one every update of that instance is invalid there; so a helper that lives in a restricted project cannot be
// updated at all. A proxy device is also a host-reach primitive: a proxy to the Incus unix socket is root on the host, which is precisely what
// a restricted project exists to refuse. Going over the network asks the project for nothing: a NIC on a managed bridge, which the helper has
// anyway. Measured on the lab host: a container on a managed bridge (tink0) reaches the host's API port at the bridge's gateway address when
// core.https_address is a wildcard **[lab]**.
//
// What network mode costs: the API port is reachable by every container on the bridge, not only by the helper (a proxy device was private to the
// helper). It is still a TLS port that trusts only certificates in the trust store, but an egress ACL rule on the bridge's other instances is how
// to limit it to the helper (docs/helper.md); and the host's listener must cover the gateway, which a wildcard does and a loopback address does not.
//
// Nothing here changes an existing helper: its devices are left as they are (`tink helper upgrade` recreates the instance with the same devices),
// and a request for the other shape is refused with the way to get it, not applied behind the person's back.

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// APIVia says how the helper reaches the host's API: see the comment at the top of this file.
type APIVia string

const (
	// APIViaAuto is network when it is possible and proxy when it is not, with a note saying why.
	APIViaAuto APIVia = "auto"
	// APIViaNetwork forces the network, and is an error when it is impossible.
	APIViaNetwork APIVia = "network"
	// APIViaProxy is a proxy device on the helper's loopback.
	APIViaProxy APIVia = "proxy"

	// apiDevice is the name of the proxy device.
	apiDevice = "api"
)

// ParseAPIVia reads the value of --api-via. Empty means auto.
func ParseAPIVia(s string) (APIVia, error) {
	switch v := APIVia(strings.ToLower(strings.TrimSpace(s))); v {
	case "", APIViaAuto:
		return APIViaAuto, nil
	case APIViaNetwork, APIViaProxy:
		return v, nil
	}
	return "", fmt.Errorf("--api-via %q: expected auto, network or proxy", s)
}

// APIRoute is how an installed helper reaches the host's API, read from its devices: proxy when it has the proxy device `api`, network when it
// has not. It is the shape `upgrade` keeps, and what `kind: helper` planning mentions.
func (f Found) APIRoute() APIVia {
	if f.Devices[apiDevice]["type"] == "proxy" {
		return APIViaProxy
	}
	return APIViaNetwork
}

// apiRoute is the way chosen for one helper.
type apiRoute struct {
	// Via is APIViaNetwork or APIViaProxy, never auto.
	Via APIVia
	// Remote is the address the helper's `host` remote is registered at, inside the helper (the token carries the server's certificate
	// fingerprint, which pins it; the address is the only thing that differs between the two ways).
	Remote string
	// Port is the port of the API listener.
	Port string
	// Connect is where on the host the proxy device connects (proxy only).
	Connect string
	// Note is one line for the person running install, or "".
	Note string
}

// proxyDevice is the helper's `api` device, or nil when it has none (network mode).
func (r apiRoute) proxyDevice() map[string]string {
	if r.Via != APIViaProxy {
		return nil
	}
	// the host's API, on the container's own loopback: the helper reaches it without it being exposed to anything new
	return map[string]string{"type": "proxy", "bind": "container", "listen": "tcp:127.0.0.1:" + r.Port, "connect": "tcp:" + r.Connect}
}

// explain adds to a failure of the helper's first connection what the network way can get wrong that the proxy way cannot.
func (r apiRoute) explain(err error) error {
	if err == nil || r.Via != APIViaNetwork || !strings.Contains(err.Error(), "could not reach the server") {
		return err
	}
	return fmt.Errorf("%w\nthe helper reached for the host's API over the network, at %s: check that the host's firewall lets containers on the bridge reach that port, or install with --api-via proxy", err, r.Remote)
}

// apiProblem is what stands in the way of the network way: Why for the note (auto) and Fix for the error (network).
type apiProblem struct{ Why, Fix string }

// foundIn is the helper in list that is at project/name, or nil.
func foundIn(list []Found, project, name string) *Found {
	for i := range list {
		if list[i].Project == project && list[i].Name == name {
			return &list[i]
		}
	}
	return nil
}

// chooseAPIRoute decides how a helper will reach the host's API. apiAddr is the address a proxy device would connect to (see apiAddress),
// network the NIC's network as networkFor decided it, and current the helper if one is already installed there: an installed helper keeps its
// shape. Nothing is created and nothing is changed here; a failed read is an error in every mode rather than a reason to guess a shape.
func (in *Installer) chooseAPIRoute(opts InstallOptions, apiAddr, network string, current *Found) (apiRoute, error) {
	via, err := ParseAPIVia(string(opts.APIVia))
	if err != nil {
		return apiRoute{}, err
	}
	_, port, _ := net.SplitHostPort(apiAddr)
	proxy := apiRoute{Via: APIViaProxy, Port: port, Connect: apiAddr, Remote: "https://127.0.0.1:" + port}

	if current != nil {
		return in.keepAPIRoute(current, via, proxy, network)
	}
	if via == APIViaProxy {
		return proxy, nil
	}
	route, problem, err := in.networkRoute(network, port)
	if err != nil {
		return apiRoute{}, err
	}
	switch {
	case problem == nil:
		route.Note = fmt.Sprintf("the helper will reach the host's API over the network, at %s (no proxy device)", route.Remote)
		return route, nil
	case via == APIViaNetwork:
		return apiRoute{}, fmt.Errorf("--api-via network: %s. %s", problem.Why, problem.Fix)
	}
	proxy.Note = fmt.Sprintf("note: the helper will reach the host's API through a proxy device, not over the network, because %s (--api-via network says what would change that)", problem.Why)
	return proxy, nil
}

// keepAPIRoute is the route of a helper that is already installed: the one it has. A request for the other shape is refused, because install
// leaves what exists alone and changing the devices of a running helper is not something to do as a side effect.
func (in *Installer) keepAPIRoute(current *Found, via APIVia, proxy apiRoute, network string) (apiRoute, error) {
	have := current.APIRoute()
	if via != APIViaAuto && via != have {
		return apiRoute{}, fmt.Errorf("--api-via %s: the helper %s already exists and reaches the host's API through %s; install does not change an existing helper's devices. "+
			"`tink helper remove` (it keeps the helper's volumes) and then install again makes it the other way", via, current.Label(), describeVia(have))
	}
	remote := current.Config[MarkerKey+".remote"]
	if have == APIViaProxy {
		if remote != "" {
			proxy.Remote = remote
		}
		return proxy, nil
	}
	if remote == "" {
		// a helper that has no proxy device and does not say where it dials: take it from the network as a new one would
		route, problem, err := in.networkRoute(network, proxy.Port)
		if err != nil {
			return apiRoute{}, err
		}
		if problem != nil {
			return apiRoute{}, fmt.Errorf("the helper %s has no proxy device and does not record where it reaches the host's API (%s): %s", current.Label(), MarkerKey+".remote", problem.Why)
		}
		return route, nil
	}
	return apiRoute{Via: APIViaNetwork, Remote: remote, Port: proxy.Port}, nil
}

func describeVia(v APIVia) string {
	if v == APIViaProxy {
		return "a proxy device"
	}
	return "the network (it has no proxy device)"
}

// networkRoute is the network way, or what is in the way of it. port is the port of the API listener.
func (in *Installer) networkRoute(network, port string) (apiRoute, *apiProblem, error) {
	nw, problem, err := in.helperNetwork(network)
	if err != nil || problem != nil {
		return apiRoute{}, problem, err
	}
	gateway, problem := bridgeGateway(nw)
	if problem != nil {
		return apiRoute{}, problem, nil
	}
	srv, _, err := in.Server.GetServer()
	if err != nil {
		return apiRoute{}, nil, fmt.Errorf("reading the server's configuration: %w", err)
	}
	if problem := listenerCovers(srv.Config["core.https_address"], gateway, nw.Name); problem != nil {
		return apiRoute{}, problem, nil
	}
	return apiRoute{Via: APIViaNetwork, Port: port, Remote: "https://" + net.JoinHostPort(gateway.String(), port)}, nil, nil
}

// helperNetwork is the network the helper's NIC joins, as the server describes it: the one install chose, or, when the default profile gives
// instances a NIC, that NIC's network. A problem (as opposed to an error) says the network is not one the helper can use this way.
func (in *Installer) helperNetwork(network string) (*api.Network, *apiProblem, error) {
	name, fromProfile := network, false
	if name == "" {
		fromProfile = true
		prof, _, found, err := incusapi.LookupProfile(in.Server, "default")
		if err != nil {
			return nil, nil, fmt.Errorf("reading the default profile: %w", err)
		}
		if found {
			names := make([]string, 0, len(prof.Devices))
			for dev := range prof.Devices {
				names = append(names, dev)
			}
			sort.Strings(names)
			for _, dev := range names {
				if d := prof.Devices[dev]; d["type"] == "nic" && d["network"] != "" {
					name = d["network"]
					break
				}
			}
		}
		if name == "" {
			return nil, &apiProblem{
				Why: "the NIC the default profile gives instances is not on a network Incus manages (it names no `network:`)",
				Fix: "give --network NAME, a managed bridge that has an IPv4 address (incus network list)",
			}, nil
		}
	}
	nw, _, found, err := lookupNetwork(in.Server, name)
	if err != nil {
		return nil, nil, fmt.Errorf("reading network %s: %w", name, err)
	}
	if !found {
		where := ""
		if fromProfile {
			where = " (the default profile's NIC is on it)"
		}
		return nil, &apiProblem{Why: fmt.Sprintf("network %s%s does not exist", name, where), Fix: "give --network NAME, a managed bridge that has an IPv4 address (incus network list)"}, nil
	}
	if nw.Name == "" {
		nw.Name = name
	}
	return nw, nil, nil
}

// bridgeGateway is the address the host has on a managed bridge, which is the address a container on it finds the host at.
func bridgeGateway(nw *api.Network) (netip.Addr, *apiProblem) {
	fix := "give --network NAME, a managed bridge that has an IPv4 address (incus network list)"
	switch {
	case !nw.Managed:
		return netip.Addr{}, &apiProblem{Why: fmt.Sprintf("network %s is not managed by Incus, so the host's address on it is not known", nw.Name), Fix: fix}
	case nw.Type != "bridge":
		return netip.Addr{}, &apiProblem{Why: fmt.Sprintf("network %s is a %s network, and only a managed bridge gives the host an address the helper can reach", nw.Name, nw.Type), Fix: fix}
	}
	raw := nw.Config["ipv4.address"]
	pfx, err := netip.ParsePrefix(raw)
	if err != nil || !pfx.Addr().Is4() {
		shown := raw
		if shown == "" {
			shown = "not set"
		}
		return netip.Addr{}, &apiProblem{Why: fmt.Sprintf("network %s has no IPv4 address (ipv4.address is %s)", nw.Name, shown), Fix: fix}
	}
	return pfx.Addr(), nil
}

// listenerCovers says whether the API listener (core.https_address) accepts connections made to gateway: a wildcard does, and so does exactly
// that address. Anything else (loopback, another interface's address, an IPv6 address, a name) does not.
func listenerCovers(listen string, gateway netip.Addr, networkName string) *apiProblem {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return &apiProblem{Why: fmt.Sprintf("core.https_address %q is not host:port", listen), Fix: "set it: `incus config set core.https_address :8443`"}
	}
	switch host {
	case "", "0.0.0.0", "::":
		return nil
	}
	if a, err := netip.ParseAddr(host); err == nil && a.Unmap() == gateway {
		return nil
	}
	return &apiProblem{
		Why: fmt.Sprintf("the host's API listens on %s (core.https_address), which does not include %s, the host's address on %s", listen, gateway, networkName),
		Fix: fmt.Sprintf("make it listen there: `incus config set core.https_address %s` listens on that address only, which is the narrower choice; "+
			"`incus config set core.https_address :%s` listens on every address, which also opens the port on every other interface of the host (a firewall decides who reaches it); "+
			"or leave it, and use --api-via proxy", net.JoinHostPort(gateway.String(), port), port),
	}
}
