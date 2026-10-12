package resolve

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtrasDescribe(t *testing.T) {
	t.Run("nothing to say", func(t *testing.T) {
		if got := (Extras{}).Describe(); got != "" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("devices, config keys and profiles, names only", func(t *testing.T) {
		got := Extras{
			Devices: []string{"config (disk)"}, Config: []string{"limits.cpu"},
			LiveProfiles: []string{"lan"}, MissingProfiles: []string{"internal"},
		}.Describe()
		for _, want := range []string{
			"not in the stack (left alone): ", "device.config (disk)", "config.limits.cpu",
			"profile lan (on the instance, not in profiles:)", "profile internal (in profiles:, not on the instance)",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("%q lacks %q", got, want)
			}
		}
	})
	t.Run("a long list is cut and says how many more", func(t *testing.T) {
		var keys []string
		for i := 0; i < 14; i++ {
			keys = append(keys, "k"+string(rune('a'+i)))
		}
		got := Extras{Config: keys}.Describe()
		if !strings.Contains(got, "and 4 more") || strings.Contains(got, "config.kn") {
			t.Errorf("got %q", got)
		}
	})
	t.Run("a partial comparison says so", func(t *testing.T) {
		got := Extras{Config: []string{"boot.autorestart"}, Partial: true}.Describe()
		if !strings.Contains(got, "environment.* and oci.* not compared with the image") {
			t.Errorf("got %q", got)
		}
	})
}

func TestDropPrefixed(t *testing.T) {
	got := dropPrefixed([]string{"boot.autorestart", "environment.TZ", "oci.uid", "limits.cpu"}, "environment.", "oci.")
	if want := []string{"boot.autorestart", "limits.cpu"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// the instance as exportServer holds it: an OCI instance with boot.autorestart, environment.TZ and DB_PASSWORD, and eth0, proxy0 and volume0.
func kumaDeclared() Resource {
	return Resource{
		Kind: KindInstance, Name: "kuma-play", Project: "tink-play", Image: "docker-oci:louislam/uptime-kuma:2",
		Config:  map[string]string{"environment.TZ": "America/Denver", "environment.DB_PASSWORD": "hunter2"},
		Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}},
	}
}

func TestPlanWithExtrasListsWhatTheStackDoesNotDeclareAndChangesNothing(t *testing.T) {
	s := &exportServer{inst: kumaInstance(), volCfg: map[string]string{}}
	r := kumaDeclared()
	without, err := planInstance(s, r, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if without.Extras != nil {
		t.Errorf("extras are only computed when asked for, got %+v", without.Extras)
	}
	with, err := planInstance(s, r, NewPlanOptions(true).WithExtras())
	if err != nil {
		t.Fatal(err)
	}
	if with.Extras == nil {
		t.Fatal("WithExtras should report on an instance that exists")
	}
	// informational only: the action, changes, drift and blocks are exactly what they were
	if with.Action != without.Action || !reflect.DeepEqual(with.Changes, without.Changes) || !reflect.DeepEqual(with.Blocked, without.Blocked) || !reflect.DeepEqual(with.Drift, without.Drift) {
		t.Errorf("reporting extras must not change the plan: %+v vs %+v", with, without)
	}
	e := *with.Extras
	if want := []string{"proxy0 (proxy)", "volume0 (disk)"}; !reflect.DeepEqual(e.Devices, want) {
		t.Errorf("Devices = %v, want %v", e.Devices, want)
	}
	if want := []string{"boot.autorestart"}; !reflect.DeepEqual(e.Config, want) {
		t.Errorf("Config = %v, want %v (Incus's and tink's own keys are not extras; DB_PASSWORD is declared)", e.Config, want)
	}
	if !e.Partial {
		t.Error("offline, an OCI instance's oci.* and environment.* cannot be compared with the image, and the report must say so")
	}
	if strings.Contains(e.Describe(), "hunter2") {
		t.Error("a value must never be printed")
	}
}

func TestPlanWithExtrasKeepsEnvironmentKeysForAnInstanceThatIsNotFromAnImage(t *testing.T) {
	inst := kumaInstance()
	delete(inst.Config, "volatile.container.oci") // a system container: nothing about its environment.* came from an image
	s := &exportServer{inst: inst, volCfg: map[string]string{}}
	r := kumaDeclared()
	delete(r.Config, "environment.TZ")
	p, err := planInstance(s, r, NewPlanOptions(true).WithExtras())
	if err != nil {
		t.Fatal(err)
	}
	if p.Extras == nil || p.Extras.Partial {
		t.Fatalf("a non-OCI instance has nothing to compare with an image, so the report is complete: %+v", p.Extras)
	}
	found := false
	for _, k := range p.Extras.Config {
		found = found || k == "environment.TZ"
	}
	if !found {
		t.Errorf("environment.TZ is the person's here and undeclared, so it is an extra: %v", p.Extras.Config)
	}
}

func TestUndeclaredExtrasForAnInstanceThatIsNotThere(t *testing.T) {
	s := &exportServer{inst: kumaInstance(), volCfg: map[string]string{}}
	r := kumaDeclared()
	r.Name = "not-deployed"
	if _, found, err := UndeclaredExtras(s, r, true); err != nil || found {
		t.Errorf("found = %v, err = %v: a stack not yet applied has no extras", found, err)
	}
	r.Name = "kuma-play"
	if e, found, err := UndeclaredExtras(s, r, true); err != nil || !found || e.Empty() {
		t.Errorf("found = %v, err = %v, extras = %+v", found, err, e)
	}
}
