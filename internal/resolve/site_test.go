package resolve

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func siteStack(site string) []Resource {
	return []Resource{
		{Kind: KindStack, Name: "platform", Project: "tink", Site: site},
		{Kind: KindInstance, Name: "dns", Project: "tink", Config: map[string]string{RoleKey: "dns", "user.tink.dns.zone": "home.example.org"}},
		{Kind: KindInstance, Name: "plain", Project: "tink", Config: map[string]string{"user.tink.offer.web.port": "80"}},
		{Kind: KindInstance, Name: "whoami", Project: "tink", Config: map[string]string{"user.tink.offer.web.port": "80"}},
		pubIn("tink", "whoami", "whoami", "web", "family"),
	}
}

func pubIn(project, name, instance, offer string, allow ...string) Resource {
	return Resource{Kind: KindPublish, Name: name, Project: project, Instance: instance, Offer: offer, Allow: allow}
}

func TestAStackWithASiteStampsItsHelpersAndItsPublishedInstancesAndNothingElse(t *testing.T) {
	rs := siteStack("herd")
	applyStackSite(rs)
	want := map[string]string{"dns": "herd", "plain": "", "whoami": "herd"}
	for _, r := range rs {
		if r.Kind != KindInstance {
			continue
		}
		if got := r.Config[SiteKey]; got != want[r.Name] {
			t.Errorf("instance %s: %s = %q, want %q", r.Name, SiteKey, got, want[r.Name])
		}
	}
	// the stamp is the only change, and the caller's maps were copied and not written into
	if dns := instanceOf(t, rs, "dns"); dns.Config[RoleKey] != "dns" || dns.Config["user.tink.dns.zone"] != "home.example.org" {
		t.Errorf("dns config = %v", dns.Config)
	}
	// twice is once
	again := siteStack("herd")
	applyStackSite(again)
	applyStackSite(again)
	if !reflect.DeepEqual(rs, again) {
		t.Errorf("applying twice changes the result:\n%v\n%v", rs, again)
	}
}

func TestAStackWithNoSiteStampsNothing(t *testing.T) {
	rs := siteStack("")
	applyStackSite(rs)
	for _, r := range rs {
		if _, ok := r.Config[SiteKey]; ok {
			t.Errorf("%s/%s carries %s: a stack with no site is in the unnamed site, which has no stamp", r.Kind, r.Name, SiteKey)
		}
	}
}

// The stamp is in the desired config of the instance, so the ordinary plan converges it: a missing one is an update, no code of its own.
func TestPlanAddsTheSiteStampToAHelperThatHasNone(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "platform", Project: "canary", Site: "herd"},
		{Kind: KindInstance, Name: "whoami", Project: "canary", Config: map[string]string{"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http"}},
		pubIn("canary", "whoami", "whoami", "web", "family"),
	}
	r := desiredWhoami(t, rs)
	live := map[string]string{"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http", "user.tink.publish.whoami": `{"allow":["family"],"offer":"web"}`}
	p, err := planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Changes, "|"); got != `config.user.tink.site: "" -> "herd"` {
		t.Errorf("changes = %s", got)
	}
	live[SiteKey] = "herd"
	if p, err = planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true)); err != nil || p.Action != ActionNone {
		t.Errorf("converged: %+v, %v", p, err)
	}
}

// The key is owned: a stack that stops saying `site:` takes the helper back to the unnamed site, as one that stops publishing takes its stamp back.
func TestPlanRemovesTheSiteStampWhenTheStackNoLongerHasASite(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "platform", Project: "canary"},
		{Kind: KindInstance, Name: "whoami", Project: "canary", Config: map[string]string{"user.tink.offer.web.port": "80"}},
	}
	r := desiredWhoami(t, rs)
	live := map[string]string{"user.tink.offer.web.port": "80", SiteKey: "herd", "user.tink.sitemap": "mine"}
	p, err := planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	// the stamp and only the stamp: a key that merely starts with the same words is not the site stamp and is not the stack's to take back
	if got := strings.Join(p.Changes, "|"); got != `config.user.tink.site: "herd" -> (unset)` {
		t.Errorf("changes = %s", got)
	}
}

func TestTheSiteStampIsNeverAnExtraNorWrittenByExport(t *testing.T) {
	declared := Resource{Config: map[string]string{}}
	live := Resource{Config: map[string]string{SiteKey: "herd", "limits.cpu": "2"}}
	if got := undeclaredExtras(live, declared).Config; !reflect.DeepEqual(got, []string{"limits.cpu"}) {
		t.Errorf("extras = %v: the site stamp is the stack's, planned for removal and not an extra", got)
	}
}

func TestASiteIsNamedLikeAStackAndOnlyOnAStack(t *testing.T) {
	for _, bad := range []string{"Herd", "a b", "-x", "x/y", strings.Repeat("a", 64)} {
		err := Validate(Resource{Kind: KindStack, Name: "platform", Site: bad})
		if err == nil || !strings.Contains(err.Error(), "a site's name") {
			t.Errorf("site %q: err = %v", bad, err)
		}
	}
	for _, ok := range []string{"herd", "home.lab", "trial-2", "a_b"} {
		if err := Validate(Resource{Kind: KindStack, Name: "platform", Site: ok}); err != nil {
			t.Errorf("site %q: %v", ok, err)
		}
	}
	// a field that belongs to the stack, on anything else, is the author's mistake: refused like the other stack-only fields
	if err := Validate(Resource{Kind: KindInstance, Name: "x", Image: "docker-oci:x", Site: "herd"}); err == nil {
		t.Error("a site on an instance was accepted")
	}
}

func TestAnInstanceMayNotDeclareTheSiteStamp(t *testing.T) {
	err := Validate(Resource{Kind: KindInstance, Name: "dns", Image: "docker-oci:x", Config: map[string]string{SiteKey: "herd"}})
	if err == nil || !strings.Contains(err.Error(), "site stamp") || !strings.Contains(err.Error(), "site:") {
		t.Errorf("err = %v", err)
	}
}

func TestASiteInAStackFileLoadsAndStampsAsTheStackDeclares(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	const doc = `kind: stack
name: platform
project: tink
site: herd
---
kind: instance
name: dns
image: docker-oci:coredns/coredns:1.14.7
config:
  user.tink.role: dns
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if got := StackSite(rs); got != "herd" {
		t.Errorf("StackSite = %q", got)
	}
	if dns := instanceOf(t, rs, "dns"); dns.Config[SiteKey] != "herd" || dns.Project != "tink" {
		t.Errorf("dns = %+v: loading is where the project and the site reach the instance", dns)
	}
	// Levels, which plan runs, keeps it
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, level := range levels {
		for _, r := range level {
			if r.Kind == KindInstance && r.Name == "dns" {
				found = r.Config[SiteKey] == "herd"
			}
		}
	}
	if !found {
		t.Error("the planned instance lost its site stamp")
	}
}

func TestAStackNamedTwiceHasNoSite(t *testing.T) {
	rs := []Resource{{Kind: KindStack, Name: "a", Site: "x"}, {Kind: KindStack, Name: "b", Site: "y"}}
	if got := StackSite(rs); got != "" {
		t.Errorf("StackSite = %q", got)
	}
}
