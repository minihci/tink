package resolve

import (
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// An instance that exists but is not running, or that lacks a profile the stack names (plan_instance.go). Found by reading the code, not live: a
// create that fails after the instance and its config exist but before a successful start leaves a stopped instance, and plan said "no changes".

// stateFake is the instance "app" with a given status and profile list, recording every state change asked of it.
type stateFake struct {
	incus.InstanceServer
	inst    *api.Instance
	actions []string
}

func newStateFake(status string, profiles ...string) *stateFake {
	return &stateFake{inst: &api.Instance{
		Name: "app", Type: "container", Status: status,
		InstancePut: api.InstancePut{Profiles: profiles},
	}}
}

func (s *stateFake) UseProject(string) incus.InstanceServer { return s }

func (s *stateFake) GetInstance(string) (*api.Instance, string, error) {
	cp := *s.inst
	return &cp, "", nil
}

func (s *stateFake) GetProfile(name string) (*api.Profile, string, error) {
	return &api.Profile{Name: name}, "", nil
}

func (s *stateFake) UpdateInstance(_ string, put api.InstancePut, _ string) (incus.Operation, error) {
	s.inst.Config, s.inst.Devices = put.Config, put.Devices
	return waitOp{}, nil
}

func (s *stateFake) UpdateInstanceState(_ string, put api.InstanceStatePut, _ string) (incus.Operation, error) {
	s.actions = append(s.actions, put.Action)
	if put.Action == "start" {
		s.inst.Status = "Running"
	}
	return waitOp{}, nil
}

func appRes(profiles ...string) Resource {
	return Resource{Kind: KindInstance, Name: "app", Profiles: profiles}
}

func TestPlanStoppedInstanceIsStarted(t *testing.T) {
	p, err := planOne(newStateFake("Stopped", "default"), appRes("default"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionUpdate {
		t.Fatalf("action = %v, want ActionUpdate: a stopped instance is not what the stack declares", p.Action)
	}
	if len(p.Changes) != 1 || !strings.Contains(p.Changes[0], "Stopped") {
		t.Errorf("changes = %q, want one that says the instance is Stopped", p.Changes)
	}
}

// Anything that is not Stopped is left alone: the status is only read for the one case that a failed start leaves.
func TestPlanInstanceThatIsNotStoppedHasNoStateChange(t *testing.T) {
	for _, status := range []string{"Running", "Frozen", ""} {
		p, err := planOne(newStateFake(status, "default"), appRes("default"), PlanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if p.Action != ActionNone || len(p.Changes) != 0 {
			t.Errorf("status %q: action = %v, changes = %q, want none", status, p.Action, p.Changes)
		}
	}
}

// Applying the plan starts it (and only starts it: restart: true would restart a running instance, not a stopped one).
func TestApplyStartsAStoppedInstance(t *testing.T) {
	s := newStateFake("Stopped", "default")
	if err := updateInstance(s, appRes("default")); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.actions, ","); got != "start" {
		t.Errorf("state changes = %q, want start", got)
	}
	p, err := planOne(s, appRes("default"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNone {
		t.Errorf("after the apply: action = %v, want ActionNone", p.Action)
	}
}

func TestApplyDoesNotTouchARunningInstanceWithoutRestart(t *testing.T) {
	s := newStateFake("Running", "default")
	if err := updateInstance(s, appRes("default")); err != nil {
		t.Fatal(err)
	}
	if len(s.actions) != 0 {
		t.Errorf("state changes = %q, want none", s.actions)
	}
}

// A declared profile the instance does not have is blocked: apply does not change an instance's profiles, so it must not say "no changes".
func TestPlanMissingProfileIsBlocked(t *testing.T) {
	p, err := planOne(newStateFake("Running", "default"), appRes("default", "web"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionBlocked {
		t.Fatalf("action = %v, want ActionBlocked", p.Action)
	}
	if len(p.Blocked) == 0 || !strings.Contains(p.Blocked[0], "web") {
		t.Errorf("blocked = %q, want a reason that names the profile web", p.Blocked)
	}
}

// Profiles the stack does not name are not drift (Extras reports them), and a stack that names none says nothing about them.
func TestPlanProfilesNotMissingAreNotAChange(t *testing.T) {
	for name, tc := range map[string]struct {
		live     []string
		declared []string
	}{
		"same":                    {[]string{"default", "web"}, []string{"default", "web"}},
		"order is not compared":   {[]string{"web", "default"}, []string{"default", "web"}},
		"extra live profile":      {[]string{"default", "web", "x"}, []string{"default", "web"}},
		"stack names no profiles": {[]string{"default", "web"}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := planOne(newStateFake("Running", tc.live...), appRes(tc.declared...), PlanOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if p.Action != ActionNone {
				t.Errorf("action = %v (%q %q), want ActionNone", p.Action, p.Changes, p.Blocked)
			}
		})
	}
}

// A stopped instance that is also missing a profile is blocked, and the start is withheld with the rest.
func TestPlanStoppedAndMissingProfileIsBlocked(t *testing.T) {
	p, err := planOne(newStateFake("Stopped", "default"), appRes("default", "web"), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionBlocked {
		t.Fatalf("action = %v, want ActionBlocked", p.Action)
	}
	if got := strings.Join(p.Blocked, "; "); !strings.Contains(got, "withheld") {
		t.Errorf("blocked = %q, want the withheld-change count", got)
	}
}
