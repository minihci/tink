package resolve

import (
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

// kindsServer holds one live object of each kind other than an instance, or none (nil: Incus answers 404).
type kindsServer struct {
	incus.InstanceServer
	profile *api.Profile
	project *api.Project
	network *api.Network
	volume  *api.StorageVolume
}

func (s *kindsServer) UseProject(string) incus.InstanceServer { return s }

func (s *kindsServer) GetProfile(string) (*api.Profile, string, error) {
	if s.profile == nil {
		return nil, "", errNotFound
	}
	return s.profile, "", nil
}

func (s *kindsServer) GetProject(string) (*api.Project, string, error) {
	if s.project == nil {
		return nil, "", errNotFound
	}
	return s.project, "", nil
}

func (s *kindsServer) GetNetwork(string) (*api.Network, string, error) {
	if s.network == nil {
		return nil, "", errNotFound
	}
	return s.network, "", nil
}

func (s *kindsServer) GetStoragePoolVolume(string, string, string) (*api.StorageVolume, string, error) {
	if s.volume == nil {
		return nil, "", errNotFound
	}
	return s.volume, "", nil
}

func planExtrasOf(t *testing.T, s *kindsServer, r Resource) PlannedResource {
	t.Helper()
	p, err := planKind(s, r, NewPlanOptions(true).WithExtras())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The case the report exists for: a key was dropped from the stack, and the object still carries it.
func TestPlanSaysWhatAProfileKeepsThatTheStackNoLongerDeclares(t *testing.T) {
	live := &api.Profile{Name: "web", ProfilePut: api.ProfilePut{
		Config:  map[string]string{"limits.cpu": "2", "limits.memory": "1GiB"},
		Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "br0"}, "scratch": {"type": "disk", "path": "/scratch", "source": "/srv"}},
	}}
	// the stack used to declare limits.cpu and the scratch device too; now it declares limits.memory and eth0 only
	r := Resource{Kind: KindProfile, Name: "web",
		Config:  map[string]string{"limits.memory": "1GiB"},
		Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "br0"}}}
	s := &kindsServer{profile: live}

	p := planExtrasOf(t, s, r)
	if p.Action != ActionNone || len(p.Changes) != 0 {
		t.Errorf("extras are informational: action = %v, changes = %v", p.Action, p.Changes)
	}
	if p.Extras == nil {
		t.Fatal("the profile still carries limits.cpu and a device the stack does not declare, and plan must say so")
	}
	if want := []string{"limits.cpu"}; !reflect.DeepEqual(p.Extras.Config, want) {
		t.Errorf("Config = %v, want %v", p.Extras.Config, want)
	}
	if want := []string{"scratch (disk)"}; !reflect.DeepEqual(p.Extras.Devices, want) {
		t.Errorf("Devices = %v, want %v", p.Extras.Devices, want)
	}
	if got := p.Extras.Describe(); !strings.Contains(got, "config.limits.cpu") || !strings.Contains(got, "device.scratch (disk)") {
		t.Errorf("Describe() = %q", got)
	}

	// without asking, nothing is read or said
	plain, err := planKind(s, r, NewPlanOptions(true))
	if err != nil || plain.Extras != nil {
		t.Errorf("extras are only reported when asked for: %+v, %v", plain.Extras, err)
	}
}

func TestPlanExtrasForAProfileThatMatchesIsSilent(t *testing.T) {
	live := &api.Profile{Name: "web", ProfilePut: api.ProfilePut{Config: map[string]string{"limits.cpu": "2"}}}
	p := planExtrasOf(t, &kindsServer{profile: live}, Resource{Kind: KindProfile, Name: "web", Config: map[string]string{"limits.cpu": "2"}})
	if p.Extras != nil && !p.Extras.Empty() {
		t.Errorf("nothing undeclared, so nothing to say: %+v", p.Extras)
	}
}

func TestPlanExtrasForAMissingObjectIsNil(t *testing.T) {
	for _, r := range []Resource{
		{Kind: KindProfile, Name: "web"}, {Kind: KindProject, Name: "p"}, {Kind: KindNetwork, Name: "br0"},
		{Kind: KindStorageVolume, Name: "data", Backup: &backupmeta.VolumeBackup{None: "regenerable"}},
	} {
		p := planExtrasOf(t, &kindsServer{}, r)
		if p.Action != ActionCreate || p.Extras != nil {
			t.Errorf("%s: a stack not yet applied has no extras: action = %v, extras = %+v", r.Kind, p.Action, p.Extras)
		}
	}
}

func TestPlanSaysWhatAStorageVolumeKeepsAndNeverCountsTinksOrIncusKeys(t *testing.T) {
	live := &api.StorageVolume{Name: "data", StorageVolumePut: api.StorageVolumePut{Config: map[string]string{
		"size":                  "10GiB",
		"snapshots.expiry":      "7d",
		"volatile.idmap.last":   "[]",
		backupmeta.PolicyKey:    "{}",
		backupmeta.StackKey:     "mystack",
		backupmeta.MarkerCopyOf: "default/default/data",
	}}}
	r := Resource{Kind: KindStorageVolume, Name: "data", Config: map[string]string{"size": "10GiB"}, Backup: &backupmeta.VolumeBackup{None: "regenerable"}}
	p := planExtrasOf(t, &kindsServer{volume: live}, r)
	if p.Extras == nil {
		t.Fatal("snapshots.expiry is on the volume and not in the stack")
	}
	if want := []string{"snapshots.expiry"}; !reflect.DeepEqual(p.Extras.Config, want) {
		t.Errorf("Config = %v, want %v (volatile.* and user.tink.* are not a person's)", p.Extras.Config, want)
	}
}

func TestPlanSaysWhatANetworkKeeps(t *testing.T) {
	live := &api.Network{Name: "br0", Managed: true, Type: "bridge", Status: "Created", NetworkPut: api.NetworkPut{Config: map[string]string{
		"ipv4.address": "203.0.113.1/24", "ipv4.dhcp": "false", "volatile.bridge.hwaddr": "00:00:5e:00:53:01",
	}}}
	r := Resource{Kind: KindNetwork, Name: "br0", Config: map[string]string{"ipv4.address": "203.0.113.1/24"}}
	p := planExtrasOf(t, &kindsServer{network: live}, r)
	if p.Action != ActionNone {
		t.Errorf("action = %v, %v", p.Action, p.Blocked)
	}
	if p.Extras == nil || !reflect.DeepEqual(p.Extras.Config, []string{"ipv4.dhcp"}) {
		t.Errorf("Extras = %+v, want ipv4.dhcp only", p.Extras)
	}
}

func TestPlanSaysWhatAProjectKeepsAndNotTheFeatureFlagsIncusFilledIn(t *testing.T) {
	live := &api.Project{Name: "p", ProjectPut: api.ProjectPut{Config: map[string]string{
		"features.images": "true", "features.profiles": "true", "features.networks": "false", // what Incus writes at creation
		"features.storage.volumes": "false", // not its default: someone set it
		"limits.instances":         "5",
	}}}
	r := Resource{Kind: KindProject, Name: "p", Config: map[string]string{"features.profiles": "true"}}
	p := planExtrasOf(t, &kindsServer{project: live}, r)
	if p.Action != ActionNone {
		t.Errorf("action = %v", p.Action)
	}
	if p.Extras == nil || !reflect.DeepEqual(p.Extras.Config, []string{"features.storage.volumes", "limits.instances"}) {
		t.Errorf("Extras = %+v", p.Extras)
	}
}
