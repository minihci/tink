package resolve

import "testing"

// planIncus itself (like Create/pushFile/runIncus elsewhere in this
// package) is deliberately not unit-tested: it shells out to the real
// `incus` binary, which this repo's own CI runner doesn't have
// installed at all -- confirmed live the hard way (a version of this
// file assumed a bare client binary would always be present, without
// even a daemon, and that broke CI). No Incus dependency at all is what
// earns real test coverage in this package; see flags_test.go's own
// package doc for the same reasoning applied to tink run's Build().

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
