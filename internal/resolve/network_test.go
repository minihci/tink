package resolve

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// netServer is a fake Incus server for the network planner and applier. Methods a network has no business calling panic (nil embedded
// interface), and every write is recorded, so a test can prove that a refused change never reached the server.
type netServer struct {
	incus.InstanceServer
	live    *api.Network // nil: Incus answers 404
	err     error
	created []api.NetworksPost
	updated []api.NetworkPut
	etag    string
}

func (s *netServer) GetNetwork(string) (*api.Network, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	if s.live == nil {
		return nil, "", errNotFound
	}
	return s.live, "etag-1", nil
}

func (s *netServer) CreateNetwork(p api.NetworksPost) error {
	s.created = append(s.created, p)
	return nil
}

func (s *netServer) UpdateNetwork(_ string, put api.NetworkPut, etag string) error {
	s.updated = append(s.updated, put)
	s.etag = etag
	return nil
}

func liveBridge(config map[string]string, usedBy ...string) *api.Network {
	return &api.Network{
		NetworkPut: api.NetworkPut{Config: config},
		Name:       "svc",
		Type:       "bridge",
		Managed:    true,
		Status:     "Created",
		UsedBy:     usedBy,
	}
}

func netResource(config map[string]string) Resource {
	return Resource{Kind: KindNetwork, Name: "svc", Config: config}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "tink.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFile_Network(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, `
kind: network
name: svc
description: the service bridge
allow_disruption: true
config:
  ipv4.address: 10.10.20.1/24
  ipv4.nat: "true"
  ipv6.address: none
`))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d resources, want 1", len(rs))
	}
	r := rs[0]
	if r.Kind != KindNetwork || r.Name != "svc" || r.Description != "the service bridge" || !r.AllowDisruption {
		t.Errorf("resource = %+v", r)
	}
	if got := r.Config["ipv4.address"]; got != "10.10.20.1/24" {
		t.Errorf("ipv4.address = %q", got)
	}
}

func TestLoadFile_NetworkRejectedForms(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"project", "kind: network\nname: svc\nproject: ha-eval\n", "server-global"},
		{"name too long", "kind: network\nname: a-bridge-name-too-long\n", "at most 15"},
		{"name has a slash", "kind: network\nname: a/b\n", "letters, digits"},
		{"pool is not a network field", "kind: network\nname: svc\npool: default\n", `does not use field "Pool"`},
		{"image is not a network field", "kind: network\nname: svc\nimage: images:debian/12\n", `does not use field "Image"`},
		{"unknown field", "kind: network\nname: svc\ntype: bridge\n", "not found in type"},
		{"description on another kind", "kind: profile\nname: p\ndescription: nope\n", `does not use field "Description"`},
		{"allow_disruption on another kind", "kind: profile\nname: p\nallow_disruption: true\n", `does not use field "AllowDisruption"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadFile(writeTemp(t, c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestLoadFile_NetworkMayNotHideASecret(t *testing.T) {
	_, err := LoadFile(writeTemp(t, "kind: network\nname: svc\nconfig:\n  raw.dnsmasq: ${secret:x}\n"))
	if err == nil {
		t.Fatal("a ${secret:} reference outside an instance's environment must be rejected, as for every other kind")
	}
}

func TestLevels_NICOnADeclaredNetworkWaitsForIt(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "svc"}}},
		{Kind: KindNetwork, Name: "svc"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if !inLevel(levels, 0, "svc") || !inLevel(levels, 1, "ha") {
		t.Errorf("want the network in level 0 and the instance in level 1, got %v", levels)
	}
}

func TestLevels_ProfileNICOnADeclaredNetworkWaitsForItAndSoDoesItsInstance(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "ha", Profiles: []string{"base"}},
		{Kind: KindProfile, Name: "base", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "svc"}}},
		{Kind: KindNetwork, Name: "svc"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if !inLevel(levels, 0, "svc") || !inLevel(levels, 1, "base") || !inLevel(levels, 2, "ha") {
		t.Errorf("want network, profile, instance in levels 0, 1, 2, got %v", levels)
	}
}

func TestLevels_NICOnANetworkTheStackDoesNotDeclareStaysLenient(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("a NIC on a network that already exists on the server is normal, got error %v", err)
	}
	if !inLevel(levels, 0, "ha") {
		t.Errorf("want the instance in level 0, got %v", levels)
	}
}

func TestPlanNetwork_NotFoundCreates(t *testing.T) {
	p, err := planNetwork(&netServer{}, netResource(map[string]string{"ipv4.address": "10.10.20.1/24"}))
	if err != nil || p.Action != ActionCreate {
		t.Fatalf("planNetwork() = %v, %v; want ActionCreate", p.Action, err)
	}
}

func TestPlanNetwork_ForbiddenIsNotAbsent(t *testing.T) {
	p, err := planNetwork(&netServer{err: errForbidden}, netResource(nil))
	if err == nil {
		t.Fatalf("a 403 must be an error, never read as 'the network is absent' (got action %v)", p.Action)
	}
}

func TestPlanNetwork_ExtraLiveKeysAreNotDrift(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.address": "10.10.20.1/24", "ipv4.nat": "true", "ipv6.address": "none", "volatile.network.ipv4.address": "10.10.20.1"})
	p, err := planNetwork(&netServer{live: live}, netResource(map[string]string{"ipv4.address": "10.10.20.1/24", "ipv4.nat": "true"}))
	if err != nil || p.Action != ActionNone {
		t.Fatalf("planNetwork() = %v (%v), %v; want ActionNone", p.Action, p.Changes, err)
	}
}

func TestPlanNetwork_AutoMatchesTheAddressIncusPicked(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.address": "10.77.1.1/24", "ipv6.address": "fd42:1:2:3::1/64"})
	p, err := planNetwork(&netServer{live: live}, netResource(map[string]string{"ipv4.address": "auto", "ipv6.address": "auto"}))
	if err != nil || p.Action != ActionNone {
		t.Fatalf("planNetwork() = %v (%v), %v; want ActionNone, auto must accept the generated address", p.Action, p.Changes, err)
	}
}

func TestPlanNetwork_AutoIsWantedWhenTheNetworkHasNoAddress(t *testing.T) {
	live := liveBridge(map[string]string{"ipv6.address": "none"})
	p, _ := planNetwork(&netServer{live: live}, netResource(map[string]string{"ipv6.address": "auto"}))
	if p.Action != ActionUpdate || len(p.Changes) != 1 {
		t.Fatalf("a network with ipv6 'none' asked for 'auto' must plan an update, got %v %v", p.Action, p.Changes)
	}
}

func TestPlanNetwork_DifferenceOnAnUnusedBridgeUpdates(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"})
	p, _ := planNetwork(&netServer{live: live}, netResource(map[string]string{"ipv4.nat": "false"}))
	if p.Action != ActionUpdate {
		t.Fatalf("action = %v, want ActionUpdate", p.Action)
	}
	if want := []string{`config.ipv4.nat: "true" -> "false"`}; !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("changes = %v, want %v", p.Changes, want)
	}
	if len(p.Warnings) != 0 {
		t.Errorf("an unused bridge needs no disruption warning, got %v", p.Warnings)
	}
}

func TestPlanNetwork_DifferenceOnABridgeInUseIsBlockedAndNamesTheInstances(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"},
		"/1.0/instances/mosquitto",
		"/1.0/instances/ha?project=ha-eval",
		"/1.0/profiles/default?project=ha-eval", // not an instance: not counted
	)
	p, _ := planNetwork(&netServer{live: live}, netResource(map[string]string{"ipv4.nat": "false"}))
	if p.Action != ActionBlocked {
		t.Fatalf("action = %v, want ActionBlocked", p.Action)
	}
	if len(p.Changes) != 1 {
		t.Errorf("a blocked plan still shows what it would change, got %v", p.Changes)
	}
	why := strings.Join(p.Blocked, " ")
	for _, want := range []string{"2 instance(s)", "ha-eval/ha", "mosquitto", "allow_disruption: true"} {
		if !strings.Contains(why, want) {
			t.Errorf("blocked reason %q does not mention %q", why, want)
		}
	}
}

func TestPlanNetwork_AllowDisruptionAppliesAndWarns(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"}, "/1.0/instances/mosquitto")
	r := netResource(map[string]string{"ipv4.nat": "false"})
	r.AllowDisruption = true
	p, _ := planNetwork(&netServer{live: live}, r)
	if p.Action != ActionUpdate {
		t.Fatalf("action = %v, want ActionUpdate", p.Action)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "mosquitto") {
		t.Errorf("warnings = %v, want one naming the affected instance", p.Warnings)
	}
}

func TestPlanNetwork_DescriptionOnlyChangeIsNeverBlocked(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"}, "/1.0/instances/mosquitto")
	r := netResource(map[string]string{"ipv4.nat": "true"})
	r.Description = "the service bridge"
	p, _ := planNetwork(&netServer{live: live}, r)
	if p.Action != ActionUpdate || len(p.Warnings) != 0 {
		t.Fatalf("a description change on a bridge in use is harmless: action %v warnings %v", p.Action, p.Warnings)
	}
}

func TestPlanNetwork_RefusesWhatItDoesNotOwn(t *testing.T) {
	cases := []struct {
		name string
		live api.Network
		want string
	}{
		{"unmanaged host interface", api.Network{Name: "svc", Type: "bridge", Managed: false}, "does not manage it"},
		{"not a bridge", api.Network{Name: "svc", Type: "ovn", Managed: true, Status: "Created"}, `type "ovn"`},
		{"not created", api.Network{Name: "svc", Type: "bridge", Managed: true, Status: "Errored"}, `status "Errored"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			live := c.live
			p, err := planNetwork(&netServer{live: &live}, netResource(map[string]string{"ipv4.nat": "true"}))
			if err != nil || p.Action != ActionBlocked {
				t.Fatalf("planNetwork() = %v, %v; want ActionBlocked", p.Action, err)
			}
			if !strings.Contains(strings.Join(p.Blocked, " "), c.want) {
				t.Errorf("blocked = %v, want it to mention %q", p.Blocked, c.want)
			}
		})
	}
}

func TestNetworkInstances(t *testing.T) {
	got := networkInstances([]string{
		"/1.0/instances/b",
		"/1.0/instances/a?project=default",
		"/1.0/instances/c?project=lab",
		"/1.0/profiles/p",
		"/1.0/networks/other",
		"%zz",
	})
	if want := []string{"a", "b", "lab/c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("networkInstances() = %v, want %v", got, want)
	}
}

func TestCreateOne_NetworkCreatesABridge(t *testing.T) {
	s := &netServer{}
	r := netResource(map[string]string{"ipv4.address": "10.10.20.1/24"})
	r.Description = "the service bridge"
	if err := createOne(s, r, volumeEnv{}); err != nil {
		t.Fatalf("createOne() error = %v", err)
	}
	if len(s.created) != 1 {
		t.Fatalf("CreateNetwork called %d times, want 1", len(s.created))
	}
	got := s.created[0]
	if got.Name != "svc" || got.Type != "bridge" || got.Description != "the service bridge" || got.Config["ipv4.address"] != "10.10.20.1/24" {
		t.Errorf("CreateNetwork got %+v", got)
	}
}

func TestUpdateOne_NetworkMergesDeclaredKeysOnly(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.address": "10.77.1.1/24", "ipv4.nat": "true", "volatile.x": "keep"})
	s := &netServer{live: live}
	r := netResource(map[string]string{"ipv4.address": "auto", "ipv4.nat": "false"})
	if err := updateOne(s, r, volumeEnv{}); err != nil {
		t.Fatalf("updateOne() error = %v", err)
	}
	if len(s.updated) != 1 {
		t.Fatalf("UpdateNetwork called %d times, want 1", len(s.updated))
	}
	cfg := s.updated[0].Config
	if cfg["ipv4.nat"] != "false" {
		t.Errorf("ipv4.nat = %q, want the declared false", cfg["ipv4.nat"])
	}
	if cfg["ipv4.address"] != "10.77.1.1/24" {
		t.Errorf("ipv4.address = %q: 'auto' must never overwrite the address Incus picked (it would move every instance)", cfg["ipv4.address"])
	}
	if cfg["volatile.x"] != "keep" {
		t.Errorf("a key tink did not declare was dropped: %v", cfg)
	}
	if s.etag != "etag-1" {
		t.Errorf("etag = %q, want the one read with the network", s.etag)
	}
}

func TestApplyOne_BlockedNetworkChangeNeverReachesTheServer(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"}, "/1.0/instances/mosquitto")
	s := &netServer{live: live}
	var notes []string
	out, err := applyOne(s, netResource(map[string]string{"ipv4.nat": "false"}), PlanOptions{}, func(f string, a ...any) {
		notes = append(notes, strings.TrimSpace(fmt.Sprintf(f, a...)))
	})
	if err != nil || out != outBlocked {
		t.Fatalf("applyOne() = %v, %v; want outBlocked", out, err)
	}
	if len(s.updated) != 0 || len(s.created) != 0 {
		t.Errorf("a blocked change wrote to the server: %d updates, %d creates", len(s.updated), len(s.created))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "BLOCKED") {
		t.Errorf("notes = %v, want one BLOCKED line", notes)
	}
}
