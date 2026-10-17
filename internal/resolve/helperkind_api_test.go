package resolve

import (
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

// A helper that reaches the host's API through a proxy device is told in the plan, as a note: a restricted project must allow proxy devices.
// A helper that has none (it goes over the network) is not.
func TestPlanHelperNotesAProxyDeviceAndSaysNothingAboutNone(t *testing.T) {
	withProxy := helperInstance(t, "tink-helper", "helper", "Running", goodStatus())
	withProxy.Devices = map[string]map[string]string{
		"root": {"type": "disk", "path": "/", "pool": "default"},
		"api":  {"type": "proxy", "bind": "container", "listen": "tcp:127.0.0.1:8443", "connect": "tcp:127.0.0.1:8443"},
	}
	p, err := planHelper(&helperKindServer{instances: []api.Instance{withProxy}}, helperRes(nil), helperNow)
	if err != nil {
		t.Fatal(err)
	}
	n := joined(p.Notes)
	if !strings.Contains(n, "reaches the host's API through a proxy device") || !strings.Contains(n, "restricted project must allow proxy devices") || !strings.Contains(n, "--api-via network") {
		t.Errorf("notes = %q", n)
	}
	if p.Action != ActionNone || len(p.Warnings) != 0 || len(p.Blocked) != 0 {
		t.Errorf("a note, never a change or a warning: %+v", p)
	}

	overNetwork := helperInstance(t, "tink-helper", "helper", "Running", goodStatus())
	overNetwork.Devices = map[string]map[string]string{
		"root": {"type": "disk", "path": "/", "pool": "default"},
		"eth0": {"type": "nic", "network": "tink0", "name": "eth0"},
	}
	p, err = planHelper(&helperKindServer{instances: []api.Instance{overNetwork}}, helperRes(nil), helperNow)
	if err != nil {
		t.Fatal(err)
	}
	if n := joined(p.Notes); strings.Contains(n, "proxy") {
		t.Errorf("a helper with no proxy device is not told about one: %q", n)
	}
}
