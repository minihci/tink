package derive

import (
	"net/http"
	"os"
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

// dnsSite is the platform with a hosts file for the dns helper and, when zone is not empty, the zone file it declares.
func dnsSite(t *testing.T, zone string) []resolve.Resource {
	t.Helper()
	rs, err := resolve.LoadFiles([]string{"../join/testdata/platform.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	rs = append(rs, resolve.Resource{Kind: resolve.KindFile, Name: "dns-hosts", Instance: "dns", Project: "tink", Path: "/hosts", Derive: resolve.DeriveDNS,
		Content: "# generated\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"})
	if zone != "" {
		rs = append(rs, resolve.Resource{Kind: resolve.KindFile, Name: "dns-zone", Instance: "dns", Project: "tink", Path: "/db.home.example.org", Content: zone})
	}
	return rs
}

const platformZone = "$ORIGIN home.example.org.\n@ IN SOA dns.home.example.org. h.home.example.org. 1 3600 600 86400 60\nauth IN A 192.0.2.3\n"

// The records are the join's dns rendering, one line per published name, answering with the ingress helper's serves-leg address.
func TestDerivedDNSHoldsTheRecords(t *testing.T) {
	rs := dnsSite(t, platformZone)
	notices, err := DerivedFiles(serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml"), rs)
	if err != nil {
		t.Fatal(err)
	}
	got := file(rs, "dns-hosts").Content
	want := "# generated\n" + resolve.DeriveBegin + "\n192.0.2.231 whoami.home.example.org\n192.0.2.231 whoami-admin.home.example.org\n" + resolve.DeriveEnd + "\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "dns derived for 2 published name(s)") {
		t.Errorf("notices = %v", notices)
	}
}

// The zone file may be on the server and not in the stack.
func TestDerivedDNSReadsTheZoneFromTheServer(t *testing.T) {
	srv := serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	srv.files = map[string]string{"dns:/db.home.example.org": platformZone}
	if _, err := DerivedFiles(srv, dnsSite(t, "")); err != nil {
		t.Fatal(err)
	}
}

func TestDerivedDNSRefusals(t *testing.T) {
	srv := func() *serverFake {
		return serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	}
	// a published name that is a record of the platform's own
	zone := platformZone + "whoami IN A 192.0.2.99\n"
	if _, err := DerivedFiles(srv(), dnsSite(t, zone)); err == nil || !strings.Contains(err.Error(), "already holds a record for whoami") {
		t.Errorf("a clash with the zone file: err = %v", err)
	}
	// the same, written the long way
	zone = platformZone + "whoami-admin.home.example.org. IN A 192.0.2.99\n"
	if _, err := DerivedFiles(srv(), dnsSite(t, zone)); err == nil || !strings.Contains(err.Error(), "whoami-admin") {
		t.Errorf("a clash written with the zone: err = %v", err)
	}
	// no zone file anywhere
	if _, err := DerivedFiles(srv(), dnsSite(t, "")); err == nil || !strings.Contains(err.Error(), "neither declared") {
		t.Errorf("no zone file: err = %v", err)
	}
	// a read that fails is not a file that is not there
	failing := srv()
	failing.fileErr = api.StatusErrorf(http.StatusInternalServerError, "database is locked")
	if _, err := DerivedFiles(failing, dnsSite(t, "")); err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Errorf("a failing read: err = %v", err)
	}
	// the file is on the wrong instance
	wrong := dnsSite(t, platformZone)
	for i := range wrong {
		if wrong[i].Name == "dns-hosts" {
			wrong[i].Instance = "sso"
		}
	}
	if _, err := DerivedFiles(srv(), wrong); err == nil || !strings.Contains(err.Error(), "belongs on the dns helper") {
		t.Errorf("a dns file on another instance: err = %v", err)
	}
}

// The mesh view is the whole file: the join's own JSON, one A record per published name, answering with the ingress helper's mesh-leg address.
func TestDerivedMeshIsTheWholeFile(t *testing.T) {
	rs, err := resolve.LoadFiles([]string{"../join/testdata/platform.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	rs = append(rs, resolve.Resource{Kind: resolve.KindFile, Name: "mesh-records", Instance: "mesh", Project: "tink", Path: "/extra-records.json", Derive: resolve.DeriveMesh})
	notices, err := DerivedFiles(serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml"), rs)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../join/testdata/golden/mesh.txt")
	if err != nil {
		t.Fatal(err)
	}
	f := file(rs, "mesh-records")
	if f.Content != string(want) {
		t.Errorf("got:\n%s\nwant:\n%s", f.Content, want)
	}
	if f.DerivedZone != "home.example.org" {
		t.Errorf("the zone the guard looks at = %q", f.DerivedZone)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "mesh derived for 2 published name(s)") {
		t.Errorf("notices = %v", notices)
	}

	// with nothing published the file is an empty array, valid JSON for the control plane
	rs2, _ := resolve.LoadFiles([]string{"../join/testdata/platform.yaml"})
	rs2 = append(rs2, resolve.Resource{Kind: resolve.KindFile, Name: "mesh-records", Instance: "mesh", Project: "tink", Path: "/extra-records.json", Derive: resolve.DeriveMesh})
	if _, err := DerivedFiles(serverWith(t, "../join/testdata/platform.yaml"), rs2); err != nil {
		t.Fatal(err)
	}
	if got := file(rs2, "mesh-records").Content; got != "[]\n" {
		t.Errorf("nothing published: %q", got)
	}
}

// A site with no mesh helper has no mesh view to write, and says so.
func TestDerivedMeshNeedsAMeshHelper(t *testing.T) {
	rs := site(t)
	rs = append(rs, resolve.Resource{Kind: resolve.KindFile, Name: "mesh-records", Instance: "mesh", Project: "tink", Path: "/x.json", Derive: resolve.DeriveMesh})
	for i := range rs {
		if rs[i].Kind == resolve.KindInstance && rs[i].Name == "mesh" {
			rs[i].Config = map[string]string{}
		}
	}
	srv := serverWith(t, "../join/testdata/platform.yaml", "../join/testdata/canary.yaml")
	for i := range srv.instances {
		if srv.instances[i].Name == "mesh" {
			srv.instances[i].Config = map[string]string{}
		}
	}
	if _, err := DerivedFiles(srv, rs); err == nil || !strings.Contains(err.Error(), "no helper has role mesh") {
		t.Errorf("err = %v", err)
	}
}
