package derive

import (
	"io"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// serverFake is the read side of a server and nothing more: it embeds the interface, so a call to anything it does not implement (every write)
// panics through the nil and fails the test.
type serverFake struct {
	incus.InstanceServer
	instances []api.Instance
	profiles  []api.Profile
	networks  []api.Network
	calls     int
	files     map[string]string
	fileErr   error
}

func (f *serverFake) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	f.calls++
	return f.instances, nil
}
func (f *serverFake) GetProfilesAllProjects() ([]api.Profile, error) {
	f.calls++
	return f.profiles, nil
}
func (f *serverFake) GetNetworks() ([]api.Network, error) { f.calls++; return f.networks, nil }

// GetInstanceFile answers from files, keyed "instance:path"; a path not there is a 404, as the real API says. UseProject returns the same fake.
func (f *serverFake) GetInstanceFile(instance, path string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	f.calls++
	if f.fileErr != nil {
		return nil, nil, f.fileErr
	}
	c, ok := f.files[instance+":"+path]
	if !ok {
		return nil, nil, api.StatusErrorf(http.StatusNotFound, "not found")
	}
	return io.NopCloser(strings.NewReader(c)), &incus.InstanceFileResponse{Type: "file"}, nil
}
func (f *serverFake) UseProject(string) incus.InstanceServer { return f }

// platformOnServer stores the platform stack of the join's fixtures as `plan apply` would have left it.
func platformOnServer(t *testing.T) *serverFake {
	t.Helper()
	rs, err := resolve.LoadFiles([]string{"../join/testdata/platform.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	f := &serverFake{networks: []api.Network{{Name: "br0", Type: "bridge", Managed: false}}}
	for _, r := range rs {
		switch r.Kind {
		case resolve.KindInstance:
			f.instances = append(f.instances, api.Instance{Name: r.Name, Project: "tink", Type: "container",
				InstancePut: api.InstancePut{Config: r.Config, Devices: r.Devices, Profiles: r.Profiles}})
		case resolve.KindProfile:
			f.profiles = append(f.profiles, api.Profile{Name: r.Name, Project: "tink", ProfilePut: api.ProfilePut{Config: r.Config, Devices: r.Devices}})
		case resolve.KindNetwork:
			f.networks = append(f.networks, api.Network{Name: r.Name, Type: "bridge", Managed: true, NetworkPut: api.NetworkPut{Config: r.Config}})
		}
	}
	return f
}

// canary loads the join's app stack with its ACL asking for derived rules.
func canary(t *testing.T) []resolve.Resource {
	t.Helper()
	rs, err := resolve.LoadFiles([]string{"../join/testdata/canary.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range rs {
		if rs[i].Kind == resolve.KindNetworkACL {
			rs[i].DeriveIngress = true
		}
	}
	return rs
}

func acl(rs []resolve.Resource) *resolve.Resource {
	for i := range rs {
		if rs[i].Kind == resolve.KindNetworkACL {
			return &rs[i]
		}
	}
	return nil
}

// The rules are the join's own `acl` rendering: one rule for port 80, admitting the ingress helper's mesh-leg address, covering both names.
func TestTheRulesAreTheJoinsRendering(t *testing.T) {
	rs := canary(t)
	notices, err := ACLRules(platformOnServer(t), rs)
	if err != nil {
		t.Fatal(err)
	}
	got := acl(rs).Ingress
	if len(got) != 1 {
		t.Fatalf("want one rule, got %v", got)
	}
	want := api.NetworkACLRule{Action: "allow", Source: "192.0.2.3/32", Protocol: "tcp", DestinationPort: "80", State: "enabled",
		Description: "ingress helper publishes whoami, whoami-admin"}
	if got[0] != want {
		t.Errorf("rule = %+v, want %+v", got[0], want)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "network-acl/canary0-in") || !strings.Contains(notices[0], "whoami, whoami-admin") {
		t.Errorf("notices = %v", notices)
	}
}

// With nothing published, the direction is still declared: an empty list, not a missing one, so a removed publish takes its rule with it.
func TestNothingPublishedDeclaresNone(t *testing.T) {
	rs := canary(t)
	var kept []resolve.Resource
	for _, r := range rs {
		if r.Kind != resolve.KindPublish {
			kept = append(kept, r)
		}
	}
	notices, err := ACLRules(platformOnServer(t), kept)
	if err != nil {
		t.Fatal(err)
	}
	if got := acl(kept).Ingress; got == nil || len(got) != 0 {
		t.Errorf("want an empty, non-nil list, got %#v", got)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "ingress rules are none") {
		t.Errorf("notices = %v", notices)
	}
}

// A stack with no ACL that derives its rules is not touched, and the server is not read.
func TestNoMarkerNoWork(t *testing.T) {
	rs := canary(t)
	acl(rs).DeriveIngress = false
	f := &serverFake{}
	notices, err := ACLRules(f, rs)
	if err != nil || notices != nil || f.calls != 0 {
		t.Errorf("notices %v, err %v, server calls %d: want none of them", notices, err, f.calls)
	}
	if acl(rs).Ingress != nil {
		t.Error("an ACL that did not ask must be left as written")
	}
}

// Without the platform on the server there is nothing to derive from, and the person is told what is missing, not given an empty ACL.
func TestNoPlatformIsAnError(t *testing.T) {
	_, err := ACLRules(&serverFake{}, canary(t))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"network-acl/canary0-in", "ingress helper", "sso helper"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

// Another stack's apps, and their stamps, are not read: a fault in one of them does not stop this stack from being planned, and the server's old
// copy of an instance this stack declares does not make it "declared twice".
func TestOtherAppsOnTheServerAreNotRead(t *testing.T) {
	f := platformOnServer(t)
	f.instances = append(f.instances,
		api.Instance{Name: "stranger", Project: "elsewhere", InstancePut: api.InstancePut{Config: map[string]string{"user.tink.publish.x": "not json"}}},
		api.Instance{Name: "whoami", Project: "canary", InstancePut: api.InstancePut{Config: map[string]string{"user.tink.offer.web.port": "8080", "user.tink.offer.web.proto": "http"}}},
	)
	rs := canary(t)
	if _, err := ACLRules(f, rs); err != nil {
		t.Fatal(err)
	}
	if got := acl(rs).Ingress; len(got) != 1 || got[0].DestinationPort != "80" {
		t.Errorf("the stack's own offer (port 80) must win over the server's old copy (8080): %+v", got)
	}
}
