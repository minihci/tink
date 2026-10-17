package resolve

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

func proxyInstance(name string, devices map[string]map[string]string, profiles ...string) Resource {
	return Resource{Kind: KindInstance, Name: name, Image: "images:alpine/3.22", Devices: devices, Profiles: profiles}
}

func natProxy(listen, connect string) map[string]string {
	return map[string]string{"type": "proxy", "nat": "true", "listen": listen, "connect": connect}
}

func nicWith(kv ...string) map[string]string {
	d := map[string]string{"type": "nic", "network": "incusbr0", "name": "eth0"}
	for i := 0; i+1 < len(kv); i += 2 {
		d[kv[i]] = kv[i+1]
	}
	return d
}

func TestParseProxyEndpoint(t *testing.T) {
	good := []struct {
		in    string
		proto string
		addr  string
		ports []portRange
	}{
		{"tcp:10.0.0.1:443", "tcp", "10.0.0.1", []portRange{{443, 443}}},
		{"udp:0.0.0.0:80-90", "udp", "0.0.0.0", []portRange{{80, 90}}},
		{"tcp:[::]:8080", "tcp", "::", []portRange{{8080, 8080}}},
		{"tcp:[fd42::1]:443", "tcp", "fd42::1", []portRange{{443, 443}}},
		{"tcp:1.2.3.4:80,443,8000-8010", "tcp", "1.2.3.4", []portRange{{80, 80}, {443, 443}, {8000, 8010}}},
		{"  tcp:127.0.0.1:3001  ", "tcp", "127.0.0.1", []portRange{{3001, 3001}}},
	}
	for _, c := range good {
		ep, ok := parseProxyEndpoint(c.in)
		if !ok || ep.proto != c.proto || ep.addr != c.addr || !reflect.DeepEqual(ep.ports, c.ports) {
			t.Errorf("parseProxyEndpoint(%q) = %+v, %v; want %s %s %v", c.in, ep, ok, c.proto, c.addr, c.ports)
		}
	}
	for _, in := range []string{"", "unix:/run/x.sock", "tcp:1.2.3.4", "tcp:1.2.3.4:abc", "tcp:1.2.3.4:90-80", "tcp:1.2.3.4:70000", "tcp:1.2.3.4:0", "tcp:[::1:80", "tcp:1.2.3.4:80,"} {
		if ep, ok := parseProxyEndpoint(in); ok {
			t.Errorf("parseProxyEndpoint(%q) = %+v, want it not to parse", in, ep)
		}
	}
}

func TestEndpointOverlaps(t *testing.T) {
	ep := func(s string) proxyEndpoint {
		e, ok := parseProxyEndpoint(s)
		if !ok {
			t.Fatalf("bad test endpoint %q", s)
		}
		return e
	}
	cases := []struct {
		a, b string
		want bool
	}{
		{"tcp:10.0.0.1:443", "tcp:10.0.0.1:443", true},
		{"tcp:10.0.0.1:443", "tcp:10.0.0.1:444", false},
		{"tcp:10.0.0.1:443", "tcp:10.0.0.2:443", false},
		{"tcp:10.0.0.1:443", "udp:10.0.0.1:443", false},
		{"tcp:10.0.0.1:8000-8010", "tcp:10.0.0.1:8005", true},
		{"tcp:10.0.0.1:8000-8010", "tcp:10.0.0.1:8011-8020", false},
		{"tcp:10.0.0.1:80,443", "tcp:10.0.0.1:443", true},
		{"tcp:0.0.0.0:443", "tcp:10.0.0.1:443", true},
		{"tcp:10.0.0.1:443", "tcp:0.0.0.0:443", true},
		{"tcp:0.0.0.0:443", "tcp:[fd42::1]:443", false},
		{"tcp:[::]:443", "tcp:[fd42::1]:443", true},
		{"tcp:[::]:443", "tcp:10.0.0.1:443", false},
		{"tcp:0.0.0.0:443", "tcp:0.0.0.0:443", true},
		{"tcp:0.0.0.0:443", "tcp:[::]:443", false},
		{"tcp:[fd42::1]:443", "tcp:[fd42:0:0:0:0:0:0:1]:443", true},
	}
	for _, c := range cases {
		if got := ep(c.a).overlaps(ep(c.b)); got != c.want {
			t.Errorf("overlaps(%s, %s) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := ep(c.b).overlaps(ep(c.a)); got != c.want {
			t.Errorf("overlaps(%s, %s) = %v, want %v (symmetry)", c.b, c.a, got, c.want)
		}
	}
}

func TestNATProxyWarnings(t *testing.T) {
	profiles := map[string]Resource{
		"lan": {Kind: KindProfile, Name: "lan", Devices: map[string]map[string]string{"eth0": nicWith("ipv4.address", "10.34.253.50")}},
	}
	cases := []struct {
		name  string
		r     Resource
		wants []string // substrings, one per expected warning; nil = none
	}{
		{"connects to the NIC's static address", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv4.address", "10.34.253.201"), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.201:443")}), nil},
		{"a CIDR on the NIC counts, its prefix ignored", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv4.address", "10.34.253.201/24"), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.201:443")}), nil},
		{"an IPv6 static address", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv6.address", "fd42::201"), "https": natProxy("tcp:[2001:db8::1]:443", "tcp:[fd42::201]:443")}), nil},
		{"the NIC has no static address (a DHCP lease is not one)", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.77:443")}),
			[]string{`proxy device "https" has nat on and connects to 10.34.253.77`, "none of its NICs declares a static address"}},
		{"connects to an address the NIC does not have", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv4.address", "10.34.253.201"), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.202:443")}),
			[]string{"which is not a static address of this instance", "the static addresses its NICs declare are 10.34.253.201"}},
		{"ipv4.address none is not an address", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv4.address", "none"), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.77:443")}),
			[]string{"none of its NICs declares a static address"}},
		{"a connect name that is not an IP address", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv4.address", "10.34.253.201"), "https": natProxy("tcp:170.205.38.254:443", "tcp:localhost:443")}),
			[]string{`connects to "localhost", which is not an IP address`}},
		{"the default userspace proxy (no nat) needs no static address", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "https": {"type": "proxy", "listen": "tcp:170.205.38.254:443", "connect": "tcp:127.0.0.1:443"}}), nil},
		{"nat written false", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "https": {"type": "proxy", "nat": "false", "listen": "tcp:170.205.38.254:443", "connect": "tcp:127.0.0.1:443"}}), nil},
		{"a proxy that listens inside the instance is not a host NAT", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "api": {"type": "proxy", "nat": "true", "bind": "container", "listen": "tcp:127.0.0.1:8443", "connect": "tcp:10.0.0.1:8443"}}), nil},
		{"the static address comes from a declared profile", proxyInstance("ing", map[string]map[string]string{
			"https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.50:443")}, "lan"), nil},
		{"a profile the stack does not declare is named in the warning", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.77:443")}, "default", "site"),
			[]string{"profiles default, site are not declared in this stack"}},
		{"one good and one bad device give one warning, naming the bad one", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith("ipv4.address", "10.34.253.201"),
			"http": natProxy("tcp:170.205.38.254:80", "tcp:10.34.253.201:80"), "https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.9:443")}),
			[]string{`proxy device "https"`}},
		{"a device that is not a proxy is ignored", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "data": {"type": "disk", "nat": "true", "connect": "tcp:localhost:1"}}), nil},
		{"an unparseable connect is left to Incus", proxyInstance("ing", map[string]map[string]string{
			"eth0": nicWith(), "https": natProxy("tcp:170.205.38.254:443", "unix:/run/x.sock")}), nil},
		{"not an instance", Resource{Kind: KindProfile, Name: "p", Devices: map[string]map[string]string{"https": natProxy("tcp:1.2.3.4:443", "tcp:localhost:443")}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := natProxyWarnings(c.r, profiles)
			if (len(c.wants) == 0) != (len(got) == 0) {
				t.Fatalf("got %d warnings %q, wants %v", len(got), got, c.wants)
			}
			if len(c.wants) == 0 {
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d warnings %q, want exactly one", len(got), got)
			}
			for _, want := range c.wants {
				if !strings.Contains(got[0], want) {
					t.Errorf("warning %q does not contain %q", got[0], want)
				}
			}
			if !strings.Contains(got[0], "docs/public-exposure.md") {
				t.Errorf("warning %q should point at docs/public-exposure.md", got[0])
			}
		})
	}
}

func listenOn(listen string) map[string]string {
	return map[string]string{"type": "proxy", "nat": "true", "listen": listen, "connect": "tcp:10.0.0.9:1"}
}

func TestProxyListenerNotes(t *testing.T) {
	a := proxyInstance("a", map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:443")})
	b := proxyInstance("b", map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:443")})

	t.Run("two instances on the same address and port are each told about the other", func(t *testing.T) {
		n := proxyListenerNotes([]Resource{a, b}, nil)
		if len(n["a"]) != 1 || len(n["b"]) != 1 {
			t.Fatalf("notes = %q", n)
		}
		if !strings.Contains(n["a"][0], "instance/b's device \"web\"") || !strings.Contains(n["b"][0], "instance/a's device \"web\"") {
			t.Errorf("each note should name the other instance: %q", n)
		}
		if !strings.Contains(n["a"][0], "without complaint") || !strings.Contains(n["a"][0], "this stack only") {
			t.Errorf("the note should say what Incus does and what the check covers: %q", n["a"][0])
		}
	})
	t.Run("different ports", func(t *testing.T) {
		c := proxyInstance("c", map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:8443")})
		if n := proxyListenerNotes([]Resource{a, c}, nil); n != nil {
			t.Errorf("notes = %q, want none", n)
		}
	})
	t.Run("a port range that contains the other's port", func(t *testing.T) {
		c := proxyInstance("c", map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:400-500")})
		if n := proxyListenerNotes([]Resource{a, c}, nil); len(n["a"]) != 1 || len(n["c"]) != 1 {
			t.Errorf("notes = %q, want one each", n)
		}
	})
	t.Run("a wildcard overlaps a specific address of its family only", func(t *testing.T) {
		w4 := proxyInstance("w4", map[string]map[string]string{"web": listenOn("tcp:0.0.0.0:443")})
		w6 := proxyInstance("w6", map[string]map[string]string{"web": listenOn("tcp:[::]:443")})
		if n := proxyListenerNotes([]Resource{a, w4}, nil); len(n) != 2 {
			t.Errorf("wildcard v4 against specific v4: notes = %q, want both instances", n)
		}
		if n := proxyListenerNotes([]Resource{a, w6}, nil); n != nil {
			t.Errorf("wildcard v6 against specific v4: notes = %q, want none", n)
		}
	})
	t.Run("udp and tcp do not clash", func(t *testing.T) {
		u := proxyInstance("u", map[string]map[string]string{"web": listenOn("udp:170.205.38.254:443")})
		if n := proxyListenerNotes([]Resource{a, u}, nil); n != nil {
			t.Errorf("notes = %q, want none", n)
		}
	})
	t.Run("two devices of one instance are reported to it once", func(t *testing.T) {
		d := proxyInstance("d", map[string]map[string]string{"one": listenOn("tcp:170.205.38.254:443"), "two": listenOn("tcp:170.205.38.254:443")})
		n := proxyListenerNotes([]Resource{d}, nil)
		if len(n["d"]) != 1 || !strings.Contains(n["d"][0], `"one"`) || !strings.Contains(n["d"][0], `"two"`) {
			t.Errorf("notes = %q, want one note naming both devices", n)
		}
	})
	t.Run("a listener bound inside the instance is not the host's", func(t *testing.T) {
		in := proxyInstance("in", map[string]map[string]string{"web": {"type": "proxy", "bind": "container", "listen": "tcp:170.205.38.254:443", "connect": "tcp:127.0.0.1:1"}})
		if n := proxyListenerNotes([]Resource{a, in}, nil); n != nil {
			t.Errorf("notes = %q, want none", n)
		}
	})
	t.Run("three on one port: each is told about the other two", func(t *testing.T) {
		c := proxyInstance("c", map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:443")})
		n := proxyListenerNotes([]Resource{a, b, c}, nil)
		if len(n["a"]) != 2 || len(n["b"]) != 2 || len(n["c"]) != 2 {
			t.Errorf("notes = %q, want two each", n)
		}
	})
	t.Run("projects do not matter, a listener is host-global", func(t *testing.T) {
		pa, pb := a, b
		pa.Project, pb.Project = "one", "two"
		if n := proxyListenerNotes([]Resource{pa, pb}, nil); len(n) != 2 {
			t.Errorf("notes = %q, want both", n)
		}
	})
	t.Run("a listener that comes from a declared profile counts", func(t *testing.T) {
		profiles := map[string]Resource{"edge": {Kind: KindProfile, Name: "edge", Devices: map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:443")}}}
		p := proxyInstance("p", nil, "edge")
		if n := proxyListenerNotes([]Resource{a, p}, profiles); len(n) != 2 {
			t.Errorf("notes = %q, want both", n)
		}
	})
	t.Run("non-instances and unparseable listens are ignored", func(t *testing.T) {
		v := Resource{Kind: KindStorageVolume, Name: "v", Devices: map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:443")}}
		u := proxyInstance("u", map[string]map[string]string{"web": {"type": "proxy", "listen": "unix:/run/x.sock", "connect": "tcp:127.0.0.1:1"}})
		if n := proxyListenerNotes([]Resource{a, v, u}, nil); n != nil {
			t.Errorf("notes = %q, want none", n)
		}
	})
	t.Run("the order of the notes does not depend on the order of the resources", func(t *testing.T) {
		c := proxyInstance("c", map[string]map[string]string{"web": listenOn("tcp:170.205.38.254:443")})
		if x, y := proxyListenerNotes([]Resource{a, b, c}, nil), proxyListenerNotes([]Resource{c, b, a}, nil); !reflect.DeepEqual(x, y) {
			t.Errorf("notes differ with the order of the resources:\n%q\n%q", x, y)
		}
	})
}

func TestPlanOne_NATProxyWithoutAStaticAddressWarnsBeforeTheInstanceExists(t *testing.T) {
	r := proxyInstance("ingress", map[string]map[string]string{
		"eth0":  nicWith(),
		"https": natProxy("tcp:170.205.38.254:443", "tcp:10.34.253.77:443"),
	})
	opts := PlanOptions{env: &imageEnv{offline: true, conf: &cliconfig.Config{Remotes: ociTestRemotes}}}.ForResources([]Resource{r})
	p, err := planOne(&lookupServer{err: errNotFound}, r, opts)
	if err != nil {
		t.Fatalf("planOne() error = %v", err)
	}
	if p.Action != ActionCreate {
		t.Fatalf("action = %v, want ActionCreate: a warning must not stop the plan", p.Action)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "none of its NICs declares a static address") {
		t.Errorf("warnings = %q, want the static-address warning", p.Warnings)
	}
}

func TestPlanOptions_ListenerNotesComeFromTheFullStack(t *testing.T) {
	a := proxyInstance("a", map[string]map[string]string{"eth0": nicWith("ipv4.address", "10.0.0.9"), "web": natProxy("tcp:170.205.38.254:443", "tcp:10.0.0.9:443")})
	b := proxyInstance("b", map[string]map[string]string{"eth0": nicWith("ipv4.address", "10.0.0.10"), "web": natProxy("tcp:170.205.38.254:443", "tcp:10.0.0.10:443")})
	stack := []Resource{a, b}

	env := &imageEnv{offline: true, conf: &cliconfig.Config{Remotes: ociTestRemotes}}
	for name, opts := range map[string]PlanOptions{
		"ForResources": PlanOptions{env: env}.ForResources(stack),
		"withTargets":  PlanOptions{env: env}.withTargets(stack),
	} {
		t.Run(name, func(t *testing.T) {
			// planned on its own, as a one-level plan is, the second instance still hears about the first
			p, err := planOne(&lookupServer{err: errNotFound}, b, opts)
			if err != nil {
				t.Fatalf("planOne() error = %v", err)
			}
			if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "instance/a's device") {
				t.Errorf("warnings = %q, want the note about instance a", p.Warnings)
			}
		})
	}
}
