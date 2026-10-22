package derive

import (
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// serverWith stores stacks the way `plan apply` leaves them: each is planned (resolve.Levels) so that its publishes are stamped onto its instances.
func serverWith(t *testing.T, files ...string) *serverFake {
	t.Helper()
	f := &serverFake{}
	for _, file := range files {
		rs, err := resolve.LoadFiles([]string{file})
		if err != nil {
			t.Fatal(err)
		}
		levels, err := resolve.Levels(rs)
		if err != nil {
			t.Fatal(err)
		}
		for _, level := range levels {
			for _, r := range level {
				switch r.Kind {
				case resolve.KindInstance:
					f.instances = append(f.instances, api.Instance{Name: r.Name, Project: r.Project, Type: "container",
						InstancePut: api.InstancePut{Config: r.Config, Devices: r.Devices, Profiles: r.Profiles}})
				case resolve.KindProfile:
					f.profiles = append(f.profiles, api.Profile{Name: r.Name, Project: r.Project, ProfilePut: api.ProfilePut{Config: r.Config, Devices: r.Devices}})
				case resolve.KindNetwork:
					f.networks = append(f.networks, api.Network{Name: r.Name, Type: "bridge", Managed: true, NetworkPut: api.NetworkPut{Config: r.Config}})
				}
			}
		}
	}
	return f
}

// site is the join's platform stack with the two files that carry what the site publishes: the proxy's routes, and the sign-on helper's rules
// inside a list that has a rule of its own.
func site(t *testing.T) []resolve.Resource {
	t.Helper()
	rs, err := resolve.LoadFiles([]string{"../join/testdata/platform.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	rs = append(rs, []resolve.Resource{
		{Kind: resolve.KindFile, Name: "ingress-routes", Instance: "ingress", Project: "tink", Path: "/etc/caddy/apps.caddy", Derive: resolve.DeriveCaddy,
			Content: "# routes\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"},
		{Kind: resolve.KindFile, Name: "sso-rules", Instance: "sso", Project: "tink", Path: "/app/configuration.yml", Derive: resolve.DeriveSSO,
			Content: "rules:\n  - domain: auth.home.example.org\n    policy: bypass\n  " + resolve.DeriveBegin + "\n  " + resolve.DeriveEnd + "\nsession:\n"},
	}...)
	return rs
}

func file(rs []resolve.Resource, name string) resolve.Resource {
	for _, r := range rs {
		if r.Kind == resolve.KindFile && r.Name == name {
			return r
		}
	}
	return resolve.Resource{}
}

// The files hold the names that the stacks on the server publish, as the join renders them: a route and a rule for each.
func TestDerivedFilesHoldWhatTheServerPublishes(t *testing.T) {
	srv := serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	rs := site(t)
	notices, err := DerivedFiles(srv, rs)
	if err != nil {
		t.Fatal(err)
	}
	caddy := file(rs, "ingress-routes").Content
	for _, want := range []string{"@whoami host whoami.home.example.org", "reverse_proxy 198.51.100.10:80", "@whoami_admin host whoami-admin.home.example.org"} {
		if !strings.Contains(caddy, want) {
			t.Errorf("the routes lack %q:\n%s", want, caddy)
		}
	}
	if !strings.HasPrefix(caddy, "# routes\n"+resolve.DeriveBegin+"\n") || !strings.HasSuffix(caddy, resolve.DeriveEnd+"\n") {
		t.Errorf("the file around the region changed:\n%s", caddy)
	}
	sso := file(rs, "sso-rules").Content
	for _, want := range []string{"  - domain: auth.home.example.org\n    policy: bypass\n", "  - domain: 'whoami.home.example.org'\n    policy: one_factor\n    subject: 'group:family'\n", "  - domain: 'whoami-admin.home.example.org'"} {
		if !strings.Contains(sso, want) {
			t.Errorf("the rules lack %q:\n%s", want, sso)
		}
	}
	if len(notices) != 2 || !strings.Contains(notices[0], "2 published name(s): whoami, whoami-admin") {
		t.Errorf("notices = %v", notices)
	}
}

// Planned again with the same server, the same content: a plan of the site says no changes until a stack changes what is published.
func TestDerivedFilesAreStable(t *testing.T) {
	srv := serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	a, b := site(t), site(t)
	if _, err := DerivedFiles(srv, a); err != nil {
		t.Fatal(err)
	}
	if _, err := DerivedFiles(srv, b); err != nil {
		t.Fatal(err)
	}
	if file(a, "ingress-routes").Content != file(b, "ingress-routes").Content || file(a, "sso-rules").Content != file(b, "sso-rules").Content {
		t.Error("two plans of the same server derive different files")
	}
}

// With nothing published the regions are empty, and the person is told so.
func TestDerivedFilesWithNothingPublished(t *testing.T) {
	rs := site(t)
	notices, err := DerivedFiles(serverWith(t, "../join/testdata/platform.yaml"), rs)
	if err != nil {
		t.Fatal(err)
	}
	if got := file(rs, "ingress-routes").Content; got != "# routes\n"+resolve.DeriveBegin+"\n"+resolve.DeriveEnd+"\n" {
		t.Errorf("the region is not empty:\n%s", got)
	}
	if len(notices) != 2 || !strings.Contains(notices[0], "no name is published") {
		t.Errorf("notices = %v", notices)
	}
}

// A stamp the join cannot read, or an app it will not render, stops the plan: a route missing looks like a route that was never wanted.
func TestDerivedFilesAreAsStrictAsTheJoin(t *testing.T) {
	srv := serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	for i := range srv.instances {
		if srv.instances[i].Name == "whoami" {
			cfg := map[string]string{}
			for k, v := range srv.instances[i].Config {
				cfg[k] = v
			}
			cfg["user.tink.publish.broken"] = "not json"
			srv.instances[i].Config = cfg
		}
	}
	if _, err := DerivedFiles(srv, site(t)); err == nil || !strings.Contains(err.Error(), "whoami") || !strings.Contains(err.Error(), "broken") {
		t.Errorf("an unreadable stamp: err = %v", err)
	}

	srv = serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	for i := range srv.instances {
		if srv.instances[i].Name == "whoami" {
			srv.instances[i].Devices = map[string]map[string]string{"eth0": {"type": "nic", "network": "canary0"}} // takes DHCP
		}
	}
	if _, err := DerivedFiles(srv, site(t)); err == nil || !strings.Contains(err.Error(), "cannot derive the files") {
		t.Errorf("an app on DHCP: err = %v", err)
	}
}

// A file that does not ask is left alone and the server is not read.
func TestNoDerivedFileNoWork(t *testing.T) {
	rs := site(t)
	for i := range rs {
		rs[i].Derive = ""
	}
	f := &serverFake{}
	notices, err := DerivedFiles(f, rs)
	if err != nil || notices != nil || f.calls != 0 {
		t.Errorf("notices %v, err %v, server calls %d", notices, err, f.calls)
	}
}
