package join

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

// The public-only site: a VPS-shaped host whose names live in public DNS. It has no dns helper, no mesh helper and no LAN leg; the ingress helper states the zone
// and has a `public` leg that the host's NAT proxy devices connect to. testdata/public/golden is what a person would write by hand for it.

func loadPublic(t *testing.T) []Stack {
	t.Helper()
	var out []Stack
	for _, f := range []string{"platform.yaml", "apps.yaml"} {
		rs, err := resolve.LoadFiles([]string{filepath.Join("testdata", "public", f)})
		if err != nil {
			t.Fatal(err)
		}
		name, _ := resolve.StackName(rs)
		out = append(out, Stack{Name: name, Resources: rs})
	}
	return out
}

func publicGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "public", "golden", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAPublicOnlySiteJoinsToTheHandWrittenPieces(t *testing.T) {
	res := Build(loadPublic(t))
	if res.Errors() != 0 {
		t.Fatalf("errors: %s", messages(res, Error))
	}
	if res.Zone != "apps.example.org" || res.PublicAddress != "10.20.50.3" || res.HasMesh || res.LANAddress != "" || res.MeshAddress != "" || res.DNSInstance != "" {
		t.Errorf("facts = zone %q public %q lan %q mesh %q hasMesh %v dns %q", res.Zone, res.PublicAddress, res.LANAddress, res.MeshAddress, res.HasMesh, res.DNSInstance)
	}
	if len(res.Sources) != 1 || res.Sources[0] != "10.20.50.3" {
		t.Errorf("sources = %v", res.Sources)
	}
	for name, got := range map[string]string{"caddy": res.Caddy, "sso": res.SSO, "acl": res.ACL} {
		if want := publicGolden(t, name); got != want {
			t.Errorf("%s rendering differs from public/golden/%s.txt\n--- got\n%s--- want\n%s", name, name, got, want)
		}
	}
	if res.DNS != "" || res.Mesh != "" {
		t.Errorf("dns %q and mesh %q must be empty: there is no helper to write them for", res.DNS, res.Mesh)
	}
	var names []string
	for _, s := range res.Sections() {
		names = append(names, s.Name)
	}
	if got := strings.Join(names, ","); got != "caddy,sso,acl" {
		t.Errorf("sections = %s", got)
	}
}

// With no dns helper the join says, as a warning, what the person has to do outside tink, with the names to do it for.
func TestAPublicOnlySiteSaysWhichNamesBelongInPublicDNS(t *testing.T) {
	res := Build(loadPublic(t))
	if len(res.Findings) != 1 || res.Findings[0].Severity != Warning {
		t.Fatalf("findings = %v, want exactly the one warning", res.Findings)
	}
	w := res.Findings[0].String()
	for _, want := range []string{"no dns records are rendered", "whoami-admin.apps.example.org, whoami.apps.example.org"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q does not contain %q", w, want)
		}
	}
}

func TestAPublicOnlySiteIsIndependentOfTheOrderOfTheDocuments(t *testing.T) {
	a := Build(loadPublic(t))
	stacks := loadPublic(t)
	for _, s := range stacks {
		for i, j := 0, len(s.Resources)-1; i < j; i, j = i+1, j-1 {
			s.Resources[i], s.Resources[j] = s.Resources[j], s.Resources[i]
		}
	}
	stacks[0], stacks[1] = stacks[1], stacks[0]
	b := Build(stacks)
	if a.Caddy != b.Caddy || a.SSO != b.SSO || a.ACL != b.ACL || a.Zone != b.Zone {
		t.Error("the same documents in another order rendered differently")
	}
}

func TestAPublicOnlySiteErrorsStopTheRenderingAndSayWhy(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, s []Stack)
		want string
	}{
		{"no zone anywhere", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.dns.zone", "") })
		}, "the ingress helper states no zone"},
		{"a zone that is not a name", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.dns.zone", "Apps_Example") })
		}, "the ingress helper must state the zone the published names end with"},
		{"neither a mesh leg nor a public leg", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.leg.eth0", "discovery") })
		}, "no NIC is stamped user.tink.leg.<nic>: mesh or public"},
		{"a public leg on a NIC with no static address", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "eth0", map[string]string{"type": "nic", "name": "eth0", "network": "tink0"})
			})
		}, "the public leg (eth0) has no static ipv4.address"},
		{"a public leg on a device that is not a NIC", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.leg.eth9", "public") })
		}, "2 NICs are marked public (eth0, eth9)"},
		{"two public legs", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "eth1", map[string]string{"type": "nic", "name": "eth1", "network": "tink0", "ipv4.address": "10.20.50.4"})
				setConfig(r, "user.tink.leg.eth1", "public")
			})
		}, "2 NICs are marked public (eth0, eth1)"},
		{"a leg word that does not exist", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.leg.eth0", "public, wan") })
		}, `is "public, wan": a leg is serves, mesh, public or discovery`},
		{"a leg that says nothing", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.leg.eth0", " , ") })
		}, `is " , ": a leg is serves, mesh, public or discovery`},
		{"a mesh helper with no mesh leg", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "sso", func(r *resolve.Resource) { setConfig(r, "user.tink.role", "mesh") })
		}, "no NIC is stamped user.tink.leg.<nic>: mesh"},
		{"a dns helper with no serves leg", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "sso", func(r *resolve.Resource) {
				setConfig(r, "user.tink.role", "dns")
				setConfig(r, "user.tink.dns.zone", "apps.example.org")
			})
		}, "no NIC is stamped user.tink.leg.<nic>: serves"},
		{"a dns helper and an ingress helper that disagree about the zone", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "sso", func(r *resolve.Resource) {
				setConfig(r, "user.tink.role", "dns")
				setConfig(r, "user.tink.dns.zone", "other.example.org")
			})
		}, `is "apps.example.org", but the dns helper serves "other.example.org": a site has one zone`},
		{"no sign-on helper", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "sso", func(r *resolve.Resource) { setConfig(r, "user.tink.role", "") })
		}, "no instance has user.tink.role: sso"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stacks := loadPublic(t)
			c.edit(t, stacks)
			res := Build(stacks)
			if res.Errors() == 0 {
				t.Fatalf("no error, want one containing %q", c.want)
			}
			if got := messages(res, Error); !strings.Contains(got, c.want) {
				t.Errorf("errors =\n%s\nwant one containing %q", got, c.want)
			}
			if res.DNS != "" || res.Mesh != "" || res.Caddy != "" || res.SSO != "" || res.ACL != "" || len(res.Sections()) != 0 {
				t.Error("an error must stop every rendering")
			}
		})
	}
}

// The public leg is a word about the NIC, and the proxy devices that make it true are checked as warnings: they may be applied after the join is read.
func TestThePublicLegWarnsWhenNothingCarriesTrafficToIt(t *testing.T) {
	cases := []struct {
		name string
		edit func(t *testing.T, s []Stack)
		want string // "" means no warning about the leg
	}{
		{"no proxy device at all", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "http", nil)
				setDevice(r, "https", nil)
			})
		}, "has no proxy device on the host that connects to it"},
		{"proxy devices that connect somewhere else", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "http", map[string]string{"type": "proxy", "nat": "true", "listen": "tcp:203.0.113.10:80", "connect": "tcp:10.20.50.99:80"})
				setDevice(r, "https", map[string]string{"type": "proxy", "nat": "true", "listen": "tcp:203.0.113.10:443", "connect": "tcp:10.20.50.99:443"})
			})
		}, "has no proxy device on the host that connects to it"},
		{"a proxy device bound inside the instance does not count", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "http", nil)
				setDevice(r, "https", map[string]string{"type": "proxy", "bind": "container", "nat": "true", "listen": "tcp:0.0.0.0:443", "connect": "tcp:10.20.50.3:443"})
			})
		}, "has no proxy device on the host that connects to it"},
		{"one of two without nat", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "https", map[string]string{"type": "proxy", "listen": "tcp:203.0.113.10:443", "connect": "tcp:10.20.50.3:443"})
			})
		}, "1 of the 2 proxy device(s) that carry traffic to the public leg (eth0, 10.20.50.3) do not have nat on"},
		{"nat written as 1 counts", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "https", map[string]string{"type": "proxy", "nat": "1", "listen": "tcp:203.0.113.10:443", "connect": "tcp:10.20.50.3:443"})
			})
		}, ""},
		{"an IPv6 listener to the same address is still one that carries", func(t *testing.T, s []Stack) {
			edit(t, s, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
				setDevice(r, "https6", map[string]string{"type": "proxy", "nat": "true", "listen": "tcp:[2001:db8::10]:443", "connect": "tcp:10.20.50.3:443"})
			})
		}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stacks := loadPublic(t)
			c.edit(t, stacks)
			res := Build(stacks)
			if res.Errors() != 0 {
				t.Fatalf("a warning must not be an error: %s", messages(res, Error))
			}
			if res.Caddy != publicGolden(t, "caddy") || res.ACL != publicGolden(t, "acl") {
				t.Error("a warning must not change the rendering")
			}
			w := messages(res, Warning)
			if c.want == "" {
				if strings.Contains(w, "public leg") {
					t.Errorf("unexpected warning about the public leg: %s", w)
				}
				return
			}
			if !strings.Contains(w, c.want) {
				t.Errorf("warnings =\n%s\nwant one containing %q", w, c.want)
			}
		})
	}
}

// A NIC may have several purposes, so one address can be both: the ACL admits it once.
func TestOneNICThatIsMeshAndPublicIsAdmittedOnce(t *testing.T) {
	stacks := load(t)
	edit(t, stacks, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.leg.eth0", "mesh, public") })
	res := Build(stacks)
	if res.Errors() != 0 {
		t.Fatalf("errors: %s", messages(res, Error))
	}
	if len(res.Sources) != 1 || res.Sources[0] != "10.10.50.3" || res.PublicAddress != "10.10.50.3" || res.MeshAddress != "10.10.50.3" {
		t.Errorf("sources %v public %q mesh %q", res.Sources, res.PublicAddress, res.MeshAddress)
	}
	if res.ACL != golden(t, "acl") {
		t.Errorf("acl =\n%s\nwant the same as with a mesh leg alone", res.ACL)
	}
}

// A site that has both a mesh leg and a public leg on different NICs admits both, in one rule, in order.
func TestAMeshLegAndASeparatePublicLegAreBothAdmitted(t *testing.T) {
	stacks := load(t)
	edit(t, stacks, resolve.KindInstance, "ingress", func(r *resolve.Resource) {
		setDevice(r, "eth2", map[string]string{"type": "nic", "nictype": "bridged", "parent": "br1", "ipv4.address": "10.0.99.5/24"})
		setConfig(r, "user.tink.leg.eth2", "public")
	})
	res := Build(stacks)
	if res.Errors() != 0 {
		t.Fatalf("errors: %s", messages(res, Error))
	}
	if got := strings.Join(res.Sources, ","); got != "10.0.99.5,10.10.50.3" {
		t.Errorf("sources = %s", got)
	}
	if !strings.Contains(res.ACL, "source: 10.0.99.5/32,10.10.50.3/32,") {
		t.Errorf("acl =\n%s\nwant both sources in one rule", res.ACL)
	}
	if res.DNS != golden(t, "dns") || res.Mesh != golden(t, "mesh") {
		t.Error("a public leg must not change the dns or mesh renderings")
	}
}

// With a dns helper that states the zone, the ingress helper may state it too, and the two may not disagree; stating it twice is not an error.
func TestAnIngressZoneThatAgreesWithTheDNSHelperIsFine(t *testing.T) {
	stacks := load(t)
	edit(t, stacks, resolve.KindInstance, "ingress", func(r *resolve.Resource) { setConfig(r, "user.tink.dns.zone", "home.example.org") })
	res := Build(stacks)
	if len(res.Findings) != 0 {
		t.Errorf("findings = %v", res.Findings)
	}
}
