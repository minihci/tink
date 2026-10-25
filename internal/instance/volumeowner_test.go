package instance

import "testing"

// A volume the engine creates takes the config the Spec carries (the owner `tink run` works out, from --user or from the image's user), and a volume
// that exists is left as it is. Moved here with the engine from internal/dockerrun (#216).

func TestOnlyNewlyCreatedVolumesGetTheOwner(t *testing.T) {
	spec := diskOnPool()
	spec.VolumeConfig = map[string]string{"initial.uid": "1000"}
	f := &readFake{volErr: errNotFound}
	if err := ensureManagedVolumes(f, spec); err != nil {
		t.Fatal(err)
	}
	if len(f.createdConfig) != 1 || f.createdConfig[0]["initial.uid"] != "1000" {
		t.Errorf("a new volume is created with the owner: %v", f.createdConfig)
	}
	f = &readFake{} // exists
	if err := ensureManagedVolumes(f, spec); err != nil || len(f.created) != 0 {
		t.Errorf("an existing volume is not touched: %v %v", f.created, err)
	}
	// and the callers that set no VolumeConfig (apply, helper) create exactly what they always did
	f = &readFake{volErr: errNotFound}
	if err := ensureManagedVolumes(f, diskOnPool()); err != nil || len(f.createdConfig) != 1 || f.createdConfig[0] != nil {
		t.Errorf("no VolumeConfig, no config: %v %v", f.createdConfig, err)
	}
}

// A tmpfs or unix-char device has no pool, so the engine does not try to create a managed volume for it (the devices tink run writes for
// --incus-device 'cache type=disk source=tmpfs: ...' and a zigbee adapter).
func TestDevicesWithNoPoolAreNotManagedVolumes(t *testing.T) {
	spec := &Spec{Devices: map[string]map[string]string{
		"cache":  {"type": "disk", "source": "tmpfs:", "path": "/cache", "size": "64MiB"},
		"zigbee": {"type": "unix-char", "source": "/dev/ttyUSB0", "path": "/dev/ttyUSB0", "required": "false"},
	}}
	f := &readFake{volErr: errNotFound}
	if err := ensureManagedVolumes(f, spec); err != nil || len(f.created) != 0 {
		t.Errorf("tmpfs and unix-char devices are not volumes: %v %v", f.created, err)
	}
}
