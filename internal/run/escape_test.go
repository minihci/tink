package run

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func TestParseUserTakesNumbersAndRefusesNames(t *testing.T) {
	for in, want := range map[string][2]string{"1000": {"1000", ""}, "1000:1000": {"1000", "1000"}, "0:0": {"0", "0"}, "33:0": {"33", "0"}} {
		uid, gid, err := parseUser(in)
		if err != nil || uid != want[0] || gid != want[1] {
			t.Errorf("%q -> %q %q %v, want %v", in, uid, gid, err, want)
		}
	}
	for _, in := range []string{"node", "root", "1000:dialout", ":1000", "-1", "1000:", "1.5"} {
		_, _, err := parseUser(in)
		if err == nil {
			t.Errorf("%q must be refused: a name cannot be looked up without the image's /etc/passwd", in)
			continue
		}
		if !strings.Contains(err.Error(), "numeric UID[:GID]") {
			t.Errorf("%q: the error should say what is accepted: %v", in, err)
		}
	}
}

func TestUserSetsTheProcessAndTheOwnerOfNewVolumes(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", User: "1000:1001", Volume: []string{"data:/data"}, Pool: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Config["oci.uid"] != "1000" || spec.Config["oci.gid"] != "1001" {
		t.Errorf("config = %v", spec.Config)
	}
	if want := map[string]string{"initial.uid": "1000", "initial.gid": "1001"}; !reflect.DeepEqual(spec.VolumeConfig, want) {
		t.Errorf("VolumeConfig = %v, want %v", spec.VolumeConfig, want)
	}
	// a user with no group leaves the group alone, in the process and in the volume
	spec, _ = Build(Options{Name: "n", Image: "i", User: "1000"})
	if _, ok := spec.Config["oci.gid"]; ok || spec.VolumeConfig["initial.gid"] != "" {
		t.Errorf("no group given, none set: %v %v", spec.Config, spec.VolumeConfig)
	}
	// and without --user nothing about ownership is said
	spec, _ = Build(Options{Name: "n", Image: "i"})
	if spec.VolumeConfig != nil || spec.Config["oci.uid"] != "" {
		t.Errorf("no --user: %v %v", spec.Config, spec.VolumeConfig)
	}
}

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

func TestVolumeLinesSayWhoOwnsIt(t *testing.T) {
	spec := diskOnPool()
	spec.VolumeConfig = map[string]string{"initial.uid": "1000", "initial.gid": "1001"}
	if got := describeVolumes(&readFake{volErr: errNotFound}, spec); len(got) != 1 || !strings.Contains(got[0], "owned by 1000:1001") {
		t.Errorf("new: %v", got)
	}
	got := describeVolumes(&readFake{}, spec)
	if len(got) != 1 || !strings.Contains(got[0], "ownership is not changed") || !strings.Contains(got[0], "chown it to 1000:1001") {
		t.Errorf("existing: %v", got)
	}
}

func TestIncusConfigAddsAndNeverOverrides(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", IncusConfig: []string{"limits.memory=512MiB", "security.nesting=true", "linux.sysctl.net.ipv4.ip_forward=1"}})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Config["limits.memory"] != "512MiB" || spec.Config["linux.sysctl.net.ipv4.ip_forward"] != "1" {
		t.Errorf("config = %v", spec.Config)
	}
	for name, c := range map[string]struct {
		opts Options
		want string
	}{
		"-e":          {Options{Env: []string{"A=b"}, IncusConfig: []string{"environment.A=c"}}, "set by -e"},
		"--restart":   {Options{Restart: "always", IncusConfig: []string{"boot.autorestart=false"}}, "set by --restart"},
		"--user":      {Options{User: "1000", IncusConfig: []string{"oci.uid=0"}}, "set by --user"},
		"the command": {Options{Cmd: []string{"x"}, IncusConfig: []string{"oci.entrypoint=y"}}, "the command after the image"},
		"twice":       {Options{IncusConfig: []string{"limits.cpu=1", "limits.cpu=2"}}, "already set"},
		"volatile":    {Options{IncusConfig: []string{"volatile.uuid=x"}}, "belong to Incus"},
		"the stamp":   {Options{IncusConfig: []string{KeyCommand + "=x"}}, "records the command"},
		"no equals":   {Options{IncusConfig: []string{"limits.cpu"}}, "KEY=VALUE"},
		"empty key":   {Options{IncusConfig: []string{"=1"}}, "KEY=VALUE"},
	} {
		c.opts.Name, c.opts.Image = "n", "i"
		if _, err := Build(c.opts); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, c.want)
		}
	}
}

func TestIncusDeviceParsesLikeIncusConfigDeviceAdd(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", IncusDevice: []string{
		"cache type=disk source=tmpfs: path=/cache size=64MiB",
		"zigbee type=unix-char source=/dev/ttyUSB0 path=/dev/ttyUSB0 required=false",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"type": "disk", "source": "tmpfs:", "path": "/cache", "size": "64MiB"}
	if !reflect.DeepEqual(spec.Devices["cache"], want) {
		t.Errorf("cache = %v, want %v", spec.Devices["cache"], want)
	}
	if spec.Devices["zigbee"]["required"] != "false" {
		t.Errorf("zigbee = %v", spec.Devices["zigbee"])
	}
	// a tmpfs source has no pool, so tink does not try to create a managed volume for it
	f := &readFake{volErr: errNotFound}
	if err := ensureManagedVolumes(f, spec); err != nil || len(f.created) != 0 {
		t.Errorf("tmpfs and unix-char devices are not volumes: %v %v", f.created, err)
	}
}

func TestIncusDeviceErrorsSayWhatIsWrong(t *testing.T) {
	for name, c := range map[string]struct {
		opts Options
		want string
	}{
		"no name":       {Options{IncusDevice: []string{"type=disk path=/x"}}, "device's name first"},
		"empty":         {Options{IncusDevice: []string{"  "}}, "device's name first"},
		"not key=value": {Options{IncusDevice: []string{"d type=disk readonly"}}, "not key=value"},
		"no type":       {Options{IncusDevice: []string{"d path=/x source=/y"}}, "type=TYPE is required"},
		"repeated key":  {Options{IncusDevice: []string{"d type=disk type=nic"}}, "given twice"},
		"repeated name": {Options{IncusDevice: []string{"d type=nic network=a", "d type=nic network=b"}}, "already used"},
		"a flag's name": {Options{Network: "incusbr0", IncusDevice: []string{"eth0 type=nic network=b"}}, "already used"},
		"a port's name": {Options{Publish: []string{"80:80"}, IncusDevice: []string{"proxy0 type=proxy listen=tcp:0.0.0.0:81 connect=tcp:127.0.0.1:81"}}, "already used"},
	} {
		c.opts.Name, c.opts.Image = "n", "i"
		if _, err := Build(c.opts); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one containing %q", name, err, c.want)
		}
	}
}

func TestAnIncusDeviceCanBeTheNIC(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", Publish: []string{"80:80"}, IncusDevice: []string{"lan type=nic nictype=bridged parent=br0"}})
	if err != nil {
		t.Fatal(err)
	}
	f := &profileFake{profiles: map[string]map[string]map[string]string{"default": rootOnly}}
	if w, err := checkNetwork(f, spec); err != nil || w != "" {
		t.Errorf("a NIC given as an --incus-device satisfies the check: %q %v", w, err)
	}
}

func TestTheRecordedCommandCarriesTheNewFlagsAndHidesSecrets(t *testing.T) {
	got := CommandLine(Options{
		Name: "n", Image: "i", User: "1000:1000",
		IncusConfig: []string{"limits.memory=512MiB", "environment.DB_PASSWORD=hunter2"},
		IncusDevice: []string{"cache type=disk source=tmpfs: path=/cache"},
	})
	want := `tink run --name n --user 1000:1000 --incus-config limits.memory=512MiB --incus-config 'environment.DB_PASSWORD=***' --incus-device 'cache type=disk source=tmpfs: path=/cache' i`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "hunter2") {
		t.Error("a secret-looking --incus-config value must never be recorded")
	}
}

// removeFake is a server on which applying config fails (the instance cannot be read back), and that records whether the instance was deleted.
type removeFake struct {
	incus.InstanceServer
	deleteErr error
	deleted   []string
}

func (f *removeFake) GetInstance(string) (*api.Instance, string, error) {
	return nil, "", fmt.Errorf("device validation failed")
}

func (f *removeFake) DeleteInstance(name string) (incus.Operation, error) {
	if f.deleteErr != nil {
		return nil, f.deleteErr
	}
	f.deleted = append(f.deleted, name)
	return doneOp{}, nil
}

type doneOp struct{ incus.Operation }

func (doneOp) Wait() error { return nil }

func TestAFailedConfigRemovesTheInstanceItJustMade(t *testing.T) {
	f := &removeFake{}
	err := configureOrRemove(f, &Spec{Name: "abs-play"})
	if err == nil || !strings.Contains(err.Error(), "device validation failed") || !strings.Contains(err.Error(), "abs-play was removed again") {
		t.Errorf("err = %v", err)
	}
	if !reflect.DeepEqual(f.deleted, []string{"abs-play"}) {
		t.Errorf("deleted = %v", f.deleted)
	}
}

func TestIfTheInstanceCannotBeRemovedBothFailuresAreReported(t *testing.T) {
	f := &removeFake{deleteErr: fmt.Errorf("busy")}
	err := configureOrRemove(f, &Spec{Name: "abs-play"})
	if err == nil || !strings.Contains(err.Error(), "device validation failed") || !strings.Contains(err.Error(), "could not be removed: busy") {
		t.Errorf("err = %v", err)
	}
}
