package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/applied"
	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/prepare"
	"github.com/minihci/tink/internal/resolve"
)

// planServer holds an unstamped profile, an unstamped instance and nothing else: a host that predates stamps.
type planServer struct {
	incus.InstanceServer
	stamp string // what the profile and the instance carry, or "" for nothing
	// resource is what they carry as their resource stamp: "" for none, "own" for the address each really has, anything else as it is
	resource string
	// record is whether they also carry the applied record that ownershipStack applies (internal/applied)
	record bool
}

func appliedText(config map[string]string, profiles []string) string {
	rec, _ := applied.Build(applied.Input{Config: config, Profiles: profiles, Withhold: func(string) bool { return false }})
	return applied.Encode(rec)
}

func (s *planServer) resourceFor(address string) map[string]string {
	cfg := map[string]string{}
	if s.stamp != "" {
		cfg[ownership.Key] = s.stamp
	}
	switch s.resource {
	case "":
	case "own":
		cfg[ownership.ResourceKey] = address
	default:
		cfg[ownership.ResourceKey] = s.resource
	}
	return cfg
}

func (s *planServer) UseProject(string) incus.InstanceServer { return s }

func (s *planServer) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return nil, nil
}

func (s *planServer) GetStoragePoolNames() ([]string, error) { return nil, nil }

func (s *planServer) GetProfile(name string) (*api.Profile, string, error) {
	if name != "base" {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	cfg := s.resourceFor("profile/default/base")
	cfg["user.note"] = "x"
	if s.record {
		cfg[ownership.AppliedKey] = appliedText(map[string]string{"user.note": "x"}, nil)
	}
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Config: cfg}}, "", nil
}

func (s *planServer) GetInstance(name string) (*api.Instance, string, error) {
	if name != "app" {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	cfg := s.resourceFor("instance/default/app")
	cfg["environment.A"] = "b"
	if s.record {
		cfg[ownership.AppliedKey] = appliedText(map[string]string{"environment.A": "b"}, []string{"default", "base"})
	}
	return &api.Instance{Name: name, Type: "container", Status: "Running", StatusCode: api.Running,
		InstancePut: api.InstancePut{Config: cfg, Profiles: []string{"default", "base"}}}, "", nil
}

func ownershipStack(named bool) []resolve.Resource {
	rs := []resolve.Resource{
		{Kind: resolve.KindProfile, Name: "base", Config: map[string]string{"user.note": "x"}},
		{Kind: resolve.KindInstance, Name: "app", Profiles: []string{"default", "base"}, Config: map[string]string{"environment.A": "b"}},
	}
	if named {
		rs = append([]resolve.Resource{{Kind: resolve.KindStack, Name: "lab"}}, rs...)
	}
	return rs
}

func runPlan(t *testing.T, server incus.InstanceServer, resources []resolve.Resource, args ...string) (string, error) {
	t.Helper()
	levels, err := resolve.Levels(resources)
	if err != nil {
		t.Fatal(err)
	}
	prepareStack = func(prepare.Request) (*prepare.Prepared, error) {
		return &prepare.Prepared{Server: server, Resources: resources, Levels: levels}, nil
	}
	t.Cleanup(func() { prepareStack = prepare.Prepare })
	var out, errOut bytes.Buffer
	root := newRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"plan", "--offline"}, args...))
	err = root.Execute()
	return out.String(), err
}

// The plan of a stack that exists already is what it was, plus one line about ownership: nothing else moves, and the exit status is the same.
func TestPlanOfAnExistingStackChangesOnlyByTheOwnershipLine(t *testing.T) {
	const before = `level 0:
  profile/base: no changes
level 1:
  instance/app: no changes
`
	// a host with objects that carry no stamp (the state every existing host is in)
	out, err := runPlan(t, &planServer{}, ownershipStack(true))
	if err != nil {
		t.Fatal(err)
	}
	line := ""
	var rest []string
	for _, l := range strings.SplitAfter(out, "\n") {
		if strings.HasPrefix(l, "ownership:") {
			line += l
			continue
		}
		rest = append(rest, l)
	}
	if got := strings.Join(rest, ""); got != before {
		t.Errorf("plan output apart from the ownership line:\n%s\nwant:\n%s", got, before)
	}
	want := "ownership: 2 declared objects have no stamp (made before stamps, or by hand): instance/app, profile/base; nothing changes because of it; `tink plan apply --adopt` stamps them in one pass and restarts none\n"
	if line != want {
		t.Errorf("ownership line:\n%q\nwant:\n%q", line, want)
	}

	// once the objects are stamped, and carry the record of what was applied, the output is the old output, to the byte
	out, err = runPlan(t, &planServer{stamp: "lab", resource: "own", record: true}, ownershipStack(true))
	if err != nil || out != before {
		t.Errorf("stamped here: err %v, output:\n%s", err, out)
	}
	// stamped here and no record yet: the levels read as before and one grouped line says the record will be written, and it is not a change
	out, err = runPlan(t, &planServer{stamp: "lab", resource: "own"}, ownershipStack(true))
	wantRecord := "applied record: 2 declared objects have no record of what was applied yet: instance/app, profile/base; `tink plan apply` writes it, in the same write as any change and otherwise alone, and restarts none\n"
	if err != nil || out != before+wantRecord {
		t.Errorf("stamped here, no record: err %v, output:\n%s", err, out)
	}
	// stamped by another stack: today's output (only a volume says anything), and no line
	out, err = runPlan(t, &planServer{stamp: "other", resource: "own"}, ownershipStack(true))
	if err != nil || out != before {
		t.Errorf("stamped elsewhere: err %v, output:\n%s", err, out)
	}
}

// The stamp dimension is lines and nothing else: a copy and an object with no resource stamp yet are each one grouped line after the levels, the levels
// read as they did, and the exit status is the same.
func TestPlanSaysCopiesAndUnboundObjectsInOneLineEachAndChangesNothingElse(t *testing.T) {
	const before = `level 0:
  profile/base: no changes
level 1:
  instance/app: no changes
`
	for _, c := range []struct {
		name     string
		server   *planServer
		wantLine string
	}{
		{"no resource stamp yet", &planServer{stamp: "lab"},
			"ownership: 2 declared objects have no resource stamp yet: instance/app, profile/base; `tink plan apply` writes it and restarts none\n"},
		{"stamps made for another resource", &planServer{stamp: "lab", resource: "somewhere/else"},
			"ownership: 2 declared objects carry stamps made for another resource (copies?): instance/app, profile/base; nothing acts on them and nothing changes because of it; `tink plan apply --adopt` takes them over for this declaration in one pass and restarts none\n"},
	} {
		out, err := runPlan(t, c.server, ownershipStack(true))
		if err != nil {
			t.Fatalf("%s: a stamp is never an exit status: %v", c.name, err)
		}
		if want := before + c.wantLine; out != want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.name, out, want)
		}
	}
}

func TestPlanOfAnUnnamedStackSaysSoOnceAndChangesNothingElse(t *testing.T) {
	out, err := runPlan(t, &planServer{}, ownershipStack(false))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "ownership:"); n != 1 || !strings.Contains(out, "this stack has no name") {
		t.Errorf("want one line saying the stack has no name:\n%s", out)
	}
	if !strings.Contains(out, "profile/base: no changes") || !strings.Contains(out, "instance/app: no changes") {
		t.Errorf("the plan itself changed:\n%s", out)
	}
}

func TestPlanApplyHasAnAdoptFlagAndPlanDoesNot(t *testing.T) {
	root := newRootCmd()
	plan := findCmd(t, root, "plan")
	apply := findCmd(t, root, "plan", "apply")
	if apply.Flags().Lookup("adopt") == nil {
		t.Error("plan apply has no --adopt")
	}
	if plan.Flags().Lookup("adopt") != nil || plan.PersistentFlags().Lookup("adopt") != nil {
		t.Error("plan only reads: --adopt belongs to apply")
	}
}

// --adopt on a stack with no name has nothing to stamp with: it says so and applies nothing.
func TestAdoptNeedsANamedStack(t *testing.T) {
	_, err := runPlan(t, &planServer{}, ownershipStack(false), "apply", "--adopt")
	if err == nil || !strings.Contains(err.Error(), "no name") || !strings.Contains(err.Error(), "nothing was applied") {
		t.Errorf("err = %v", err)
	}
}
