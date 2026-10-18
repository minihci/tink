package join

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// serverFake is the read side of a server and nothing more: it embeds the interface, so a call to anything it does not implement (every write) panics
// through the nil and fails the test. FromServer is read-only by that.
type serverFake struct {
	incus.InstanceServer
	instances []api.Instance
	profiles  []api.Profile
	networks  []api.Network
	err       map[string]error // by method name
}

func (f *serverFake) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return f.instances, f.err["instances"]
}
func (f *serverFake) GetProfilesAllProjects() ([]api.Profile, error) {
	return f.profiles, f.err["profiles"]
}
func (f *serverFake) GetNetworks() ([]api.Network, error) { return f.networks, f.err["networks"] }

// serverOf puts stacks on a fake server the way `tink plan apply` would: each stack is planned (resolve.Levels) so its publishes are stamped onto its
// instances, and then every instance, profile and managed network is stored as Incus holds it. An unmanaged bridge is on the server too, as on a real one.
func serverOf(t *testing.T, stacks []Stack) *serverFake {
	t.Helper()
	f := &serverFake{networks: []api.Network{{Name: "br0", Type: "bridge", Managed: false}}}
	for _, s := range stacks {
		levels, err := resolve.Levels(s.Resources)
		if err != nil {
			t.Fatal(err)
		}
		for _, level := range levels {
			for _, r := range level {
				switch r.Kind {
				case resolve.KindInstance:
					f.instances = append(f.instances, api.Instance{Name: r.Name, Project: projectOf(r), Type: "container",
						InstancePut: api.InstancePut{Config: r.Config, Devices: r.Devices, Profiles: r.Profiles}})
				case resolve.KindProfile:
					f.profiles = append(f.profiles, api.Profile{Name: r.Name, Project: projectOf(r), ProfilePut: api.ProfilePut{Config: r.Config, Devices: r.Devices}})
				case resolve.KindNetwork:
					f.networks = append(f.networks, api.Network{Name: r.Name, Type: "bridge", Managed: true, NetworkPut: api.NetworkPut{Config: r.Config}})
				}
			}
		}
	}
	return f
}

// whoami finds the canary app's instance on the fake, to change what is stamped on it.
func (f *serverFake) whoami(t *testing.T) *api.Instance {
	t.Helper()
	for i := range f.instances {
		if f.instances[i].Name == "whoami" {
			return &f.instances[i]
		}
	}
	t.Fatal("no whoami on the fake server")
	return nil
}

func fromServer(t *testing.T, f *serverFake) Result {
	t.Helper()
	stacks, err := FromServer(f)
	if err != nil {
		t.Fatal(err)
	}
	return Build(stacks)
}

// The test this step exists for: the declarations are on the SERVER, as stamps, and the join built from the server renders exactly what the join built
// from the files renders. The expected text is the hand-written golden files, not output of this code.
func TestTheServersStampsJoinToTheSamePiecesAsTheFiles(t *testing.T) {
	f := serverOf(t, load(t))
	if _, ok := f.whoami(t).Config["user.tink.publish.whoami-admin"]; !ok {
		t.Fatalf("plan did not stamp the publish onto the instance: %v", f.whoami(t).Config)
	}
	res := fromServer(t, f)
	if len(res.Findings) != 0 {
		t.Fatalf("findings: %v", res.Findings)
	}
	if res.Zone != "home.example.org" || res.LANAddress != "192.0.2.231" || res.MeshAddress != "10.10.50.3" || !res.HasMesh {
		t.Errorf("facts = zone %q lan %q mesh %q hasMesh %v", res.Zone, res.LANAddress, res.MeshAddress, res.HasMesh)
	}
	for name, got := range map[string]string{"dns": res.DNS, "mesh": res.Mesh, "caddy": res.Caddy, "sso": res.SSO, "acl": res.ACL} {
		if want := golden(t, name); got != want {
			t.Errorf("%s from the server differs from golden/%s.txt\n--- got\n%s--- want\n%s", name, name, got, want)
		}
	}
	// and the whole result, not only the text, is the one the files give
	files := Build(load(t))
	if !reflect.DeepEqual(res.Published, files.Published) || res.DNSProject != files.DNSProject || res.DNSInstance != files.DNSInstance {
		t.Errorf("published/where differ:\nserver %+v\nfiles  %+v", res.Published, files.Published)
	}
}

func TestJoiningFromTheServerDoesNotDependOnTheOrderTheServerListsThings(t *testing.T) {
	f := serverOf(t, load(t))
	a := fromServer(t, f)
	for i, j := 0, len(f.instances)-1; i < j; i, j = i+1, j-1 {
		f.instances[i], f.instances[j] = f.instances[j], f.instances[i]
	}
	for i, j := 0, len(f.profiles)-1; i < j; i, j = i+1, j-1 {
		f.profiles[i], f.profiles[j] = f.profiles[j], f.profiles[i]
	}
	for i, j := 0, len(f.networks)-1; i < j; i, j = i+1, j-1 {
		f.networks[i], f.networks[j] = f.networks[j], f.networks[i]
	}
	b := fromServer(t, f)
	if a.DNS != b.DNS || a.Mesh != b.Mesh || a.Caddy != b.Caddy || a.SSO != b.SSO || a.ACL != b.ACL {
		t.Error("the same server listed in another order rendered differently")
	}
}

func TestFromServerReadsTheStoredConfigAndDevicesNotTheExpandedOnes(t *testing.T) {
	f := serverOf(t, load(t))
	// what a real listing also carries: the expanded view, which already holds the profile's devices
	w := f.whoami(t)
	w.ExpandedConfig = map[string]string{"user.tink.publish.expanded": "garbage"}
	w.ExpandedDevices = map[string]map[string]string{"eth9": {"type": "nic", "network": "nowhere"}}
	if res := fromServer(t, f); len(res.Findings) != 0 || res.DNS != golden(t, "dns") {
		t.Errorf("the expanded view leaked into the join: %v", res.Findings)
	}
}

// An invalid stamp is an error that names the instance, and nothing is rendered: never skipped, never repaired, never a crash.
func TestAStampThatCannotBeReadIsAnErrorNamingTheInstanceAndNothingIsJoined(t *testing.T) {
	cases := []struct {
		name, key, value, want string
	}{
		{"not JSON", "user.tink.publish.whoami", `family`, "not a JSON object"},
		{"empty", "user.tink.publish.whoami", ``, "not a JSON object"},
		{"a field it should not have", "user.tink.publish.whoami", `{"allow":["family"],"offer":"web","instance":"other"}`, `"instance" is not a field`},
		{"no allow", "user.tink.publish.whoami", `{"offer":"web"}`, `no "allow"`},
		{"nobody may open it", "user.tink.publish.whoami", `{"allow":[],"offer":"web"}`, "needs allow"},
		{"a typed subject", "user.tink.publish.whoami", `{"allow":["group:family"],"offer":"web"}`, "plain group names"},
		{"a label that is not a host label", "user.tink.publish.Who_Am_I", `{"allow":["family"],"offer":"web"}`, "host label"},
		{"an offer that is not a name", "user.tink.publish.whoami", `{"allow":["family"],"offer":"Web!"}`, "is not an offer name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := serverOf(t, load(t))
			cfg := map[string]string{}
			for k, v := range f.whoami(t).Config {
				cfg[k] = v
			}
			cfg[c.key] = c.value
			f.whoami(t).Config = cfg

			stacks, err := FromServer(f)
			var se *StampError
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want a *StampError", err)
			}
			if stacks != nil {
				t.Errorf("a server with a stamp that cannot be read must give no stacks (a partial join looks complete): %v", stacks)
			}
			if len(se.Problems) != 1 || se.Problems[0].Instance != "whoami" || se.Problems[0].Project != "canary" || se.Problems[0].Key != c.key {
				t.Fatalf("problems = %+v", se.Problems)
			}
			fs := se.Findings()
			if len(fs) != 1 || fs[0].Severity != Error || fs[0].Subject != "instance/whoami" ||
				!strings.Contains(fs[0].Message, "(project canary)") || !strings.Contains(fs[0].Message, c.key) || !strings.Contains(fs[0].Message, c.want) {
				t.Errorf("finding = %+v, want an error naming instance/whoami, the project, the key and %q", fs, c.want)
			}
			if msg := se.Error(); !strings.Contains(msg, "1 publish stamp(s)") || !strings.Contains(msg, "instance/whoami") {
				t.Errorf("Error() = %q", msg)
			}
		})
	}
}

func TestEveryBadStampIsReportedNotOnlyTheFirstAndAGoodOneDoesNotRescueThem(t *testing.T) {
	f := serverOf(t, load(t))
	cfg := map[string]string{}
	for k, v := range f.whoami(t).Config {
		cfg[k] = v
	}
	cfg["user.tink.publish.zzz"] = `{"allow":"family","offer":"web"}`
	cfg["user.tink.publish.aaa"] = `nope`
	f.whoami(t).Config = cfg // the two good stamps are still there
	_, err := FromServer(f)
	var se *StampError
	if !errors.As(err, &se) || len(se.Problems) != 2 {
		t.Fatalf("err = %v", err)
	}
	if se.Problems[0].Key != "user.tink.publish.aaa" || se.Problems[1].Key != "user.tink.publish.zzz" {
		t.Errorf("problems are not in a stable order: %+v", se.Problems)
	}
}

func TestTwoInstancesStampingOneNameIsTheJoinsOwnDuplicateError(t *testing.T) {
	f := serverOf(t, load(t))
	for i := range f.instances {
		if f.instances[i].Name == "sso" {
			cfg := map[string]string{"user.tink.publish.whoami": `{"allow":["family"],"offer":"web"}`}
			for k, v := range f.instances[i].Config {
				cfg[k] = v
			}
			f.instances[i].Config = cfg
		}
	}
	res := fromServer(t, f)
	if !strings.Contains(messages(res, Error), `the name "whoami" is also published by`) {
		t.Errorf("findings: %v", res.Findings)
	}
	if res.DNS != "" {
		t.Errorf("a join with an error renders nothing: %q", res.DNS)
	}
}

func TestFromServerSeesAnOfferNicThatComesFromAProfileAndAnUnmanagedBridgeAsTheJoinDoes(t *testing.T) {
	f := serverOf(t, load(t))
	// move the NIC into the profile: the instance's own devices no longer have it
	for i := range f.profiles {
		if f.profiles[i].Name == "canary-base" {
			f.profiles[i].Devices = map[string]map[string]string{
				"root": f.profiles[i].Devices["root"],
				"eth0": {"type": "nic", "name": "eth0", "network": "canary0", "ipv4.address": "10.10.60.77"},
			}
		}
	}
	f.whoami(t).Devices = nil
	if res := fromServer(t, f); res.Errors() != 0 || !strings.Contains(res.Caddy, "10.10.60.77:80") {
		t.Errorf("the NIC from the profile should be used: %v\n%s", res.Findings, res.Caddy)
	}
	// a NIC bridged to br0, which is on the server but is not a network Incus manages: no ACL can restrict it
	f.whoami(t).Devices = map[string]map[string]string{"eth0": {"type": "nic", "nictype": "bridged", "parent": "br0", "ipv4.address": "192.0.2.50"}}
	if res := fromServer(t, f); !strings.Contains(messages(res, Error), "is not on a managed network") {
		t.Errorf("findings: %v", res.Findings)
	}
}

func TestAnAppsNetworkWithNoAclIsStillAnErrorFromTheServer(t *testing.T) {
	f := serverOf(t, load(t))
	for i := range f.networks {
		if f.networks[i].Name == "canary0" {
			f.networks[i].Config = map[string]string{"ipv4.address": "10.10.60.1/24"}
		}
	}
	if res := fromServer(t, f); !strings.Contains(messages(res, Error), "has no ACL attached (security.acls)") {
		t.Errorf("findings: %v", res.Findings)
	}
}

func TestAServerWithNoStampsHasNothingToRender(t *testing.T) {
	f := serverOf(t, load(t))
	for i := range f.instances {
		cfg := map[string]string{}
		for k, v := range f.instances[i].Config {
			if !strings.HasPrefix(k, resolve.PublishStampPrefix) {
				cfg[k] = v
			}
		}
		f.instances[i].Config = cfg
	}
	res := fromServer(t, f)
	if res.Errors() != 0 || len(res.Published) != 0 || res.DNS != "" || !strings.Contains(messages(res, Warning), "no publish is declared") {
		t.Errorf("findings %v, published %v", res.Findings, res.Published)
	}
}

func TestAFailedReadIsAnErrorAndNotAnEmptyServer(t *testing.T) {
	for _, what := range []string{"instances", "profiles", "networks"} {
		f := serverOf(t, load(t))
		f.err = map[string]error{what: api.StatusErrorf(http.StatusForbidden, "not authorized")}
		stacks, err := FromServer(f)
		if err == nil || stacks != nil || !strings.Contains(err.Error(), what) || !strings.Contains(err.Error(), "not authorized") {
			t.Errorf("%s: stacks %v, err %v", what, stacks, err)
		}
	}
}

// A stamp belongs to the instance that carries it, in the project it is in: not the first instance listed, and not another of the same name.
func TestAStampBelongsToTheInstanceAndProjectThatCarryIt(t *testing.T) {
	f := serverOf(t, load(t))
	// an instance listed before it (project canary sorts first, then the name), with no offer and no stamp
	f.instances = append(f.instances, api.Instance{Name: "aaa", Project: "canary", InstancePut: api.InstancePut{Config: map[string]string{}}})
	// and a second app of the same name in another project, on its own address, carrying a stamp of its own
	f.instances = append(f.instances, api.Instance{Name: "whoami", Project: "other", InstancePut: api.InstancePut{
		Config: map[string]string{
			"user.tink.offer.web.port": "8080", "user.tink.offer.web.proto": "http",
			"user.tink.publish.other-site": `{"allow":["kids"],"offer":"web"}`,
		},
		Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "canary0", "ipv4.address": "10.10.60.11"}},
	}})
	res := fromServer(t, f)
	if res.Errors() != 0 {
		t.Fatalf("findings: %v", res.Findings)
	}
	got := map[string]Published{}
	for _, p := range res.Published {
		got[p.Label] = p
	}
	if p := got["whoami"]; p.Project != "canary" || p.Instance != "whoami" || p.Address != "10.10.60.10" {
		t.Errorf("whoami = %+v", p)
	}
	if p := got["other-site"]; p.Project != "other" || p.Instance != "whoami" || p.Address != "10.10.60.11" || p.Offer.Port != 8080 || !reflect.DeepEqual(p.Allow, []string{"kids"}) {
		t.Errorf("other-site = %+v", p)
	}
	if len(got) != 3 {
		t.Errorf("published = %v", res.Published)
	}
}

// A NIC's network is looked up among the networks Incus manages. The server also lists host bridges it does not manage; those are not networks the join can
// find an ACL on, and must not be taken for one with no ACL attached.
func TestAnUnmanagedBridgeIsNotANetworkTheJoinCanFindAnAclOn(t *testing.T) {
	f := serverOf(t, load(t))
	f.whoami(t).Devices = map[string]map[string]string{"eth0": {"type": "nic", "network": "br0", "ipv4.address": "192.0.2.50"}}
	res := fromServer(t, f)
	if res.Errors() != 0 || !strings.Contains(messages(res, Warning), "network br0 is not declared") {
		t.Errorf("findings: %v", res.Findings)
	}
}
