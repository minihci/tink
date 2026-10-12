package resolve

import (
	"reflect"
	"testing"
)

func TestUndeclaredExtras_NothingExtraWhenTheStackSaysAllOfIt(t *testing.T) {
	res := Resource{
		Kind: KindInstance, Name: "app", Profiles: []string{"default"},
		Config:  map[string]string{"limits.memory": "256MiB", "boot.autostart": "true"},
		Devices: map[string]map[string]string{"eth0": {"type": "nic"}},
	}
	if e := undeclaredExtras(res, res); !e.Empty() || e.Count() != 0 {
		t.Errorf("an instance equal to its declaration has no extras, got %+v", e)
	}
}

func TestUndeclaredExtras_ReportsWhatTheLiveInstanceHasAndTheStackDoesNot(t *testing.T) {
	live := Resource{
		Config: map[string]string{"limits.cpu": "2", "limits.memory": "2GiB", "boot.autostart": "true", "environment.TZ": "UTC"},
		Devices: map[string]map[string]string{
			"eth0":   {"type": "nic"},
			"config": {"type": "disk"},
			"zigbee": {"type": "unix-char"},
		},
	}
	declared := Resource{
		Config:  map[string]string{"boot.autostart": "true", "environment.TZ": "UTC"},
		Devices: map[string]map[string]string{"eth0": {"type": "nic"}},
	}
	e := undeclaredExtras(live, declared)
	if want := []string{"config (disk)", "zigbee (unix-char)"}; !reflect.DeepEqual(e.Devices, want) {
		t.Errorf("Devices = %v, want %v", e.Devices, want)
	}
	if want := []string{"limits.cpu", "limits.memory"}; !reflect.DeepEqual(e.Config, want) {
		t.Errorf("Config = %v, want %v (sorted, names only)", e.Config, want)
	}
	if e.Empty() || e.Count() != 4 {
		t.Errorf("Count = %d, want 4", e.Count())
	}
}

func TestUndeclaredExtras_AKeyTheStackDeclaresIsNeverAnExtraWhateverItsValue(t *testing.T) {
	// a different value is a change plan already reports; the extras report is only about keys and devices the stack does not mention
	live := Resource{Config: map[string]string{"limits.memory": "4GiB"}, Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "a"}}}
	declared := Resource{Config: map[string]string{"limits.memory": "2GiB"}, Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "b"}}}
	if e := undeclaredExtras(live, declared); !e.Empty() {
		t.Errorf("got %+v", e)
	}
}

func TestUndeclaredExtras_TinksOwnStateIsNotAnExtra(t *testing.T) {
	live := Resource{Config: map[string]string{
		"user.tink.helper.status": "{...}", "user.tink.run.command": "tink run ...", "user.tink.exec.restart-app.trigger-hash": "abc",
		"user.tink.ingress.domain": "x.example.com",
	}}
	declared := Resource{}
	e := undeclaredExtras(live, declared)
	// the ingress keys are declared by a person (they decide a route), so they are not tink's state
	if want := []string{"user.tink.ingress.domain"}; !reflect.DeepEqual(e.Config, want) {
		t.Errorf("Config = %v, want %v", e.Config, want)
	}
}

func TestUndeclaredExtras_Profiles(t *testing.T) {
	t.Run("both directions", func(t *testing.T) {
		e := undeclaredExtras(Resource{Profiles: []string{"default", "lan"}}, Resource{Profiles: []string{"default", "internal"}})
		if !reflect.DeepEqual(e.LiveProfiles, []string{"lan"}) || !reflect.DeepEqual(e.MissingProfiles, []string{"internal"}) {
			t.Errorf("got live-only %v, missing %v", e.LiveProfiles, e.MissingProfiles)
		}
	})
	t.Run("a stack that lists no profiles compares nothing", func(t *testing.T) {
		if e := undeclaredExtras(Resource{Profiles: []string{"default", "lan"}}, Resource{}); !e.Empty() {
			t.Errorf("got %+v", e)
		}
	})
}

func TestUndeclaredExtras_IsDeterministic(t *testing.T) {
	live := Resource{Config: map[string]string{"z": "1", "a": "1", "m": "1"}, Devices: map[string]map[string]string{"z": {"type": "disk"}, "a": {"type": "disk"}}}
	for i := 0; i < 20; i++ {
		e := undeclaredExtras(live, Resource{})
		if !reflect.DeepEqual(e.Config, []string{"a", "m", "z"}) || !reflect.DeepEqual(e.Devices, []string{"a (disk)", "z (disk)"}) {
			t.Fatalf("run %d: %+v", i, e)
		}
	}
}
