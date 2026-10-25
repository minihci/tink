package resolve

import (
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// Incus never stores an empty config value: it reads one as "unset" (#214). So a desired "" for a key the live object lacks is not a difference.

func TestDiffConfig_AnEmptyValueIsUnset(t *testing.T) {
	cases := []struct {
		name     string
		current  map[string]string
		desired  map[string]string
		hidden   map[string]bool
		want     []string
		wantNone bool
	}{
		{name: "desired empty, key missing live: nothing to do", current: map[string]string{}, desired: map[string]string{"environment.X": ""}, wantNone: true},
		{name: "desired empty, key missing live, beside a real change: only the real one", current: map[string]string{},
			desired: map[string]string{"environment.X": "", "limits.cpu": "2"}, want: []string{`config.limits.cpu: "" -> "2"`}},
		{name: "desired non-empty, key missing live: a change", current: map[string]string{}, desired: map[string]string{"environment.X": "a"},
			want: []string{`config.environment.X: "" -> "a"`}},
		{name: "live has a value, desired empty: still a change (applying it removes the key)", current: map[string]string{"environment.X": "old"},
			desired: map[string]string{"environment.X": ""}, want: []string{`config.environment.X: "old" -> ""`}},
		{name: "live has the key empty, desired empty: equal", current: map[string]string{"environment.X": ""}, desired: map[string]string{"environment.X": ""}, wantNone: true},
		{name: "a sensitive key set empty and missing live: nothing, and nothing printed", current: map[string]string{},
			desired: map[string]string{"environment.DB_PASSWORD": ""}, wantNone: true},
		{name: "a sensitive key live with a value, desired empty: changed, value hidden", current: map[string]string{"environment.DB_PASSWORD": "hunter2"},
			desired: map[string]string{"environment.DB_PASSWORD": ""}, want: []string{"config.environment.DB_PASSWORD: (value hidden) changed"}},
		{name: "a secret-expanded key that expanded to nothing and is missing live: nothing", current: map[string]string{},
			desired: map[string]string{"environment.TOKEN_FILE": ""}, hidden: map[string]bool{"environment.TOKEN_FILE": true}, wantNone: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := diffConfig(tc.current, tc.desired, tc.hidden)
			if tc.wantNone {
				if len(got) != 0 {
					t.Errorf("changes = %q, want none", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("changes = %q, want %q", got, tc.want)
			}
		})
	}
}

// dropEmpty is what Incus does with a config it is sent: the keys with an empty value are not stored.
func dropEmpty(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// emptyDroppingInstance is the instance "app" on a server that stores no empty config value, as Incus does.
type emptyDroppingInstance struct{ *stateFake }

func (s *emptyDroppingInstance) UseProject(string) incus.InstanceServer { return s }
func (s *emptyDroppingInstance) UpdateInstance(n string, put api.InstancePut, etag string) (incus.Operation, error) {
	put.Config = dropEmpty(put.Config)
	return s.stateFake.UpdateInstance(n, put, etag)
}

func TestInstanceWithAnEmptyValueConverges(t *testing.T) {
	t.Run("an empty value beside a real change: one apply, then no changes", func(t *testing.T) {
		s := &emptyDroppingInstance{newStateFake("Running", "default")}
		r := appRes("default")
		r.Config = map[string]string{"environment.APP_PASSWORD": "", "environment.TZ": "Etc/UTC"}

		p, err := planOne(s, r, PlanOptions{})
		if err != nil || p.Action != ActionUpdate || len(p.Changes) != 1 || !strings.Contains(p.Changes[0], "environment.TZ") {
			t.Fatalf("first plan: %v %v %q", err, p.Action, p.Changes)
		}
		if err := updateOne(s, r, volumeEnv{}); err != nil {
			t.Fatal(err)
		}
		if _, held := s.inst.Config["environment.APP_PASSWORD"]; held {
			t.Fatalf("the fake stored the empty value: %v", s.inst.Config)
		}
		p, err = planOne(s, r, PlanOptions{})
		if err != nil || p.Action != ActionNone || len(p.Changes) != 0 {
			t.Errorf("second plan: %v %v %q, want no changes", err, p.Action, p.Changes)
		}
	})

	t.Run("only empty values: nothing to apply from the start", func(t *testing.T) {
		s := &emptyDroppingInstance{newStateFake("Running", "default")}
		r := appRes("default")
		r.Config = map[string]string{"environment.APP_PASSWORD": ""}
		p, err := planOne(s, r, PlanOptions{})
		if err != nil || p.Action != ActionNone {
			t.Errorf("plan: %v %v %q", err, p.Action, p.Changes)
		}
	})

	t.Run("a value changed to empty: planned as a change, applied, then no changes", func(t *testing.T) {
		s := &emptyDroppingInstance{newStateFake("Running", "default")}
		s.inst.Config = map[string]string{"environment.APP_PASSWORD": "old"}
		r := appRes("default")
		r.Config = map[string]string{"environment.APP_PASSWORD": ""}

		p, err := planOne(s, r, PlanOptions{})
		if err != nil || p.Action != ActionUpdate || len(p.Changes) != 1 {
			t.Fatalf("first plan: %v %v %q", err, p.Action, p.Changes)
		}
		if err := updateOne(s, r, volumeEnv{}); err != nil {
			t.Fatal(err)
		}
		p, err = planOne(s, r, PlanOptions{})
		if err != nil || p.Action != ActionNone {
			t.Errorf("second plan: %v %v %q, want no changes", err, p.Action, p.Changes)
		}
	})
}

// emptyDroppingProfile is one profile on a server that stores no empty config value.
type emptyDroppingProfile struct {
	incus.InstanceServer
	cfg map[string]string
}

func (s *emptyDroppingProfile) UseProject(string) incus.InstanceServer { return s }
func (s *emptyDroppingProfile) GetProfile(name string) (*api.Profile, string, error) {
	p := &api.Profile{Name: name}
	p.Config = s.cfg
	return p, "", nil
}
func (s *emptyDroppingProfile) UpdateProfile(_ string, put api.ProfilePut, _ string) error {
	s.cfg = dropEmpty(put.Config)
	return nil
}

func TestProfileWithAnEmptyValueConverges(t *testing.T) {
	s := &emptyDroppingProfile{cfg: map[string]string{}}
	r := Resource{Kind: KindProfile, Name: "pp", Config: map[string]string{"user.note": "", "limits.cpu": "1"}}

	p, err := planOne(s, r, PlanOptions{})
	if err != nil || p.Action != ActionUpdate || len(p.Changes) != 1 || !strings.Contains(p.Changes[0], "limits.cpu") {
		t.Fatalf("first plan: %v %v %q", err, p.Action, p.Changes)
	}
	if err := updateOne(s, r, volumeEnv{}); err != nil {
		t.Fatal(err)
	}
	if _, held := s.cfg["user.note"]; held {
		t.Fatalf("the fake stored the empty value: %v", s.cfg)
	}
	p, err = planOne(s, r, PlanOptions{})
	if err != nil || p.Action != ActionNone {
		t.Errorf("second plan: %v %v %q, want no changes", err, p.Action, p.Changes)
	}
}
