package helper

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/run"
)

// bridgeHost is a fakeHost that knows what its networks are: a managed bridge has an address, and the default profile's NIC can name one.
type bridgeHost struct {
	*fakeHost
	nets       map[string]*api.Network
	profileNIC map[string]string // the NIC the default profile gives instances; nil: none
}

func (b *bridgeHost) UseProject(string) incus.InstanceServer { return b }

func (b *bridgeHost) GetNetwork(n string) (*api.Network, string, error) {
	nw, ok := b.nets[n]
	if !ok {
		return nil, "", notFound
	}
	c := *nw
	return &c, "", nil
}

func (b *bridgeHost) GetProfile(n string) (*api.Profile, string, error) {
	p := &api.Profile{Name: n, ProfilePut: api.ProfilePut{Devices: map[string]map[string]string{"root": {"type": "disk"}}}}
	if b.profileNIC != nil {
		p.Devices["eth0"] = b.profileNIC
	}
	return p, "", nil
}

// bridge is a managed bridge whose address on the host is ipv4 (CIDR, as Incus keeps it).
func bridge(name, ipv4 string) *api.Network {
	return &api.Network{Name: name, Type: "bridge", Managed: true, NetworkPut: api.NetworkPut{Config: map[string]string{"ipv4.address": ipv4}}}
}

// hostWith is a server whose API listens on listen and which has the given networks.
func hostWith(listen string, nets ...*api.Network) *bridgeHost {
	h := newHost()
	h.serverCfg = map[string]string{"core.https_address": listen}
	b := &bridgeHost{fakeHost: h, nets: map[string]*api.Network{}}
	for _, n := range nets {
		b.nets[n.Name] = n
	}
	return b
}

func bridgeInstaller(b *bridgeHost, specs *[]*run.Spec) (*Installer, *strings.Builder) {
	in, out := installer(b.fakeHost, specs)
	in.Server = b
	return in, out
}

// route runs what Install runs before it creates anything, and returns the decision.
func route(b *bridgeHost, opts InstallOptions) (apiRoute, error) {
	in, _ := bridgeInstaller(b, new([]*run.Spec))
	opts.defaults()
	addr, err := in.apiAddress()
	if err != nil {
		return apiRoute{}, err
	}
	network, err := in.networkFor(opts)
	if err != nil {
		return apiRoute{}, err
	}
	return in.chooseAPIRoute(opts, addr, network, nil)
}

func TestTheWayToTheAPIIsChosenFromTheBridgeAndTheListener(t *testing.T) {
	unmanaged := bridge("br0", "10.10.50.1/24")
	unmanaged.Managed = false
	ovn := bridge("ovn0", "10.10.50.1/24")
	ovn.Type = "ovn"
	noV4 := bridge("v6only", "none")
	unsetV4 := &api.Network{Name: "unset", Type: "bridge", Managed: true}
	v6inV4 := bridge("odd", "fd42:1::1/64") // an IPv6 prefix where an IPv4 address belongs is no gateway the IPv4 rules can use
	tink0 := bridge("tink0", "10.10.50.1/24")

	const (
		viaNetwork = APIViaNetwork
		viaProxy   = APIViaProxy
	)
	cases := []struct {
		name       string
		listen     string
		net        *api.Network // the helper's network, named with --network
		profileNIC map[string]string
		via        APIVia
		want       APIVia
		remote     string
		note       string // a piece of the note
		err        string // a piece of the error (then nothing else is checked)
	}{
		// the listener covers the bridge's address
		{name: "a managed bridge and a wildcard listener", listen: ":8443", net: tink0, want: viaNetwork, remote: "https://10.10.50.1:8443", note: "over the network"},
		{name: "0.0.0.0 is a wildcard", listen: "0.0.0.0:8443", net: tink0, want: viaNetwork, remote: "https://10.10.50.1:8443"},
		{name: "[::] is a wildcard", listen: "[::]:8443", net: tink0, want: viaNetwork, remote: "https://10.10.50.1:8443"},
		{name: "a listener on exactly the bridge's address (what tink deploy sets)", listen: "10.10.50.1:9443", net: tink0, want: viaNetwork, remote: "https://10.10.50.1:9443"},
		{name: "the stated default is auto", listen: ":8443", net: tink0, via: "", want: viaNetwork, remote: "https://10.10.50.1:8443"},
		{name: "auto said out loud", listen: ":8443", net: tink0, via: APIViaAuto, want: viaNetwork, remote: "https://10.10.50.1:8443"},
		{name: "the network is the default profile's NIC", listen: ":8443", net: tink0, profileNIC: map[string]string{"type": "nic", "network": "tink0"}, want: viaNetwork, remote: "https://10.10.50.1:8443"},

		// it does not: a proxy device, and the note says why
		{name: "a listener on another address", listen: "192.168.1.5:8443", net: tink0, want: viaProxy, remote: "https://127.0.0.1:8443", note: "does not include 10.10.50.1"},
		{name: "a loopback listener", listen: "127.0.0.1:8443", net: tink0, want: viaProxy, remote: "https://127.0.0.1:8443", note: "127.0.0.1:8443 (core.https_address)"},
		{name: "an IPv6-only listener", listen: "[fd42:1::1]:8443", net: tink0, want: viaProxy, remote: "https://127.0.0.1:8443", note: "does not include 10.10.50.1"},
		{name: "a NIC on a network Incus does not manage", listen: ":8443", net: unmanaged, want: viaProxy, remote: "https://127.0.0.1:8443", note: "network br0 is not managed by Incus"},
		{name: "a managed network that is not a bridge", listen: ":8443", net: ovn, want: viaProxy, remote: "https://127.0.0.1:8443", note: "ovn network"},
		{name: "a bridge without an IPv4 address", listen: ":8443", net: noV4, want: viaProxy, remote: "https://127.0.0.1:8443", note: "no IPv4 address (ipv4.address is none)"},
		{name: "a bridge that does not say its IPv4 address", listen: ":8443", net: unsetV4, want: viaProxy, remote: "https://127.0.0.1:8443", note: "ipv4.address is not set"},
		{name: "a bridge whose ipv4.address is an IPv6 prefix", listen: ":8443", net: v6inV4, want: viaProxy, remote: "https://127.0.0.1:8443", note: "no IPv4 address"},
		{name: "the default profile's NIC names no network", listen: ":8443", profileNIC: map[string]string{"type": "nic", "nictype": "bridged", "parent": "br0"}, want: viaProxy, remote: "https://127.0.0.1:8443", note: "names no `network:`"},
		{name: "the default profile's NIC names a network that is gone", listen: ":8443", profileNIC: map[string]string{"type": "nic", "network": "gone"}, want: viaProxy, remote: "https://127.0.0.1:8443", note: "network gone"},

		// asked for outright
		{name: "forced proxy, though the network would do", listen: ":8443", net: tink0, via: viaProxy, want: viaProxy, remote: "https://127.0.0.1:8443"},
		{name: "forced network, and it can", listen: ":8443", net: tink0, via: viaNetwork, want: viaNetwork, remote: "https://10.10.50.1:8443"},
		{name: "forced network, listener elsewhere", listen: "127.0.0.1:8443", net: tink0, via: viaNetwork,
			err: "--api-via network: the host's API listens on 127.0.0.1:8443 (core.https_address), which does not include 10.10.50.1"},
		{name: "forced network, and the error says what to set", listen: "192.168.1.5:8443", net: tink0, via: viaNetwork,
			err: "incus config set core.https_address :8443"},
		{name: "forced network, and the error says what the wildcard costs", listen: "192.168.1.5:8443", net: tink0, via: viaNetwork,
			err: "every other interface"},
		{name: "forced network, and the error names the alternative", listen: "192.168.1.5:8443", net: tink0, via: viaNetwork, err: "--api-via proxy"},
		{name: "forced network, unmanaged network", listen: ":8443", net: unmanaged, via: viaNetwork, err: "is not managed by Incus"},
		{name: "forced network, no IPv4 address", listen: ":8443", net: noV4, via: viaNetwork, err: "no IPv4 address"},
		{name: "forced network, the profile's NIC names no network", listen: ":8443", profileNIC: map[string]string{"type": "nic"}, via: viaNetwork, err: "give --network NAME"},
		{name: "a value that is not a way", listen: ":8443", net: tink0, via: "tunnel", err: "expected auto, network or proxy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var nets []*api.Network
			opts := InstallOptions{APIVia: tc.via}
			if tc.net != nil {
				nets = append(nets, tc.net)
				opts.Network = tc.net.Name
			}
			if tc.profileNIC != nil {
				nets = append(nets, tink0)
			}
			b := hostWith(tc.listen, nets...)
			b.profileNIC = tc.profileNIC
			got, err := route(b, opts)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want it to contain %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Via != tc.want || got.Remote != tc.remote {
				t.Errorf("route = %s at %s, want %s at %s (note %q)", got.Via, got.Remote, tc.want, tc.remote, got.Note)
			}
			if !strings.Contains(got.Note, tc.note) {
				t.Errorf("note = %q, want it to contain %q", got.Note, tc.note)
			}
			if tc.note == "" && tc.want == viaProxy && got.Note != "" {
				t.Errorf("a proxy device that was asked for needs no excuse: %q", got.Note)
			}
			if tc.want == viaProxy && tc.note != "" && !strings.HasPrefix(got.Note, "note: ") {
				t.Errorf("falling back to a proxy device is a note: %q", got.Note)
			}
			if (got.proxyDevice() != nil) != (tc.want == viaProxy) {
				t.Errorf("a proxy device exactly when the way is the proxy: %v", got.proxyDevice())
			}
		})
	}
}

func TestListenerCoversTheGatewayOnlyWhenItAcceptsConnectionsThere(t *testing.T) {
	gw := netip.MustParseAddr("10.10.50.1")
	for listen, covers := range map[string]bool{
		":8443": true, "0.0.0.0:8443": true, "[::]:8443": true, "10.10.50.1:8443": true, "[::ffff:10.10.50.1]:8443": true,
		"127.0.0.1:8443": false, "10.10.50.2:8443": false, "[::1]:8443": false, "[fd42::1]:8443": false, "incus.lan:8443": false,
		"nonsense": false, "": false,
	} {
		if got := listenerCovers(listen, gw, "tink0") == nil; got != covers {
			t.Errorf("listenerCovers(%q) = %v, want %v", listen, got, covers)
		}
	}
}

func TestParseAPIVia(t *testing.T) {
	for in, want := range map[string]APIVia{"": APIViaAuto, "auto": APIViaAuto, " Network ": APIViaNetwork, "PROXY": APIViaProxy} {
		if got, err := ParseAPIVia(in); err != nil || got != want {
			t.Errorf("ParseAPIVia(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseAPIVia("socket"); err == nil || !strings.Contains(err.Error(), "auto, network or proxy") {
		t.Errorf("err = %v", err)
	}
}

// The instance itself: in network mode it has no proxy device, and the remote it is enrolled at is the bridge's address.
func TestNetworkModeGivesTheHelperNoProxyDeviceAndEnrolsItAtTheBridgesAddress(t *testing.T) {
	b := hostWith(":8443", bridge("tink0", "10.10.50.1/24"))
	var specs []*run.Spec
	in, out := bridgeInstaller(b, &specs)
	if err := in.Install(InstallOptions{Image: "x", Network: "tink0", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	s := specs[0]
	if dev, has := s.Devices["api"]; has {
		t.Errorf("no proxy device in network mode: %v", dev)
	}
	for name, dev := range s.Devices {
		if dev["type"] == "proxy" {
			t.Errorf("device %s is a proxy device", name)
		}
	}
	if s.Devices["eth0"]["network"] != "tink0" {
		t.Errorf("the NIC is the way to the API: %v", s.Devices)
	}
	if s.Devices["root"]["type"] != "disk" || s.Devices["config"]["source"] != configVolume || s.Devices["data"]["source"] != dataVolume {
		t.Errorf("the rest of the devices are as they were: %v", s.Devices)
	}
	if got := s.Config[MarkerKey+".remote"]; got != "https://10.10.50.1:8443" {
		t.Errorf("the instance records where it reaches the API: %q", got)
	}
	if len(b.execs) != 1 {
		t.Fatalf("one enrolment: %v", b.execs)
	}
	e := b.execs[0]
	if strings.Join(e.command, " ") != "/usr/local/bin/tink remote add host https://10.10.50.1:8443 --token-file -" {
		t.Errorf("only the address differs from the proxy path's enrolment: %v", e.command)
	}
	if e.stdin == "" || strings.Contains(strings.Join(e.command, " "), e.stdin) {
		t.Errorf("the token is still on standard input, never on the command line: %q", e.stdin)
	}
	if !strings.Contains(out.String(), "over the network, at https://10.10.50.1:8443 (no proxy device)") {
		t.Errorf("it says which way it chose:\n%s", out.String())
	}
	if len(b.certs) != 1 || b.certs[0].Name != TrustName {
		t.Errorf("enrolled: %+v", b.certs)
	}
}

func TestTheDefaultIsTheNetworkWhenTheDefaultProfilesNICIsOnAManagedBridge(t *testing.T) {
	b := hostWith("10.10.50.1:8443", bridge("tink0", "10.10.50.1/24"))
	b.profileNIC = map[string]string{"type": "nic", "network": "tink0"}
	var specs []*run.Spec
	in, _ := bridgeInstaller(b, &specs)
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if _, has := specs[0].Devices["api"]; has {
		t.Errorf("no proxy device: %v", specs[0].Devices)
	}
	if _, has := specs[0].Devices["eth0"]; has {
		t.Errorf("and no second NIC: the profile gives one: %v", specs[0].Devices)
	}
	if got := b.execs[0].command; got[len(got)-3] != "https://10.10.50.1:8443" {
		t.Errorf("%v", got)
	}
}

func TestProxyModeKeepsTheProxyDeviceAndTheLoopbackAddress(t *testing.T) {
	cases := map[string]struct {
		listen  string
		via     APIVia
		port    string // the port of the listener, which the proxy listens on inside the container too
		connect string // where on the host the proxy connects
		note    string
	}{
		"asked for": {":8443", APIViaProxy, "8443", "127.0.0.1:8443", ""},
		"asked for, with a listener on the gateway": {"10.10.50.1:8443", APIViaProxy, "8443", "10.10.50.1:8443", ""},
		"auto, and the listener is loopback":        {"127.0.0.1:8443", APIViaAuto, "8443", "127.0.0.1:8443", "does not include 10.10.50.1"},
		"auto, and the listener is elsewhere":       {"192.168.1.5:9443", "", "9443", "192.168.1.5:9443", "does not include 10.10.50.1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			b := hostWith(tc.listen, bridge("tink0", "10.10.50.1/24"))
			var specs []*run.Spec
			in, out := bridgeInstaller(b, &specs)
			if err := in.Install(InstallOptions{Image: "x", Network: "tink0", APIVia: tc.via, Wait: -1}); err != nil {
				t.Fatal(err)
			}
			dev := specs[0].Devices["api"]
			if dev["type"] != "proxy" || dev["bind"] != "container" || dev["listen"] != "tcp:127.0.0.1:"+tc.port || dev["connect"] != "tcp:"+tc.connect {
				t.Errorf("a proxy device on the container's loopback: %v", dev)
			}
			remote := "https://127.0.0.1:" + tc.port
			if got := specs[0].Config[MarkerKey+".remote"]; got != remote {
				t.Errorf("recorded remote = %q, want %q", got, remote)
			}
			if got := strings.Join(b.execs[0].command, " "); got != "/usr/local/bin/tink remote add host "+remote+" --token-file -" {
				t.Errorf("enrolment: %s", got)
			}
			switch {
			case tc.note == "" && strings.Contains(out.String(), "note:"):
				t.Errorf("no excuse needed:\n%s", out.String())
			case tc.note != "" && !strings.Contains(out.String(), "note: the helper will reach the host's API through a proxy device, not over the network, because "):
				t.Errorf("the fallback says so, in one line:\n%s", out.String())
			case tc.note != "" && !strings.Contains(out.String(), tc.note):
				t.Errorf("and says why:\n%s", out.String())
			}
		})
	}
}

func TestANetworkInstallThatCannotBeDoneSaysSoBeforeCreatingAnything(t *testing.T) {
	b := hostWith("127.0.0.1:8443", bridge("tink0", "10.10.50.1/24"))
	var specs []*run.Spec
	in, _ := bridgeInstaller(b, &specs)
	err := in.Install(InstallOptions{Image: "x", Network: "tink0", APIVia: APIViaNetwork, Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "core.https_address") || !strings.Contains(err.Error(), "--api-via proxy") {
		t.Fatalf("err = %v", err)
	}
	if len(b.projects) != 0 || len(specs) != 0 || len(b.log) != 0 || b.tokens != 0 {
		t.Errorf("nothing is created or minted: %v %v %v", b.projects, specs, b.log)
	}
}

// What exists is left alone: a helper keeps the way it has, and install says how to change it instead of doing it.
func TestInstallingAgainFollowsTheHelperThatExistsNotTheServerAsItIsNow(t *testing.T) {
	b := hostWith(":8443", bridge("tink0", "10.10.50.1/24"))
	in, _ := bridgeInstaller(b, new([]*run.Spec))
	if err := in.Install(InstallOptions{Image: "x", Network: "tink0", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	// the host's listener has moved since; a re-enrolment must still go to where the helper reaches the API
	b.serverCfg["core.https_address"] = "127.0.0.1:8443"
	if err := in.Install(InstallOptions{Image: "x", Network: "tink0", Reissue: true, Wait: -1}); err != nil {
		t.Fatalf("an existing network helper is not re-judged against the listener: %v", err)
	}
	got := commandsRun(b.fakeHost)
	if last := got[len(got)-1]; last != "remote add host https://10.10.50.1:8443 --token-file -" {
		t.Errorf("enrolled again at the address it was installed with: %v", got)
	}
	if _, has := b.instances["helper"].Devices["api"]; has {
		t.Errorf("a proxy device is not added to an existing helper: %v", b.instances["helper"].Devices)
	}

	// and the other way is refused with the way to get it
	err := in.Install(InstallOptions{Image: "x", Network: "tink0", APIVia: APIViaProxy, Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "tink helper remove") || !strings.Contains(err.Error(), "the network") {
		t.Errorf("err = %v", err)
	}
}

func TestAnExistingProxyHelperStaysAProxyHelperWhateverTheNetworkWouldAllow(t *testing.T) {
	b := hostWith(":8443", bridge("tink0", "10.10.50.1/24"))
	in, _ := bridgeInstaller(b, new([]*run.Spec))
	if err := in.Install(InstallOptions{Image: "x", Network: "tink0", APIVia: APIViaProxy, Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if b.instances["helper"].Devices["api"]["type"] != "proxy" {
		t.Fatalf("%v", b.instances["helper"].Devices)
	}
	if err := in.Install(InstallOptions{Image: "x", Network: "tink0", Reissue: true, Wait: -1}); err != nil { // auto
		t.Fatal(err)
	}
	got := commandsRun(b.fakeHost)
	if last := got[len(got)-1]; last != "remote add host https://127.0.0.1:8443 --token-file -" {
		t.Errorf("auto does not turn a proxy helper into a network one: %v", got)
	}
	err := in.Install(InstallOptions{Image: "x", Network: "tink0", APIVia: APIViaNetwork, Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "already exists") || !strings.Contains(err.Error(), "a proxy device") {
		t.Errorf("err = %v", err)
	}
}

// upgrade recreates the instance with the devices it had, so whichever shape exists is the shape that stays.
func TestAnImageUpgradeKeepsAHelperThatReachesTheAPIOverTheNetworkWithoutAProxyDevice(t *testing.T) {
	h := newHost()
	upgradeFixture(h)
	inst := h.instances["helper"]
	delete(inst.Devices, "api")
	inst.Devices["eth0"] = map[string]string{"type": "nic", "network": "tink0", "name": "eth0"}
	inst.Config[MarkerKey+".remote"] = "https://10.10.50.1:8443"
	if got := (Found{Devices: inst.Devices}).APIRoute(); got != APIViaNetwork {
		t.Fatalf("the fixture is a network helper: %v", got)
	}

	var specs []*run.Spec
	in, out := installer(h, &specs)
	base := in.Create
	in.Create = func(s incus.InstanceServer, spec *run.Spec, project string) error {
		if err := base(s, spec, project); err != nil {
			return err
		}
		h.instances[spec.Name].Config = map[string]string{"volatile.base_image": "sha256:new"}
		return nil
	}
	clocked(in, func(n int, now time.Time) {
		if n == 1 {
			publish(h, now, func(s *Status) { s.Draining = true })
		}
		if indexOf(h.log, "start helper") > 0 {
			publish(h, now, nil)
		}
	})
	if err := in.Upgrade(UpgradeOptions{Image: "ghcr:minihci/tink-helper:v1.1.0", Wait: time.Minute}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := h.instances["helper"]
	if _, has := got.Devices["api"]; has {
		t.Errorf("an upgrade must not give the helper a proxy device: %v", got.Devices)
	}
	if got.Devices["eth0"]["network"] != "tink0" || got.Devices["data"]["source"] != dataVolume {
		t.Errorf("the same devices: %v", got.Devices)
	}
	if got.Config[MarkerKey+".remote"] != "https://10.10.50.1:8443" {
		t.Errorf("and the same address: %v", got.Config)
	}
	if h.tokens != 0 || len(h.execs) != 0 {
		t.Error("and it is not enrolled again")
	}
}

func TestAProxyHelperIsRecognisedFromItsDevices(t *testing.T) {
	h := newHost()
	upgradeFixture(h)
	found, err := Find(h)
	if err != nil || len(found) != 1 {
		t.Fatalf("%v %v", found, err)
	}
	if found[0].APIRoute() != APIViaProxy {
		t.Errorf("Find carries the instance's devices, and one is the proxy: %v", found[0].Devices)
	}
	delete(h.instances["helper"].Devices, "api")
	found, _ = Find(h)
	if found[0].APIRoute() != APIViaNetwork {
		t.Errorf("without the device the helper reaches the API over the network: %v", found[0].Devices)
	}
}

func TestAFirstConnectionThatFailsOverTheNetworkSaysWhatToCheck(t *testing.T) {
	overNet := apiRoute{Via: APIViaNetwork, Remote: "https://10.10.50.1:8443"}
	proxy := apiRoute{Via: APIViaProxy, Remote: "https://127.0.0.1:8443"}
	unreached := errors.New("enrolling the helper failed (exit 1): Error: could not reach the server: dial tcp 10.10.50.1:8443: i/o timeout")
	if err := overNet.explain(unreached); !strings.Contains(err.Error(), "firewall") || !strings.Contains(err.Error(), "--api-via proxy") || !errors.Is(err, unreached) {
		t.Errorf("%v", err)
	}
	if err := proxy.explain(unreached); err != unreached {
		t.Errorf("the proxy way has no firewall to blame: %v", err)
	}
	other := errors.New("enrolling the helper failed (exit 1): Error: the token was already used")
	if err := overNet.explain(other); err != other {
		t.Errorf("only a failure to reach the server is explained: %v", err)
	}
	if overNet.explain(nil) != nil {
		t.Error("nil stays nil")
	}
}

func TestEnrolmentThatCannotReachTheBridgeAddressFailsWithTheHint(t *testing.T) {
	b := hostWith(":8443", bridge("tink0", "10.10.50.1/24"))
	b.execCode, b.execOut = 1, "Error: could not reach the server: dial tcp 10.10.50.1:8443: connect: connection refused\n"
	in, _ := bridgeInstaller(b, new([]*run.Spec))
	err := in.Install(InstallOptions{Image: "x", Network: "tink0", Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "could not reach the server") || !strings.Contains(err.Error(), "--api-via proxy") {
		t.Errorf("err = %v", err)
	}
	if !contains(b.log, "cancel token") {
		t.Errorf("and the token is not left live: %v", b.log)
	}
}

// Found live: for a loopback listener the refusal only offered the wildcard, though the bridge's own address is what the docs say works and
// is the narrower choice (it opens nothing on the host's other interfaces).
func TestAListenerThatDoesNotCoverTheBridgeIsToldTheNarrowFixAsWellAsTheWildcard(t *testing.T) {
	p := listenerCovers("127.0.0.1:8444", netip.MustParseAddr("10.34.253.1"), "incusbr0")
	if p == nil {
		t.Fatal("a loopback listener does not cover the bridge")
	}
	for _, want := range []string{
		"core.https_address 10.34.253.1:8444` listens on that address only",
		"core.https_address :8444` listens on every address",
		"--api-via proxy",
	} {
		if !strings.Contains(p.Fix, want) {
			t.Errorf("the fix should say %q: %s", want, p.Fix)
		}
	}
}
