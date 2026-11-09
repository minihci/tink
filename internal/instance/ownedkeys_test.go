package instance

import (
	"reflect"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func TestStaleOwnedKeysAreTheLiveOnesUnderAnOwnedPrefixThatAreNotDeclared(t *testing.T) {
	live := map[string]string{
		"user.tink.intent.publish.a": "1", "user.tink.intent.publish.b": "2", "user.tink.intent.publish.kept": "3",
		"user.tink.role": "x", "user.tink.intent.publisher": "x", "user.tink.intent.publish": "x", "user.other": "x", "limits.cpu": "2",
	}
	declared := map[string]string{"user.tink.intent.publish.kept": "3", "user.tink.role": "y"}
	cases := []struct {
		name     string
		prefixes []string
		want     []string
	}{
		{"one owned prefix", []string{"user.tink.intent.publish."}, []string{"user.tink.intent.publish.a", "user.tink.intent.publish.b"}},
		{"no owned prefix owns nothing", nil, nil},
		{"an empty prefix owns nothing (it would own everything)", []string{""}, nil},
		{"a name with no trailing dot owns that key and nothing built from it", []string{"user.tink.intent.publish"}, []string{"user.tink.intent.publish"}},
		{"two prefixes", []string{"user.tink.intent.publish.", "user."}, []string{"user.other", "user.tink.intent.publish", "user.tink.intent.publish.a", "user.tink.intent.publish.b", "user.tink.intent.publisher"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StaleOwnedKeys(live, declared, c.prefixes)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("stale = %v, want %v", got, c.want)
			}
		})
	}
}

type configFake struct {
	incus.InstanceServer
	live    map[string]string
	updated *api.InstancePut
}

func (f *configFake) GetInstance(name string) (*api.Instance, string, error) {
	cfg := map[string]string{}
	for k, v := range f.live {
		cfg[k] = v
	}
	return &api.Instance{Name: name, InstancePut: api.InstancePut{Config: cfg}}, "etag", nil
}

func (f *configFake) UpdateInstance(_ string, put api.InstancePut, _ string) (incus.Operation, error) {
	f.updated = &put
	return doneOp{}, nil
}

func TestApplyConfigRemovesOnlyTheStaleKeysOfAnOwnedPrefix(t *testing.T) {
	live := map[string]string{"user.tink.intent.publish.a": "1", "user.tink.intent.publish.kept": "old", "user.tink.role": "x", "limits.cpu": "2"}
	spec := &Spec{Name: "web", Config: map[string]string{"user.tink.intent.publish.kept": "new", "user.tink.intent.publish.fresh": "n"}, OwnedConfigPrefixes: []string{"user.tink.intent.publish."}}
	f := &configFake{live: live}
	if err := ApplyConfig(f, spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"user.tink.intent.publish.kept": "new", "user.tink.intent.publish.fresh": "n", "user.tink.role": "x", "limits.cpu": "2"}
	if !reflect.DeepEqual(map[string]string(f.updated.Config), want) {
		t.Errorf("config = %v, want %v", f.updated.Config, want)
	}
}

// Without an owned prefix ApplyConfig only sets: that is every other caller, and what "tink never removes a key" means.
func TestApplyConfigWithNoOwnedPrefixRemovesNothing(t *testing.T) {
	live := map[string]string{"user.tink.intent.publish.a": "1", "limits.cpu": "2"}
	f := &configFake{live: live}
	if err := ApplyConfig(f, &Spec{Name: "web", Config: map[string]string{"x": "y"}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"user.tink.intent.publish.a": "1", "limits.cpu": "2", "x": "y"}
	if !reflect.DeepEqual(map[string]string(f.updated.Config), want) {
		t.Errorf("config = %v, want %v", f.updated.Config, want)
	}
}

type doneOp struct{ incus.Operation }

func (doneOp) Wait() error { return nil }

type deviceFake struct {
	configFake
	devices api.DevicesMap
	puts    int
}

func (f *deviceFake) GetInstance(name string) (*api.Instance, string, error) {
	inst, etag, _ := f.configFake.GetInstance(name)
	inst.Devices = api.DevicesMap{}
	for n, d := range f.devices {
		inst.Devices[n] = d
	}
	return inst, etag, nil
}

func (f *deviceFake) UpdateInstance(n string, put api.InstancePut, etag string) (incus.Operation, error) {
	f.puts++
	return f.configFake.UpdateInstance(n, put, etag)
}

// What the caller says was applied and is no longer declared is taken back in the same PUT that sets the declaration, from the instance as it was read, and
// everything else the instance carries (its volatile.* keys above all: a PUT replaces the whole config) goes back as it was.
func TestApplyConfigTakesBackWhatStaleAppliedNamesInTheSamePut(t *testing.T) {
	f := &deviceFake{
		configFake: configFake{live: map[string]string{"limits.cpu": "2", "limits.memory": "1GiB", "user.by-hand": "h", "volatile.eth0.hwaddr": "00:16:3e:00:00:01"}},
		devices:    api.DevicesMap{"eth0": {"type": "nic"}, "old": {"type": "nic"}, "by-hand": {"type": "usb"}},
	}
	var asked int
	spec := &Spec{
		Name: "web", Config: map[string]string{"limits.cpu": "4"}, Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "br0"}},
		StaleApplied: func(config map[string]string, devices map[string]map[string]string) ([]string, []string) {
			asked++
			if config["limits.memory"] != "1GiB" || devices["old"] == nil {
				t.Errorf("asked about %v %v, want the instance as it was read", config, devices)
			}
			return []string{"limits.memory"}, []string{"old"}
		},
	}
	if err := ApplyConfig(f, spec); err != nil {
		t.Fatal(err)
	}
	if f.puts != 1 || asked != 1 {
		t.Errorf("%d PUTs, asked %d times", f.puts, asked)
	}
	wantConfig := map[string]string{"limits.cpu": "4", "user.by-hand": "h", "volatile.eth0.hwaddr": "00:16:3e:00:00:01"}
	if !reflect.DeepEqual(map[string]string(f.updated.Config), wantConfig) {
		t.Errorf("config = %v, want %v", f.updated.Config, wantConfig)
	}
	wantDevices := api.DevicesMap{"eth0": {"type": "nic", "network": "br0"}, "by-hand": {"type": "usb"}}
	if !reflect.DeepEqual(f.updated.Devices, wantDevices) {
		t.Errorf("devices = %v, want %v", f.updated.Devices, wantDevices)
	}
}
