package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/prepare"
	"github.com/minihci/tink/internal/resolve"
)

// planServer holds an unstamped profile, an unstamped instance and nothing else: a host that predates stamps.
type planServer struct {
	incus.InstanceServer
	stamp string // what the profile and the instance carry, or "" for nothing
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
	cfg := map[string]string{"user.note": "x"}
	if s.stamp != "" {
		cfg[ownership.Key] = s.stamp
	}
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Config: cfg}}, "", nil
}

func (s *planServer) GetInstance(name string) (*api.Instance, string, error) {
	if name != "app" {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	cfg := map[string]string{"environment.A": "b"}
	if s.stamp != "" {
		cfg[ownership.Key] = s.stamp
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

	// once the objects are stamped the output is the old output, to the byte
	out, err = runPlan(t, &planServer{stamp: "lab"}, ownershipStack(true))
	if err != nil || out != before {
		t.Errorf("stamped here: err %v, output:\n%s", err, out)
	}
	// stamped by another stack: today's output (only a volume says anything), and no line
	out, err = runPlan(t, &planServer{stamp: "other"}, ownershipStack(true))
	if err != nil || out != before {
		t.Errorf("stamped elsewhere: err %v, output:\n%s", err, out)
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
