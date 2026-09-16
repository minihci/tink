package ingress

import (
	"reflect"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

func instanceFixture(name string, config map[string]string, addresses ...string) api.InstanceFull {
	inst := api.InstanceFull{}
	inst.Name = name
	inst.Config = config
	inst.State = &api.InstanceState{
		Network: map[string]api.InstanceStateNetwork{},
	}
	if len(addresses) > 0 {
		var addrs []api.InstanceStateNetworkAddress
		for _, a := range addresses {
			addrs = append(addrs, api.InstanceStateNetworkAddress{Family: "inet", Address: a})
		}
		inst.State.Network["eth0"] = api.InstanceStateNetwork{Addresses: addrs}
	}
	return inst
}

func TestFilterAndResolve_BasicRegistration(t *testing.T) {
	instances := []api.InstanceFull{
		instanceFixture("ns-caddy", map[string]string{
			"user.ingress.enabled": "true",
			"user.ingress.domain":  "ns.xlii.co",
			"user.ingress.port":    "80",
		}, "10.77.20.32"),
	}

	regs, warnings := filterAndResolve(instances)

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	want := []Registration{{Name: "ns-caddy", Domain: "ns.xlii.co", Port: "80", Address: "10.77.20.32"}}
	if !reflect.DeepEqual(regs, want) {
		t.Fatalf("got %+v, want %+v", regs, want)
	}
}

func TestFilterAndResolve_DefaultsPortTo80(t *testing.T) {
	instances := []api.InstanceFull{
		instanceFixture("ns-caddy", map[string]string{
			"user.ingress.enabled": "true",
			"user.ingress.domain":  "ns.xlii.co",
		}, "10.77.20.32"),
	}

	regs, _ := filterAndResolve(instances)
	if len(regs) != 1 || regs[0].Port != "80" {
		t.Fatalf("expected port to default to 80, got %+v", regs)
	}
}

func TestFilterAndResolve_SkipsUnopted(t *testing.T) {
	instances := []api.InstanceFull{
		instanceFixture("ns-mongo", map[string]string{}, "10.77.20.30"),
		instanceFixture("ns-app", map[string]string{
			"user.ingress.domain": "ns.xlii.co", // enabled missing -> not registered
		}, "10.77.20.31"),
		instanceFixture("authelia", map[string]string{
			"user.ingress.enabled": "true", // domain missing -> not registered
		}, "10.77.20.20"),
	}

	regs, warnings := filterAndResolve(instances)
	if len(regs) != 0 || len(warnings) != 0 {
		t.Fatalf("expected nothing registered, got regs=%+v warnings=%v", regs, warnings)
	}
}

func TestFilterAndResolve_ConflictSkipsBothClaimants(t *testing.T) {
	instances := []api.InstanceFull{
		instanceFixture("a", map[string]string{
			"user.ingress.enabled": "true",
			"user.ingress.domain":  "shared.xlii.co",
		}, "10.77.20.1"),
		instanceFixture("b", map[string]string{
			"user.ingress.enabled": "true",
			"user.ingress.domain":  "shared.xlii.co",
		}, "10.77.20.2"),
	}

	regs, warnings := filterAndResolve(instances)
	if len(regs) != 0 {
		t.Fatalf("expected no registrations from a conflicted domain, got %+v", regs)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one conflict warning, got %v", warnings)
	}
}

func TestFilterAndResolve_NoAddressYetIsSkippedWithWarning(t *testing.T) {
	instances := []api.InstanceFull{
		instanceFixture("ns-caddy", map[string]string{
			"user.ingress.enabled": "true",
			"user.ingress.domain":  "ns.xlii.co",
		} /* no addresses */),
	}

	regs, warnings := filterAndResolve(instances)
	if len(regs) != 0 {
		t.Fatalf("expected no registration without an address, got %+v", regs)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected exactly one no-address warning, got %v", warnings)
	}
}
