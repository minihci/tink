package resolve

import "testing"

func TestPlanIncus_ActionNoneWhenCheckSucceeds(t *testing.T) {
	// "incus version" always exits zero and needs no live daemon.
	plan, err := planIncus(Resource{Name: "x", Check: []string{"version"}, Command: []string{"list"}})
	if err != nil {
		t.Fatalf("planIncus() error = %v", err)
	}
	if plan.Action != ActionNone {
		t.Errorf("Action = %v, want ActionNone when check exits zero", plan.Action)
	}
}

func TestPlanIncus_ActionCreateWhenCheckFails(t *testing.T) {
	// An unrecognized incus subcommand always exits non-zero.
	plan, err := planIncus(Resource{Name: "x", Check: []string{"not-a-real-subcommand"}, Command: []string{"version"}})
	if err != nil {
		t.Fatalf("planIncus() error = %v", err)
	}
	if plan.Action != ActionCreate {
		t.Errorf("Action = %v, want ActionCreate when check exits non-zero", plan.Action)
	}
	if len(plan.Changes) == 0 {
		t.Error("Changes is empty, want a note explaining why the check failed")
	}
}

func TestDiffConfig_ReportsMissingKey(t *testing.T) {
	changes := diffConfig(map[string]string{}, map[string]string{"limits.cpu": "1"})
	if len(changes) != 1 {
		t.Fatalf("diffConfig() = %v, want exactly one change", changes)
	}
}

func TestDiffConfig_ReportsDifferentValue(t *testing.T) {
	changes := diffConfig(map[string]string{"limits.cpu": "1"}, map[string]string{"limits.cpu": "2"})
	if len(changes) != 1 {
		t.Fatalf("diffConfig() = %v, want exactly one change", changes)
	}
}

func TestDiffConfig_NoChangeWhenValueMatches(t *testing.T) {
	changes := diffConfig(map[string]string{"limits.cpu": "1"}, map[string]string{"limits.cpu": "1"})
	if len(changes) != 0 {
		t.Errorf("diffConfig() = %v, want no changes", changes)
	}
}

// One-directional by design: current carries plenty of keys resolve
// doesn't own (image.*, volatile.*) and should never report those as
// drift just because desired doesn't mention them.
func TestDiffConfig_IgnoresExtraKeysOnlyInCurrent(t *testing.T) {
	changes := diffConfig(map[string]string{"volatile.uuid": "abc", "limits.cpu": "1"}, map[string]string{"limits.cpu": "1"})
	if len(changes) != 0 {
		t.Errorf("diffConfig() = %v, want no changes (current-only keys must not count as drift)", changes)
	}
}

func TestDiffDevices_ReportsMissingDevice(t *testing.T) {
	changes := diffDevices(map[string]map[string]string{}, map[string]map[string]string{
		"eth0": {"type": "nic", "network": "incusbr0"},
	})
	if len(changes) != 1 {
		t.Fatalf("diffDevices() = %v, want exactly one change", changes)
	}
}

func TestDiffDevices_NoChangeWhenIdentical(t *testing.T) {
	dev := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}
	changes := diffDevices(dev, dev)
	if len(changes) != 0 {
		t.Errorf("diffDevices() = %v, want no changes", changes)
	}
}

// Devices merge as whole blocks by name, not field-by-field (confirmed
// against real Incus behavior building tink run) -- a device differing
// in even one field must be reported as a full replacement, not
// partially matched.
func TestDiffDevices_ReportsChangeOnPartialFieldDifference(t *testing.T) {
	current := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0", "ipv4.address": "10.0.0.1"}}
	desired := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0", "ipv4.address": "10.0.0.2"}}
	changes := diffDevices(current, desired)
	if len(changes) != 1 {
		t.Fatalf("diffDevices() = %v, want exactly one change", changes)
	}
}

func TestDiffDevices_IgnoresExtraDeviceOnlyInCurrent(t *testing.T) {
	current := map[string]map[string]string{
		"eth0": {"type": "nic", "network": "incusbr0"},
		"eth1": {"type": "nic", "network": "incusbr1"},
	}
	desired := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}
	changes := diffDevices(current, desired)
	if len(changes) != 0 {
		t.Errorf("diffDevices() = %v, want no changes (current-only devices must not count as drift)", changes)
	}
}
