package run

import (
	"reflect"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func TestStaleOwnedKeysAreTheLiveOnesUnderAnOwnedPrefixThatAreNotDeclared(t *testing.T) {
	live := map[string]string{
		"user.tink.publish.a": "1", "user.tink.publish.b": "2", "user.tink.publish.kept": "3",
		"user.tink.role": "x", "user.tink.publisher": "x", "user.tink.publish": "x", "user.other": "x", "limits.cpu": "2",
	}
	declared := map[string]string{"user.tink.publish.kept": "3", "user.tink.role": "y"}
	cases := []struct {
		name     string
		prefixes []string
		want     []string
	}{
		{"one owned prefix", []string{"user.tink.publish."}, []string{"user.tink.publish.a", "user.tink.publish.b"}},
		{"no owned prefix owns nothing", nil, nil},
		{"an empty prefix owns nothing (it would own everything)", []string{""}, nil},
		{"a name with no trailing dot owns that key and nothing built from it", []string{"user.tink.publish"}, []string{"user.tink.publish"}},
		{"two prefixes", []string{"user.tink.publish.", "user."}, []string{"user.other", "user.tink.publish", "user.tink.publish.a", "user.tink.publish.b", "user.tink.publisher"}},
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
	live := map[string]string{"user.tink.publish.a": "1", "user.tink.publish.kept": "old", "user.tink.role": "x", "limits.cpu": "2"}
	spec := &Spec{Name: "web", Config: map[string]string{"user.tink.publish.kept": "new", "user.tink.publish.fresh": "n"}, OwnedConfigPrefixes: []string{"user.tink.publish."}}
	f := &configFake{live: live}
	if err := ApplyConfig(f, spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"user.tink.publish.kept": "new", "user.tink.publish.fresh": "n", "user.tink.role": "x", "limits.cpu": "2"}
	if !reflect.DeepEqual(map[string]string(f.updated.Config), want) {
		t.Errorf("config = %v, want %v", f.updated.Config, want)
	}
}

// Without an owned prefix ApplyConfig only sets: that is every other caller, and what "tink never removes a key" means.
func TestApplyConfigWithNoOwnedPrefixRemovesNothing(t *testing.T) {
	live := map[string]string{"user.tink.publish.a": "1", "limits.cpu": "2"}
	f := &configFake{live: live}
	if err := ApplyConfig(f, &Spec{Name: "web", Config: map[string]string{"x": "y"}}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"user.tink.publish.a": "1", "limits.cpu": "2", "x": "y"}
	if !reflect.DeepEqual(map[string]string(f.updated.Config), want) {
		t.Errorf("config = %v, want %v", f.updated.Config, want)
	}
}
