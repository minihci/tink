package resolve

import (
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
)

// rootProfile is a server-side profile that carries a root disk, which is what a usable `default` profile looks like. The fake servers whose tests plan
// instances answer GetProfile with it, so those plans stay free of the no-root-disk warning they are not about.
func rootProfile(name string) *api.Profile {
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: map[string]map[string]string{
		"root": {"type": "disk", "path": "/", "pool": "default"},
	}}}
}

// profileServer answers the reads the root-disk check makes. A profile that is not in profiles is a 404; one named in failing fails with that error, as a revoked
// client or a dropped connection would. instance, when set, is the one instance that exists.
type profileServer struct {
	incus.InstanceServer
	profiles map[string]*api.Profile
	failing  map[string]error
	instance *api.Instance
}

func (s *profileServer) UseProject(string) incus.InstanceServer { return s }

func (s *profileServer) GetProfile(name string) (*api.Profile, string, error) {
	if err := s.failing[name]; err != nil {
		return nil, "", err
	}
	if p, ok := s.profiles[name]; ok {
		return p, "", nil
	}
	return nil, "", errNotFound
}

func (s *profileServer) GetInstance(name string) (*api.Instance, string, error) {
	if s.instance == nil || s.instance.Name != name {
		return nil, "", errNotFound
	}
	return s.instance, "", nil
}

func profile(name string, devices map[string]map[string]string) *api.Profile {
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: devices}}
}

func declaredProfile(name string, devices map[string]map[string]string) Resource {
	return Resource{Kind: KindProfile, Name: name, Devices: devices}
}

func TestRootDiskWarnings(t *testing.T) {
	root := map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}}
	nicOnly := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}
	instance := func(devices map[string]map[string]string, profiles ...string) Resource {
		return Resource{Kind: KindInstance, Name: "dns", Image: "docker-oci:coredns/coredns:1.14.7", Devices: devices, Profiles: profiles}
	}
	cases := []struct {
		name     string
		r        Resource
		declared []Resource              // profiles the stack declares
		server   map[string]*api.Profile // profiles on the server
		failing  map[string]error        // profiles whose read fails
		wants    []string                // substrings, one per expected warning; nil = none
	}{
		{"no profiles listed and the project's default profile has no root disk", instance(nil),
			nil, map[string]*api.Profile{"default": profile("default", nil)},
			nil, []string{`instance "dns" has no root disk: none of its profiles (the project's "default" profile, because none are listed)`}},
		{"a listed stack-declared profile has no root disk", instance(nil, "base"),
			[]Resource{declaredProfile("base", nicOnly)}, nil,
			nil, []string{`none of its profiles (base) has a disk device with path: /`}},
		{"only the instance itself declares a root device", instance(root, "base"),
			[]Resource{declaredProfile("base", nicOnly)}, nil,
			nil, []string{`instance "dns" has no root disk`}},
		{"only the instance declares a root device and it lists no profiles", instance(root),
			nil, map[string]*api.Profile{"default": profile("default", nil)},
			nil, []string{`the project's "default" profile`}},
		{"a stack-declared profile has a root disk", instance(nil, "base"),
			[]Resource{declaredProfile("base", root)}, nil, nil, nil},
		{"a server-side profile has a root disk", instance(nil, "shared"),
			nil, map[string]*api.Profile{"shared": profile("shared", root)}, nil, nil},
		{"the default profile has a root disk", instance(nil),
			nil, map[string]*api.Profile{"default": profile("default", root)}, nil, nil},
		{"a stack-declared default profile has a root disk", instance(nil),
			[]Resource{declaredProfile("default", root)}, nil, nil, nil},
		{"a declared profile adds devices, so the server's copy of it can supply the root disk", instance(nil, "base"),
			[]Resource{declaredProfile("base", nicOnly)}, map[string]*api.Profile{"base": profile("base", root)}, nil, nil},
		{"a declared profile that is on the server without a root disk too", instance(nil, "base"),
			[]Resource{declaredProfile("base", nicOnly)}, map[string]*api.Profile{"base": profile("base", nicOnly)},
			nil, []string{`none of its profiles (base)`}},
		{"one profile with a root disk is enough", instance(nil, "net", "disk"),
			[]Resource{declaredProfile("net", nicOnly), declaredProfile("disk", root)}, nil, nil, nil},
		{"several profiles, none with a root disk, are all named", instance(nil, "net", "limits"),
			[]Resource{declaredProfile("net", nicOnly)}, map[string]*api.Profile{"limits": profile("limits", nil)},
			nil, []string{`none of its profiles (net, limits)`}},
		{"a profile that cannot be read is not guessed at: server error", instance(nil, "shared"),
			nil, nil, map[string]error{"shared": errServer}, nil},
		{"a profile that cannot be read is not guessed at: 403", instance(nil),
			nil, nil, map[string]error{"default": errForbidden}, nil},
		{"a profile that cannot be read is not guessed at: dropped connection", instance(nil, "shared"),
			nil, nil, map[string]error{"shared": errDropped}, nil},
		{"a declared profile whose server copy cannot be read might have the root disk there", instance(nil, "base"),
			[]Resource{declaredProfile("base", nicOnly)}, nil, map[string]error{"base": errServer}, nil},
		{"an unreadable profile hides the one before it, which might have been the other half", instance(nil, "net", "shared"),
			[]Resource{declaredProfile("net", nicOnly)}, nil, map[string]error{"shared": errServer}, nil},
		{"a profile that exists nowhere is Incus's to report, not this warning's", instance(nil, "ghost"),
			nil, nil, nil, nil},
		{"a profile that exists nowhere hides the one before it", instance(nil, "net", "ghost"),
			[]Resource{declaredProfile("net", nicOnly)}, nil, nil, nil},
		{"a virtual machine is checked like a container", Resource{Kind: KindInstance, Name: "vm", VM: true, Profiles: []string{"base"}},
			[]Resource{declaredProfile("base", nicOnly)}, nil, nil, []string{`instance "vm" has no root disk`}},
		{"a disk at another path is not a root disk", instance(nil, "base"),
			[]Resource{declaredProfile("base", map[string]map[string]string{"data": {"type": "disk", "path": "/data", "pool": "default", "source": "vol"}})},
			nil, nil, []string{`instance "dns" has no root disk`}},
		{"a disk with no path is not a root disk", instance(nil, "base"),
			[]Resource{declaredProfile("base", map[string]map[string]string{"data": {"type": "disk", "pool": "default", "source": "vol"}})},
			nil, nil, []string{`instance "dns" has no root disk`}},
		{"a device at / that is not a disk is not a root disk", instance(nil, "base"),
			[]Resource{declaredProfile("base", map[string]map[string]string{"root": {"type": "none", "path": "/"}})},
			nil, nil, []string{`instance "dns" has no root disk`}},
		{"a root disk under any device name counts", instance(nil, "base"),
			[]Resource{declaredProfile("base", map[string]map[string]string{"system": {"type": "disk", "path": "/", "pool": "fast"}})}, nil, nil, nil},
		{"not an instance", Resource{Kind: KindProfile, Name: "p", Profiles: []string{"base"}},
			[]Resource{declaredProfile("base", nicOnly)}, nil, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			declared := map[string]Resource{}
			for _, p := range c.declared {
				declared[p.Name] = p
			}
			got := rootDiskWarnings(&profileServer{profiles: c.server, failing: c.failing}, c.r, declared)
			if len(got) != len(c.wants) {
				t.Fatalf("got %d warnings %q, want %d", len(got), got, len(c.wants))
			}
			for i, want := range c.wants {
				if !strings.Contains(got[i], want) {
					t.Errorf("warning %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}

func TestRootDiskWarnings_NilStackProfilesAreFine(t *testing.T) {
	r := Resource{Kind: KindInstance, Name: "dns", Profiles: []string{"base"}}
	if got := rootDiskWarnings(&profileServer{profiles: map[string]*api.Profile{"base": profile("base", nil)}}, r, nil); len(got) != 1 {
		t.Errorf("with no stack profiles the server's are still read, got %q", got)
	}
}

// The text says what is wrong, why, and what to do, and names the instance, since a warning can end up in a log away from the plan line it hangs under.
func TestRootDiskWarnings_NamesTheInstanceAndTheFix(t *testing.T) {
	r := Resource{Kind: KindInstance, Name: "coredns-lab", Profiles: []string{"dns-lab-base"}}
	declared := map[string]Resource{"dns-lab-base": declaredProfile("dns-lab-base", map[string]map[string]string{"eth0": {"type": "nic"}})}
	got := rootDiskWarnings(&profileServer{}, r, declared)
	if len(got) != 1 {
		t.Fatalf("got %q, want one warning", got)
	}
	for _, want := range []string{
		`instance "coredns-lab"`,           // which
		"no root disk",                     // what
		"dns-lab-base",                     // the profiles looked at
		"path: /",                          // what a root disk is
		"only the profiles",                // why: creation sends nothing else
		"never the instance's own devices", //
		"a root disk declared on the instance does not help", //
		`"No root device could be found"`,                    // the Incus error it will otherwise be met as
		"kind: profile",                                      // the fix...
		"type: disk",                                         //
		"pool: <pool>",                                       //
		"under profiles:",                                    //
		"docs/resolver-architecture.md",                      // where it is explained
	} {
		if !strings.Contains(got[0], want) {
			t.Errorf("warning = %q, want it to contain %q", got[0], want)
		}
	}
}

func TestPlanOne_NoRootDiskWarnsAndPlanStillCompletes(t *testing.T) {
	noRoot := declaredProfile("base", map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}})
	withRoot := declaredProfile("base", map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": "default"}})
	dns := Resource{Kind: KindInstance, Name: "dns", Profiles: []string{"base"}}
	env := &imageEnv{offline: true, conf: &cliconfig.Config{Remotes: ociTestRemotes}}

	cases := []struct {
		name   string
		stack  []Resource
		server *profileServer
		action Action
		warns  int
	}{
		{"an instance that does not exist yet", []Resource{noRoot, dns}, &profileServer{}, ActionCreate, 1},
		{"it is about the stack, so an instance that exists is told too", []Resource{noRoot, dns}, &profileServer{instance: &api.Instance{Name: "dns"}}, ActionNone, 1},
		{"a root disk in the profile silences it", []Resource{withRoot, dns}, &profileServer{}, ActionCreate, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := PlanOptions{env: env}.ForResources(c.stack)
			p, err := planOne(c.server, dns, opts)
			if err != nil {
				t.Fatalf("planOne() error = %v, a warning must not fail the plan", err)
			}
			if p.Action != c.action {
				t.Errorf("action = %v, want %v", p.Action, c.action)
			}
			if len(p.Warnings) != c.warns {
				t.Fatalf("warnings = %q, want %d", p.Warnings, c.warns)
			}
			if c.warns > 0 && !strings.Contains(p.Warnings[0], `instance "dns" has no root disk`) {
				t.Errorf("warnings = %q, want the no-root-disk warning", p.Warnings)
			}
		})
	}
}

// A profile that cannot be read must never turn into a plan error through this check: planning the instance itself already reads the world, and this adds only a
// warning on top.
func TestPlanOne_UnreadableProfileIsNotAPlanError(t *testing.T) {
	dns := Resource{Kind: KindInstance, Name: "dns", Profiles: []string{"shared"}}
	env := &imageEnv{offline: true, conf: &cliconfig.Config{Remotes: ociTestRemotes}}
	p, err := planOne(&profileServer{failing: map[string]error{"shared": errServer}}, dns, PlanOptions{env: env}.ForResources([]Resource{dns}))
	if err != nil {
		t.Fatalf("planOne() error = %v", err)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("warnings = %q, want none for a profile that could not be read", p.Warnings)
	}
}
