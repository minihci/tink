package run

import (
	"reflect"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// createFake is a server Create can run against: it resolves a local image and records the creation requests it is sent. Any other call panics
// through the nil embedded interface.
type createFake struct {
	readFake
	projects []string
	posted   []api.InstancesPost
}

func (f *createFake) UseProject(p string) incus.InstanceServer {
	f.projects = append(f.projects, p)
	return f
}

func (f *createFake) CreateInstanceFromImage(_ incus.ImageServer, _ api.Image, req api.InstancesPost) (incus.RemoteOperation, error) {
	f.posted = append(f.posted, req)
	return createdOp{}, nil
}

type createdOp struct{ incus.RemoteOperation }

func (createdOp) Wait() error { return nil }

var (
	rootDisk = map[string]string{"type": "disk", "path": "/", "pool": "default"}
	dataDisk = map[string]string{"type": "disk", "pool": "default", "source": "vol1", "path": "/data"}
)

func TestCreateSendsTheRootDiskOnlyWhenAskedTo(t *testing.T) {
	cases := []struct {
		name    string
		devices map[string]map[string]string
		atStart bool
		want    api.DevicesMap // what the creation request carries
	}{
		{"not asked: nothing but the profiles goes, however the spec is",
			map[string]map[string]string{"root": rootDisk, "data": dataDisk}, false, nil},
		{"asked: the root disk goes, and the volume disk waits for ApplyConfig",
			map[string]map[string]string{"root": rootDisk, "data": dataDisk}, true, api.DevicesMap{"root": rootDisk}},
		{"a root disk under any device name",
			map[string]map[string]string{"system": rootDisk}, true, api.DevicesMap{"system": rootDisk}},
		{"asked, but the spec has no root disk: nothing goes",
			map[string]map[string]string{"data": dataDisk}, true, nil},
		{"asked, but the spec has no devices at all",
			nil, true, nil},
		{"a disk at / that has a source is a mount, not the root disk, as Incus counts it",
			map[string]map[string]string{"host": {"type": "disk", "path": "/", "source": "/srv/root"}}, true, nil},
		{"a device at / that is not a disk is not a root disk",
			map[string]map[string]string{"root": {"type": "none", "path": "/"}}, true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir()) // no client configuration: no remotes, so the image is a local one
			f := &createFake{readFake: readFake{aliasErr: errNotFound}}
			spec := &Spec{Name: "web", Image: "local-image", Profiles: []string{"default", "extra"}, Ephemeral: true, VM: true, Devices: c.devices, RootDiskAtCreate: c.atStart}

			if err := Create(f, spec, "tink"); err != nil {
				t.Fatal(err)
			}
			if len(f.posted) != 1 {
				t.Fatalf("%d creation requests, want 1", len(f.posted))
			}
			got := f.posted[0]
			if !reflect.DeepEqual(got.Devices, c.want) {
				t.Errorf("the request's devices = %v, want %v", got.Devices, c.want)
			}
			// what was always sent still is
			if got.Name != "web" || got.Type != api.InstanceTypeVM || !got.Ephemeral || !reflect.DeepEqual(got.Profiles, []string{"default", "extra"}) {
				t.Errorf("name, type, ephemeral flag and profiles must be unchanged: %+v", got)
			}
			if len(f.projects) != 1 || f.projects[0] != "tink" {
				t.Errorf("the request must go to the instance's project: %v", f.projects)
			}
		})
	}
}

// ApplyConfig applies the root disk again, and that has to change nothing: the instance already has it.
func TestRootDiskSentAtCreationIsTheSameDeviceApplyConfigSets(t *testing.T) {
	spec := &Spec{Devices: map[string]map[string]string{"root": rootDisk, "data": dataDisk}, RootDiskAtCreate: true}
	sent := creationDevices(spec)["root"]
	if !reflect.DeepEqual(sent, spec.Devices["root"]) {
		t.Errorf("sent %v, ApplyConfig will set %v", sent, spec.Devices["root"])
	}
}

func TestHasRootDisk(t *testing.T) {
	cases := []struct {
		name    string
		devices map[string]map[string]string
		want    bool
	}{
		{"no devices", nil, false},
		{"an empty default profile", map[string]map[string]string{}, false},
		{"a NIC only", map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}, false},
		{"the root disk", map[string]map[string]string{"root": rootDisk}, true},
		{"a root disk under another name, on another pool", map[string]map[string]string{"system": {"type": "disk", "path": "/", "pool": "fast"}}, true},
		{"a root disk among other devices", map[string]map[string]string{"eth0": {"type": "nic"}, "root": rootDisk, "data": dataDisk}, true},
		{"a disk at another path", map[string]map[string]string{"data": dataDisk}, false},
		{"a disk with no path", map[string]map[string]string{"data": {"type": "disk", "pool": "default"}}, false},
		{"a disk at / with a source is a mount", map[string]map[string]string{"host": {"type": "disk", "path": "/", "source": "/srv/root"}}, false},
		{"a device at / that is not a disk", map[string]map[string]string{"root": {"type": "none", "path": "/"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasRootDisk(c.devices); got != c.want {
				t.Errorf("HasRootDisk(%v) = %v, want %v", c.devices, got, c.want)
			}
		})
	}
}
