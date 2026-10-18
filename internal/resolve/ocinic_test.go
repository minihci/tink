package resolve

import (
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

var ociTestRemotes = map[string]cliconfig.Remote{
	"docker-oci": {Protocol: "oci"},
	"ghcr":       {Protocol: "oci"},
	"images":     {Protocol: "simplestreams"},
}

func nicInstance(image string, devices map[string]map[string]string, profiles ...string) Resource {
	return Resource{Kind: KindInstance, Name: "ha", Image: image, Devices: devices, Profiles: profiles}
}

func TestOCINICWarnings(t *testing.T) {
	cidr := map[string]string{"type": "nic", "nictype": "bridged", "parent": "br0", "ipv4.address": "192.0.2.246/24"}
	named := map[string]string{"type": "nic", "nictype": "bridged", "parent": "br0", "ipv4.address": "192.0.2.246/24", "name": "eth1"}
	cases := []struct {
		name  string
		r     Resource
		wants []string // substrings, one per expected warning; nil = none
	}{
		{"unnamed NIC with a CIDR on an OCI image", nicInstance("ghcr:home-assistant/home-assistant:2026.9.4", map[string]map[string]string{"lan": cidr}),
			[]string{`NIC device "lan" sets ipv4.address but has no name:`}},
		{"the same NIC with a name", nicInstance("ghcr:home-assistant/home-assistant:2026.9.4", map[string]map[string]string{"lan": named}), nil},
		{"unnamed NIC with a gateway of none", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"lan": {"type": "nic", "ipv4.gateway": "none"}}),
			[]string{"sets ipv4.gateway but has no name:"}},
		{"unnamed NIC that disables DHCPv4", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"eth0": {"type": "nic", "network": "svc", "ipv4.address": "none"}}),
			[]string{"sets ipv4.address but has no name:"}},
		{"unnamed NIC that disables DHCPv6", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"eth0": {"type": "nic", "ipv6.address": "none"}}),
			[]string{"sets ipv6.address but has no name:"}},
		{"several settings on one NIC give one warning listing them", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"lan": {"type": "nic", "ipv4.address": "10.0.0.5/24", "ipv4.gateway": "none"}}),
			[]string{"sets ipv4.address, ipv4.gateway but has no name:"}},
		{"a bare address is a DHCP reservation, dynamic either way", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"eth0": {"type": "nic", "network": "svc", "ipv4.address": "10.10.20.5"}}), nil},
		{"an unnamed NIC with no static setting behaves the same named or not", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"eth0": {"type": "nic", "network": "svc"}}), nil},
		{"a system container image is not an OCI container", nicInstance("images:debian/12", map[string]map[string]string{"lan": cidr}), nil},
		{"a local alias is not an OCI container", nicInstance("haos-x86-64-18.3", map[string]map[string]string{"lan": cidr}), nil},
		{"a disk device is ignored", nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"data": {"type": "disk", "ipv4.address": "10.0.0.5/24"}}), nil},
		{"not an instance", Resource{Kind: KindProfile, Name: "p", Devices: map[string]map[string]string{"lan": cidr}}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ociNICWarnings(c.r, ociTestRemotes, nil)
			if len(got) != len(c.wants) {
				t.Fatalf("got %d warnings %q, want %d", len(got), got, len(c.wants))
			}
			for i, want := range c.wants {
				if !strings.Contains(got[i], want) {
					t.Errorf("warning %d = %q, want it to contain %q", i, got[i], want)
				}
			}
		})
	}
}

func TestOCINICWarnings_UnknownRemotesMeansNoGuess(t *testing.T) {
	r := nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{"lan": {"type": "nic", "ipv4.address": "10.0.0.5/24"}})
	if got := ociNICWarnings(r, nil, nil); len(got) != 0 {
		t.Errorf("with no remotes known the image cannot be called OCI, got %q", got)
	}
}

func TestOCINICWarnings_ProfileDevices(t *testing.T) {
	unnamed := map[string]string{"type": "nic", "network": "svc", "ipv4.gateway": "none"}
	profiles := map[string]Resource{
		"base": {Kind: KindProfile, Name: "base", Devices: map[string]map[string]string{"eth0": unnamed}},
	}
	// a profile the stack declares is seen, and the warning names it
	got := ociNICWarnings(nicInstance("docker-oci:x:1", nil, "base"), ociTestRemotes, profiles)
	if len(got) != 1 || !strings.Contains(got[0], `(from profile "base")`) {
		t.Fatalf("got %q, want one warning naming the profile", got)
	}
	// the instance's own device of the same name replaces the profile's WHOLE device
	named := map[string]string{"type": "nic", "network": "svc", "name": "eth0"}
	if got := ociNICWarnings(nicInstance("docker-oci:x:1", map[string]map[string]string{"eth0": named}, "base"), ociTestRemotes, profiles); len(got) != 0 {
		t.Errorf("an instance override with a name must silence the profile's unnamed device, got %q", got)
	}
	// a profile that is not in the stack cannot be seen and is not guessed at
	if got := ociNICWarnings(nicInstance("docker-oci:x:1", nil, "default"), ociTestRemotes, profiles); len(got) != 0 {
		t.Errorf("an undeclared profile must be skipped, got %q", got)
	}
	// a later profile's device of the same name replaces an earlier one
	profiles["named"] = Resource{Kind: KindProfile, Name: "named", Devices: map[string]map[string]string{"eth0": named}}
	if got := ociNICWarnings(nicInstance("docker-oci:x:1", nil, "base", "named"), ociTestRemotes, profiles); len(got) != 0 {
		t.Errorf("the later profile's named device wins, got %q", got)
	}
}

func TestPlanOptions_ProfilesComeFromTheFullStack(t *testing.T) {
	stack := []Resource{
		{Kind: KindProfile, Name: "base"},
		{Kind: KindInstance, Name: "ha", Profiles: []string{"base"}},
	}
	if got := (PlanOptions{}).ForResources(stack).profiles; len(got) != 1 || got["base"].Name != "base" {
		t.Errorf("ForResources profiles = %v", got)
	}
	if got := (PlanOptions{}).withTargets(stack).profiles; len(got) != 1 {
		t.Errorf("withTargets profiles = %v", got)
	}
}

func TestPlanOne_UnnamedOCINICWarnsBeforeTheInstanceExists(t *testing.T) {
	r := nicInstance("docker-oci:eclipse-mosquitto:2", map[string]map[string]string{
		"lan": {"type": "nic", "nictype": "bridged", "parent": "br0", "ipv4.address": "192.0.2.246/24"},
	})
	stack := []Resource{r}
	opts := PlanOptions{env: &imageEnv{offline: true, conf: &cliconfig.Config{Remotes: ociTestRemotes}}}.ForResources(stack)
	p, err := planOne(&lookupServer{err: errNotFound}, r, opts)
	if err != nil {
		t.Fatalf("planOne() error = %v", err)
	}
	if p.Action != ActionCreate {
		t.Fatalf("action = %v, want ActionCreate", p.Action)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "ipv4.address") || !strings.Contains(p.Warnings[0], "docs/oci-network.md") {
		t.Errorf("warnings = %q, want the unnamed-NIC warning pointing at the doc", p.Warnings)
	}
}
