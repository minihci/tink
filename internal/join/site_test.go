package join

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// siteStacks loads the testdata platform and canary as a site of their own: the same files with every name that is server-wide or project-wide changed
// (the projects, the networks, the zone), and `site: NAME` on both stacks. They are a second copy of the platform, which is what a trial of the next
// version beside the current one is, and what the herd's fixture is beside a host's own platform.
func siteStacks(t *testing.T, site string) []Stack {
	t.Helper()
	dir := t.TempDir()
	rewrite := map[string][][2]string{
		"platform.yaml": {
			{"name: homelab-platform\nproject: tink\n", "name: " + site + "-platform\nsite: " + site + "\nproject: " + site + "\n"},
			{"tink0", site + "0"},
			{"home.example.org", site + ".example.org"},
		},
		"canary.yaml": {
			{"name: canary\nproject: canary\n", "name: " + site + "-canary\nsite: " + site + "\nproject: " + site + "-canary\n"},
			{"canary0", site + "c0"},
		},
	}
	var out []Stack
	for _, f := range []string{"platform.yaml", "canary.yaml"} {
		b, err := os.ReadFile(filepath.Join("testdata", f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, r := range rewrite[f] {
			if !strings.Contains(s, r[0]) {
				t.Fatalf("%s has no %q to rewrite", f, r[0])
			}
			s = strings.ReplaceAll(s, r[0], r[1])
		}
		p := filepath.Join(dir, f)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		rs, err := resolve.LoadFiles([]string{p})
		if err != nil {
			t.Fatal(err)
		}
		name, _ := resolve.StackName(rs)
		out = append(out, Stack{Name: name, Resources: rs})
	}
	return out
}

// labStack is what a lab leaves behind on a host: a project with a second dns helper and a second ingress helper that state a role and no site, as an
// experiment that was never part of the platform does. It is the shape that stopped the herd on a host that already ran a platform.
func labStack(t *testing.T) Stack {
	t.Helper()
	p := filepath.Join(t.TempDir(), "lab.yaml")
	const lab = `kind: stack
name: lab
project: lab
---
kind: instance
name: dns
image: docker-oci:coredns/coredns:1.14.7
config:
  user.tink.role: dns
  user.tink.dns.zone: lab.example.org
---
kind: instance
name: ingress
image: docker-oci:caddybuilds/caddy-cloudflare:2.11.7
config:
  user.tink.role: ingress
`
	if err := os.WriteFile(p, []byte(lab), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := resolve.LoadFiles([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	return Stack{Name: "lab", Resources: rs}
}

func TestOneStackMakesItsHelpersAndPublishedInstancesMembersOfItsSite(t *testing.T) {
	for _, s := range siteStacks(t, "trial") {
		for _, r := range s.Resources {
			if r.Kind != resolve.KindInstance {
				continue
			}
			role := strings.TrimSpace(r.Config[resolve.RoleKey]) != ""
			published := r.Name == "whoami"
			has := r.Config[resolve.SiteKey] == "trial"
			if (role || published) != has {
				t.Errorf("instance %s: role %v published %v, but its site stamp is %q", r.Name, role, published, r.Config[resolve.SiteKey])
			}
		}
	}
}

// The case #189 is for. A server holds a platform and the labs' helpers, none of them in a site, so the unnamed site has two dns and two ingress helpers
// and cannot be joined. A second site that names itself is joined all the same, and what it renders is its own.
func TestANamedSiteBesideAnUnnamedPlatformAndLabsJoinsOnItsOwn(t *testing.T) {
	f := serverOf(t, append(append(load(t), labStack(t)), siteStacks(t, "trial")...))

	// the unnamed site is as broken as it was on the host: two of each, and the message says which
	unnamed, err := FromServer(f, "")
	if err != nil {
		t.Fatal(err)
	}
	res := Build(unnamed)
	msgs := messages(res, Error)
	for _, want := range []string{"2 instances have user.tink.role: ingress (lab/ingress, tink/ingress)", "2 instances have user.tink.role: dns (lab/dns, tink/dns)"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("the unnamed site's errors do not say %q:\n%s", want, msgs)
		}
	}
	if len(res.DNSHelpers) != 2 || res.DNSInstance != "" {
		t.Errorf("DNSHelpers = %v, DNSInstance = %q: two helpers is not one", res.DNSHelpers, res.DNSInstance)
	}

	// the named site is not: it reads its own helpers and its own publishes, and renders its own zone
	named, err := FromServer(f, "trial")
	if err != nil {
		t.Fatal(err)
	}
	got := Build(named)
	// the one finding is the note of what was left out: the unnamed site's seven helpers and the two publishes of its apps
	if got.Errors() != 0 || len(got.Findings) != 1 || !strings.Contains(got.Findings[0].Message, "the unnamed site (7 helper(s), 2 publish(es))") {
		t.Fatalf("findings for site trial: %v", got.Findings)
	}
	if got.Zone != "trial.example.org" || got.DNSProject != "trial" || got.DNSInstance != "dns" {
		t.Errorf("zone %q, dns %s/%s", got.Zone, got.DNSProject, got.DNSInstance)
	}
	if len(got.Published) != 2 || got.Published[0].FQDN != "whoami-admin.trial.example.org" && got.Published[1].FQDN != "whoami-admin.trial.example.org" {
		t.Errorf("published = %+v", got.Published)
	}
	for _, p := range got.Published {
		if !strings.HasSuffix(p.FQDN, ".trial.example.org") || p.Project != "trial-canary" {
			t.Errorf("a name of another site leaked into this one: %+v", p)
		}
	}
	if strings.Contains(got.DNS, "home.example.org") || strings.Contains(got.Caddy, "home.example.org") || strings.Contains(got.SSO, "home.example.org") {
		t.Errorf("the first site's names are in the second site's renderings:\n%s", got.DNS)
	}
}

// named is the testdata platform and canary with `site: NAME` added to both stacks and nothing else changed, so what they render is still the hand-written
// golden files.
func named(t *testing.T, site string) []Stack {
	t.Helper()
	dir := t.TempDir()
	var out []Stack
	for _, f := range []struct{ file, header string }{{"platform.yaml", "name: homelab-platform\n"}, {"canary.yaml", "name: canary\n"}} {
		b, err := os.ReadFile(filepath.Join("testdata", f.file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), f.header) {
			t.Fatalf("%s has no %q", f.file, f.header)
		}
		p := filepath.Join(dir, f.file)
		if err := os.WriteFile(p, []byte(strings.Replace(string(b), f.header, f.header+"site: "+site+"\n", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		rs, err := resolve.LoadFiles([]string{p})
		if err != nil {
			t.Fatal(err)
		}
		name, _ := resolve.StackName(rs)
		out = append(out, Stack{Name: name, Resources: rs})
	}
	return out
}

// The other half of "the platform's own files are unchanged by it, and the fixture's by the platform": the platform, named, renders exactly the
// hand-written golden files with the labs' helpers and a second site beside it on the server, and so does the same platform read from its files.
func TestANamedPlatformRendersItsGoldenFilesBesideAnotherSiteAndTheLabs(t *testing.T) {
	f := serverOf(t, append(append(named(t, "main"), labStack(t)), siteStacks(t, "trial")...))
	st, err := FromServer(f, "main")
	if err != nil {
		t.Fatal(err)
	}
	res := Build(st)
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, `the unnamed site (2 helper(s)), site "trial" (5 helper(s), 2 publish(es))`) {
		t.Fatalf("findings: %v", res.Findings)
	}
	for name, got := range map[string]string{"dns": res.DNS, "mesh": res.Mesh, "caddy": res.Caddy, "sso": res.SSO, "acl": res.ACL} {
		if want := golden(t, name); got != want {
			t.Errorf("%s of site main differs from golden/%s.txt\n--- got\n%s--- want\n%s", name, name, got, want)
		}
	}
	fromFiles := Build(ForSite(append(append(named(t, "main"), labStack(t)), siteStacks(t, "trial")...), "main"))
	if fromFiles.Errors() != 0 || fromFiles.DNS != res.DNS || fromFiles.Caddy != res.Caddy || fromFiles.ACL != res.ACL {
		t.Errorf("site main from files differs from the server's: %v", fromFiles.Findings)
	}

	// and the trial site is what it renders on a server that holds only it
	alone, err := FromServer(serverOf(t, siteStacks(t, "trial")), "trial")
	if err != nil {
		t.Fatal(err)
	}
	beside, err := FromServer(f, "trial")
	if err != nil {
		t.Fatal(err)
	}
	a, b := Build(alone), Build(beside)
	if a.Errors() != 0 || b.Errors() != 0 {
		t.Fatalf("findings: alone %v, beside %v", a.Findings, b.Findings)
	}
	for name, pair := range map[string][2]string{"dns": {a.DNS, b.DNS}, "mesh": {a.Mesh, b.Mesh}, "caddy": {a.Caddy, b.Caddy}, "sso": {a.SSO, b.SSO}, "acl": {a.ACL, b.ACL}} {
		if pair[0] != pair[1] {
			t.Errorf("the %s section of site trial changes when other sites are on the server:\n--- alone\n%s--- beside\n%s", name, pair[0], pair[1])
		}
	}
}

// A platform that names its site is joined without the labs, whose helpers state no site and are not in it: this is naming the platform, the second way
// out for a host that cannot clean up its labs.
func TestNamingThePlatformLeavesTheLabsOut(t *testing.T) {
	f := serverOf(t, append(named(t, "main"), labStack(t)))
	stacks, err := FromServer(f, "main")
	if err != nil {
		t.Fatal(err)
	}
	res := Build(stacks)
	if res.Errors() != 0 {
		t.Fatalf("findings: %v", res.Findings)
	}
	if res.DNSProject != "tink" || res.DNSInstance != "dns" {
		t.Errorf("dns helper = %s/%s", res.DNSProject, res.DNSInstance)
	}
}

// Isolation includes faults. A publish stamp that cannot be read on an instance of one site stops that site (a half-read name would be half-rendered),
// and not the others: the site that is not at fault is joined and the one that is names the stamp.
func TestAStampThatCannotBeReadStopsItsOwnSiteAndNoOther(t *testing.T) {
	f := serverOf(t, append(load(t), siteStacks(t, "trial")...))
	for i := range f.instances {
		in := &f.instances[i]
		if in.Project == "trial-canary" && in.Name == "whoami" {
			cfg := map[string]string{}
			for k, v := range in.Config {
				cfg[k] = v
			}
			cfg["user.tink.publish.broken"] = "not json"
			in.Config = cfg
		}
	}
	if _, err := FromServer(f, ""); err != nil {
		t.Errorf("the unnamed site was stopped by a stamp of site trial: %v", err)
	}
	if _, err := PlatformFromServer(f, ""); err != nil {
		t.Errorf("the unnamed site's platform was stopped by a stamp of site trial: %v", err)
	}
	_, err := FromServer(f, "trial")
	var se *StampError
	if !errors.As(err, &se) || len(se.Problems) != 1 || se.Problems[0].Instance != "whoami" {
		t.Errorf("site trial err = %v: it should name the one bad stamp", err)
	}
}

// A site that is asked for and not there is the usual mistake (a typo, or the unnamed site on a server whose platform is named). The error is the join's own
// "no helper", and a warning beside it says that helpers of other sites exist and were left out, because that is what the person needs to know.
func TestAViewWithNoHelpersSaysThatOtherSitesHaveThem(t *testing.T) {
	f := serverOf(t, siteStacks(t, "trial"))
	stacks, err := FromServer(f, "")
	if err != nil {
		t.Fatal(err)
	}
	res := Build(stacks)
	if len(res.Published) != 0 || res.DNSInstance != "" {
		t.Fatalf("a view of the unnamed site found %d publishes and dns %q on a server whose only site is trial", len(res.Published), res.DNSInstance)
	}
	if w := messages(res, Warning); !strings.Contains(w, `site "trial" (5 helper(s), 2 publish(es))`) || !strings.Contains(w, "--site NAME") {
		t.Errorf("warnings do not name the other site:\n%s", w)
	}

	// and a view of a server with no other site has nothing to say
	ok, err := FromServer(f, "trial")
	if err != nil {
		t.Fatal(err)
	}
	if w := messages(Build(ok), Warning); strings.Contains(w, "other sites") {
		t.Errorf("a working view mentions other sites:\n%s", w)
	}
}

// With nothing published the join returns early and used to leave the dns helper unset for two or more as for none, so a caller said "no dns helper was
// found" about a server that had two. DNSHelpers is what tells them apart.
func TestSeveralDNSHelpersAreNotNoneEvenWhenNothingIsPublished(t *testing.T) {
	lab := labStack(t)
	two := []Stack{{Name: "platform", Resources: load(t)[0].Resources}, lab}
	res := Build(two)
	if len(res.DNSHelpers) != 2 || res.DNSInstance != "" {
		t.Fatalf("DNSHelpers = %v, DNSInstance = %q", res.DNSHelpers, res.DNSInstance)
	}
	if msg := res.NoDNSHelper(); !strings.Contains(msg, "2 dns helpers were found") || !strings.Contains(msg, "lab/dns") || !strings.Contains(msg, "default/dns") && !strings.Contains(msg, "tink/dns") {
		t.Errorf("message = %q", msg)
	}
	none := Build([]Stack{{Name: "empty"}})
	if msg := none.NoDNSHelper(); !strings.Contains(msg, "no dns helper was found") {
		t.Errorf("message = %q", msg)
	}
}

// Files and the server give one view. A site cut out of the files (ForSite) and the same site read from a server that holds them are the same join.
func TestASiteCutFromFilesIsTheSiteReadFromTheServer(t *testing.T) {
	all := append(append(load(t), labStack(t)), siteStacks(t, "trial")...)
	f := serverOf(t, all)
	fromFiles := Build(ForSite(all, "trial"))
	st, err := FromServer(f, "trial")
	if err != nil {
		t.Fatal(err)
	}
	fromServerRes := Build(st)
	if fromFiles.Errors() != 0 || fromServerRes.Errors() != 0 {
		t.Fatalf("files %v, server %v", fromFiles.Findings, fromServerRes.Findings)
	}
	if f, s := messages(fromFiles, Warning), messages(fromServerRes, Warning); f != s {
		t.Errorf("the note of what was left out differs between files and server:\nfiles: %s\nserver: %s", f, s)
	}
	if fromFiles.DNS != fromServerRes.DNS || fromFiles.Caddy != fromServerRes.Caddy || fromFiles.SSO != fromServerRes.SSO || fromFiles.ACL != fromServerRes.ACL || fromFiles.Mesh != fromServerRes.Mesh {
		t.Errorf("the site differs between files and server:\nfiles:\n%s\nserver:\n%s", fromFiles.DNS+fromFiles.Caddy, fromServerRes.DNS+fromServerRes.Caddy)
	}
	if got := SitesOf(all); strings.Join(got, ",") != ",trial" {
		t.Errorf("SitesOf = %q, want the unnamed site and trial", got)
	}
	// the stacks it was given are not changed
	if n := len(all[0].Resources); n != len(load(t)[0].Resources) {
		t.Errorf("ForSite changed its input: %d resources", n)
	}
}

func TestMembership(t *testing.T) {
	role := map[string]string{resolve.RoleKey: "dns"}
	named := map[string]string{resolve.RoleKey: "dns", resolve.SiteKey: "herd"}
	for _, c := range []struct {
		name      string
		cfg       map[string]string
		published bool
		site      string
		want      bool
	}{
		{"a helper with no site is in the unnamed site", role, false, "", true},
		{"and not in a named one", role, false, "herd", false},
		{"a helper in a site is in it", named, false, "herd", true},
		{"and not in the unnamed one", named, false, "", false},
		{"or in another", named, false, "other", false},
		{"an app that states nothing is in every view", map[string]string{"user.tink.offer.web.port": "80"}, false, "herd", true},
		{"and the unnamed one", nil, false, "", true},
		{"an app that is published and states no site is the unnamed site's", nil, true, "", true},
		{"and not a named site's", nil, true, "herd", false},
		{"a stamp of the site that is blank is the unnamed site", map[string]string{resolve.SiteKey: "  "}, false, "", true},
	} {
		if got := memberOf(c.cfg, c.published, c.site); got != c.want {
			t.Errorf("%s: memberOf = %v, want %v", c.name, got, c.want)
		}
	}
}

// the fake server must not be asked to do more than read: a site is a filter on what is read, and the readers stay read-only
func TestReadingASiteWritesNothing(t *testing.T) {
	f := &serverFake{instances: []api.Instance{{Name: "x", Project: "p", InstancePut: api.InstancePut{Config: map[string]string{resolve.RoleKey: "dns", resolve.SiteKey: "s"}}}}}
	if _, err := FromServer(f, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := PlatformFromServer(f, "other"); err != nil {
		t.Fatal(err)
	}
}

// A stack that forgot its `site:` leaves its publishes in the unnamed site, and a view of the named site then lacks them with no error. The note is the only
// sign, so it counts the publishes and not only the helpers.
func TestAPublishLeftOutOfTheSiteIsCounted(t *testing.T) {
	forgot := load(t) // the unnamed platform and canary, as stacks that never said `site:`
	all := append(siteStacks(t, "trial"), forgot...)
	var n int
	for _, s := range forgot {
		for _, r := range s.Resources {
			if r.Kind == resolve.KindPublish {
				n++
			}
		}
	}
	if n == 0 {
		t.Fatal("the fixture has no publish to forget")
	}
	res := Build(ForSite(all, "trial"))
	want := fmt.Sprintf("%d publish(es)", n)
	if w := messages(res, Warning); !strings.Contains(w, "the unnamed site") || !strings.Contains(w, want) {
		t.Errorf("warnings do not count the %d publishes left out:\n%s", n, w)
	}
}
