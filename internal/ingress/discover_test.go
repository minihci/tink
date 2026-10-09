package ingress

import (
	"reflect"
	"strings"
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
	// the old key names: still registered, and marked so the rename can be asked for
	want := []Registration{{Name: "ns-caddy", Domain: "ns.xlii.co", Port: "80", Address: "10.77.20.32", Legacy: true}}
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

func TestFilterAndResolve_CrossProjectRegistrationsBothSurvive(t *testing.T) {
	a := instanceFixture("ns-caddy", map[string]string{
		"user.ingress.enabled": "true",
		"user.ingress.domain":  "ns.xlii.co",
	}, "10.77.20.32")
	a.Project = "default"
	b := instanceFixture("ns-caddy", map[string]string{
		"user.ingress.enabled": "true",
		"user.ingress.domain":  "ns-staging.xlii.co",
	}, "10.135.20.32")
	b.Project = "nightscout"

	regs, warnings := filterAndResolve([]api.InstanceFull{a, b})

	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	want := []Registration{
		{Name: "ns-caddy", Project: "default", Domain: "ns.xlii.co", Port: "80", Address: "10.77.20.32", Legacy: true},
		{Name: "ns-caddy", Project: "nightscout", Domain: "ns-staging.xlii.co", Port: "80", Address: "10.135.20.32", Legacy: true},
	}
	if !reflect.DeepEqual(regs, want) {
		t.Fatalf("got %+v, want %+v", regs, want)
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

func TestFilterAndResolve_TheTinkNamespaceIsTheCurrentName(t *testing.T) {
	regs, warnings := filterAndResolve([]api.InstanceFull{instanceFixture("app", map[string]string{
		KeyEnabled: "true", KeyDomain: "app.example.com", KeyPort: "8080",
	}, "10.0.0.5")})
	want := []Registration{{Name: "app", Domain: "app.example.com", Port: "8080", Address: "10.0.0.5"}}
	if len(warnings) != 0 || !reflect.DeepEqual(regs, want) {
		t.Fatalf("got %+v %v, want %+v", regs, warnings, want)
	}
}

func TestFilterAndResolve_TheNewNameWinsPerKeyAndAMixIsStillLegacy(t *testing.T) {
	regs, _ := filterAndResolve([]api.InstanceFull{instanceFixture("app", map[string]string{
		KeyEnabled:            "true",
		KeyDomain:             "new.example.com",
		"user.ingress.domain": "old.example.com", // both: the new name wins
		"user.ingress.port":   "9000",            // only the old: still read
	}, "10.0.0.5")})
	want := []Registration{{Name: "app", Domain: "new.example.com", Port: "9000", Address: "10.0.0.5", Legacy: true}}
	if !reflect.DeepEqual(regs, want) {
		t.Fatalf("got %+v, want %+v", regs, want)
	}

	// the old name alone switches nothing off: this is what keeps a route from being deleted when tink is upgraded first
	regs, _ = filterAndResolve([]api.InstanceFull{instanceFixture("ns-caddy", map[string]string{
		"user.ingress.enabled": "true", "user.ingress.domain": "ns.xlii.co",
	}, "10.77.20.32")})
	if len(regs) != 1 {
		t.Fatalf("an instance registered under the old names must stay registered: %+v", regs)
	}
	// and an instance that switched the new name off is off, whatever the old one says
	regs, _ = filterAndResolve([]api.InstanceFull{instanceFixture("app", map[string]string{
		KeyEnabled: "false", "user.ingress.enabled": "true", "user.ingress.domain": "x.example.com",
	}, "10.0.0.5")})
	if len(regs) != 0 {
		t.Fatalf("the new name wins, including when it says no: %+v", regs)
	}
}

func TestLegacyNamesAndNotice(t *testing.T) {
	names := legacyNames([]Registration{
		{Name: "a", Legacy: true}, {Name: "b"}, {Name: "c", Project: "nightscout", Legacy: true},
	})
	if !reflect.DeepEqual(names, []string{"a", "nightscout/c"}) {
		t.Fatalf("%v", names)
	}
	n := LegacyNotice(names)
	if !strings.Contains(n, "2 instance(s)") || !strings.Contains(n, "user.tink.ingress.*") || !strings.Contains(n, "a, nightscout/c") {
		t.Errorf("%s", n)
	}
}

func TestFilterAndResolve_ANoAgentVMSaysWhereItIs(t *testing.T) {
	// Home Assistant OS as it is on the lab host: a VM whose eth0 Incus cannot read, so nothing is discovered
	haos := instanceFixture("haos", map[string]string{
		KeyEnabled: "true", KeyDomain: "ha.example.com", KeyPort: "8123", KeyAddress: "10.0.142.176",
	}) // no address in its state
	regs, warnings := filterAndResolve([]api.InstanceFull{haos})
	want := []Registration{{Name: "haos", Domain: "ha.example.com", Port: "8123", Address: "10.0.142.176"}}
	if len(warnings) != 0 || !reflect.DeepEqual(regs, want) {
		t.Fatalf("got %+v %v, want %+v", regs, warnings, want)
	}
	// and without the key it is still skipped, as it always was
	noKey := instanceFixture("haos", map[string]string{KeyEnabled: "true", KeyDomain: "ha.example.com", KeyPort: "8123"})
	if regs, warnings := filterAndResolve([]api.InstanceFull{noKey}); len(regs) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "no address yet") {
		t.Fatalf("%+v %v", regs, warnings)
	}
}

func TestFilterAndResolve_TheAddressTheInstanceSetsBeatsTheOneIncusReports(t *testing.T) {
	inst := instanceFixture("app", map[string]string{KeyEnabled: "true", KeyDomain: "a.example.com", KeyAddress: "  app.internal  "}, "10.0.0.5")
	regs, _ := filterAndResolve([]api.InstanceFull{inst})
	if len(regs) != 1 || regs[0].Address != "app.internal" {
		t.Fatalf("a host name is accepted, and trimmed: %+v", regs)
	}
	// an empty value is no value: discovery applies
	blank := instanceFixture("app", map[string]string{KeyEnabled: "true", KeyDomain: "a.example.com", KeyAddress: "   "}, "10.0.0.5")
	if regs, _ := filterAndResolve([]api.InstanceFull{blank}); len(regs) != 1 || regs[0].Address != "10.0.0.5" {
		t.Fatalf("%+v", regs)
	}
	v6 := instanceFixture("app", map[string]string{KeyEnabled: "true", KeyDomain: "a.example.com", KeyPort: "8123", KeyAddress: "fd00::5"})
	regs, _ = filterAndResolve([]api.InstanceFull{v6})
	files, _ := Render(regs)
	if len(regs) != 1 || regs[0].Address != "[fd00::5]" || !strings.Contains(files["app.caddy"], "reverse_proxy http://[fd00::5]:8123 {") {
		t.Fatalf("an IPv6 address needs its brackets in a URL: %+v\n%s", regs, files["app.caddy"])
	}
}

func TestFilterAndResolve_AnAddressThatIsNotAHostIsSkippedNotGuessedAt(t *testing.T) {
	for name, bad := range map[string]string{
		"a scheme":         "http://10.0.0.5",
		"a port":           "10.0.0.5:8123",
		"a path":           "10.0.0.5/ha",
		"a space":          "10.0.0.5 evil.example.com",
		"a brace":          "x}",
		"a newline":        "10.0.0.5\nrespond \"owned\"",
		"a leading hyphen": "-bad",
		"an underscore":    "my_host",
	} {
		inst := instanceFixture("app", map[string]string{KeyEnabled: "true", KeyDomain: "a.example.com", KeyAddress: bad}, "10.0.0.5")
		regs, warnings := filterAndResolve([]api.InstanceFull{inst})
		if len(regs) != 0 {
			t.Errorf("%s (%q): must not fall back to the discovered address or pass the value on: %+v", name, bad, regs)
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], KeyAddress) || !strings.Contains(warnings[0], "app") {
			t.Errorf("%s: say which key of which instance: %v", name, warnings)
		}
	}
}

func TestFilterAndResolve_ABadAddressStillCountsInADomainConflict(t *testing.T) {
	a := instanceFixture("a", map[string]string{KeyEnabled: "true", KeyDomain: "x.example.com", KeyAddress: "http://nope"}, "10.0.0.1")
	b := instanceFixture("b", map[string]string{KeyEnabled: "true", KeyDomain: "x.example.com"}, "10.0.0.2")
	regs, warnings := filterAndResolve([]api.InstanceFull{a, b})
	if len(regs) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0], "claimed by multiple") {
		t.Fatalf("a contested domain is contested whatever else is wrong with a claimant: %+v %v", regs, warnings)
	}
}
