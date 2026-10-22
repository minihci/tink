package derive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

// trialFiles writes a second copy of the join's platform and canary stacks that is a site of its own (`site: trial`), with every name that is
// server-wide or project-wide changed and the ingress helper at another address, so that what is derived for it can only come from its own helpers.
func trialFiles(t *testing.T) (platform, canaryFile string) {
	t.Helper()
	dir := t.TempDir()
	write := func(src string, edits [][2]string) string {
		b, err := os.ReadFile(filepath.Join("..", "join", "testdata", src))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, e := range edits {
			if !strings.Contains(s, e[0]) {
				t.Fatalf("%s has no %q", src, e[0])
			}
			s = strings.ReplaceAll(s, e[0], e[1])
		}
		p := filepath.Join(dir, src)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	platform = write("platform.yaml", [][2]string{
		{"name: homelab-platform\nproject: tink\n", "name: trial-platform\nsite: trial\nproject: trial\n"},
		{"tink0", "trial0"},
		{"home.example.org", "trial.example.org"},
		{"ipv4.address: 192.0.2.3}", "ipv4.address: 192.0.2.103}"},
	})
	canaryFile = write("canary.yaml", [][2]string{
		{"name: canary\nproject: canary\n", "name: trial-canary\nsite: trial\nproject: trial-canary\n"},
		{"canary0", "trialc0"},
	})
	return platform, canaryFile
}

// labFile is a project a lab left behind: a second dns helper and a second ingress helper, in no site.
func labFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lab.yaml")
	const lab = "kind: stack\nname: lab\nproject: lab\n---\n" +
		"kind: instance\nname: dns\nimage: docker-oci:coredns/coredns:1.14.7\nconfig:\n  user.tink.role: dns\n  user.tink.dns.zone: lab.example.org\n---\n" +
		"kind: instance\nname: ingress\nimage: docker-oci:caddybuilds/caddy-cloudflare:2.11.7\nconfig:\n  user.tink.role: ingress\n"
	if err := os.WriteFile(p, []byte(lab), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A host with a platform, the labs' helpers and a second site. The second site's platform is planned: its proxy file holds its own names, from its
// own helpers, and nothing of the first site's. This is what stopped the herd on a host with labs (#189).
func TestASitePlansBesideAnotherSiteAndTheLabs(t *testing.T) {
	trialPlatform, trialCanary := trialFiles(t)
	srv := serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml", labFile(t), trialPlatform, trialCanary)

	rs, err := resolve.LoadFiles([]string{trialPlatform})
	if err != nil {
		t.Fatal(err)
	}
	rs = append(rs, resolve.Resource{Kind: resolve.KindFile, Name: "ingress-routes", Instance: "ingress", Project: "trial", Path: "/etc/caddy/apps.caddy",
		Derive: resolve.DeriveCaddy, Content: "# routes\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"})
	if _, err := DerivedFiles(srv, rs); err != nil {
		t.Fatal(err)
	}
	caddy := file(rs, "ingress-routes").Content
	if !strings.Contains(caddy, "whoami.trial.example.org") || !strings.Contains(caddy, "whoami-admin.trial.example.org") {
		t.Errorf("the site's own names are not in its routes:\n%s", caddy)
	}
	if strings.Contains(caddy, "home.example.org") || strings.Contains(caddy, "lab.example.org") {
		t.Errorf("a name of another site is in the routes:\n%s", caddy)
	}

	// The first site, unnamed, has the same server and the same labs: it is as it was before sites, two of each helper, and the error says which.
	first, err := resolve.LoadFiles([]string{"../join/testdata/platform.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	first = append(first, resolve.Resource{Kind: resolve.KindFile, Name: "ingress-routes", Instance: "ingress", Project: "tink", Path: "/etc/caddy/apps.caddy",
		Derive: resolve.DeriveCaddy, Content: "# routes\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"})
	_, err = DerivedFiles(srv, first)
	if err == nil || !strings.Contains(err.Error(), "lab/ingress") || !strings.Contains(err.Error(), "tink/ingress") {
		t.Errorf("err = %v: it should name both ingress helpers", err)
	}
}

// The ACL's rules admit the ingress helper of the app's own site. The two sites' ingress helpers are at different addresses, so the rule shows which was read.
func TestTheRulesAdmitTheIngressHelperOfTheAppsOwnSite(t *testing.T) {
	trialPlatform, trialCanary := trialFiles(t)
	srv := serverWith(t, "../join/testdata/platform.yaml", labFile(t), trialPlatform)

	rs, err := resolve.LoadFiles([]string{trialCanary})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rs {
		if rs[i].Kind == resolve.KindNetworkACL {
			rs[i].DeriveIngress = true
		}
	}
	if _, err := ACLRules(srv, rs); err != nil {
		t.Fatal(err)
	}
	got := acl(rs).Ingress
	if len(got) != 1 || got[0].Source != "192.0.2.103/32" {
		t.Fatalf("rules = %+v: want one rule admitting the trial site's ingress helper", got)
	}

	// an app of the unnamed site, beside all that, is told what it was told before sites
	plain := canary(t)
	if _, err := ACLRules(serverWith(t, "../join/testdata/platform.yaml"), plain); err != nil {
		t.Fatal(err)
	}
	if got := acl(plain).Ingress; len(got) != 1 || got[0].Source != "192.0.2.3/32" {
		t.Errorf("unnamed site's rules = %+v", got)
	}
}

// With nothing published the join stops early and used to leave the dns helper unset for several as for none, so a derived dns file said "no dns helper
// was found" about a server that had two. It says how many, and where.
func TestSeveralDNSHelpersAreSaidToBeSeveral(t *testing.T) {
	srv := serverWith(t, labFile(t))
	rs := site(t)
	rs = append(rs, resolve.Resource{Kind: resolve.KindFile, Name: "dns-hosts", Instance: "dns", Project: "tink", Path: "/hosts", Derive: resolve.DeriveDNS,
		Content: resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"})
	_, err := DerivedFiles(srv, rs)
	if err == nil {
		t.Fatal("two dns helpers derived a dns file")
	}
	for _, want := range []string{"2 dns helpers were found", "lab/dns", "tink/dns"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v: it lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "no dns helper was found") {
		t.Errorf("err = %v: two is not none", err)
	}
}
