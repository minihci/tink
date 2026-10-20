package resolve

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// Files under a mount, on an instance that is not running (filemount.go). The fakes of firststartmount_test.go hold the volume files and record every
// call; fileFake adds the instance, its state, and the instance's own file API.

var (
	stateStopped  = &api.InstanceState{Status: "Stopped"}
	stateRunning  = &api.InstanceState{Status: "Running", Pid: 4242}
	stateDeadInit = &api.InstanceState{Status: "Running", Pid: -1} // the document form of a Running container with no init
)

// waitOp is an operation that has finished. Any other method panics (nil embedded interface).
type waitOp struct{ incus.Operation }

func (waitOp) Wait() error { return nil }

type fileFake struct {
	*mountFake
	inst     *api.Instance // nil: there is no such instance
	instErr  error
	state    *api.InstanceState
	stateErr error
	states   int // how often the state was asked
}

// newFileFake is the instance "ha", a container Incus lists with status instStatus, whose state call answers state, with these devices in effect.
func newFileFake(instStatus string, state *api.InstanceState, devices map[string]map[string]string) *fileFake {
	return &fileFake{
		mountFake: newMountFake(),
		inst:      &api.Instance{Name: "ha", Type: "container", Status: instStatus, ExpandedDevices: devices},
		state:     state,
	}
}

func (s *fileFake) UseProject(string) incus.InstanceServer { return s }

func (s *fileFake) GetInstance(name string) (*api.Instance, string, error) {
	switch {
	case s.instErr != nil:
		return nil, "", s.instErr
	case s.inst == nil:
		return nil, "", api.StatusErrorf(http.StatusNotFound, "Instance not found")
	}
	return s.inst, "", nil
}

func (s *fileFake) GetInstanceState(string) (*api.InstanceState, string, error) {
	s.states++
	return s.state, "", s.stateErr
}

func (s *fileFake) GetInstanceFile(instance, p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	key := instance + ":" + p
	s.log = append(s.log, "read instance "+key)
	c, ok := s.instance[key]
	if !ok {
		return nil, nil, api.StatusErrorf(http.StatusNotFound, "not found")
	}
	return io.NopCloser(strings.NewReader(c)), &incus.InstanceFileResponse{Type: "file"}, nil
}

func (s *fileFake) UpdateInstanceState(name string, put api.InstanceStatePut, _ string) (incus.Operation, error) {
	s.log = append(s.log, put.Action+" "+name)
	return waitOp{}, nil
}

// touchesVolume and touchesInstance say where the calls went.
func (s *fileFake) touchesVolume() bool {
	for _, l := range s.log {
		if strings.Contains(l, " custom ") {
			return true
		}
	}
	return false
}
func (s *fileFake) touchesInstance() bool {
	for _, l := range s.log {
		if strings.Contains(l, "instance ha:") || strings.HasPrefix(l, "push ") {
			return true
		}
	}
	return false
}

var (
	haVolumeDevices  = map[string]map[string]string{"root": rootDisk(), "config": volumeDisk("/config", "e2e-ha-config")}
	hostMediaDevices = map[string]map[string]string{"root": rootDisk(), "media": disk("/media", "/srv/media", "")}
)

const (
	volCfg  = "default/e2e-ha-config:/configuration.yaml"
	instCfg = "ha:/config/configuration.yaml"
)

func TestDecideFileTarget(t *testing.T) {
	nested := map[string]map[string]string{"config": volumeDisk("/config", "v"), "media": disk("/config/media", "/srv/media", "")}
	cases := []struct {
		name       string
		fake       *fileFake
		path       string
		want       fileWhere
		wantDevice string
		wantWhy    string
		wantStates int
		wantErr    string
	}{
		{"stopped, under a volume mount: the volume", newFileFake("Stopped", stateStopped, haVolumeDevices), "/config/configuration.yaml",
			inVolume, "config", "its status is Stopped", 1, ""},
		{"Running with pid -1 in the state: the volume", newFileFake("Running", stateDeadInit, haVolumeDevices), "/config/configuration.yaml",
			inVolume, "config", "reports no init process (pid -1)", 1, ""},
		{"Running, and the state call itself fails with Invalid PID -1 (what Incus really answers): the volume", func() *fileFake {
			f := newFileFake("Running", nil, haVolumeDevices)
			f.stateErr = api.StatusErrorf(http.StatusInternalServerError, "Invalid PID -1")
			return f
		}(), "/config/configuration.yaml", inVolume, "config", "cannot find its init process: Invalid PID -1", 1, ""},
		{"frozen counts as not running", newFileFake("Frozen", &api.InstanceState{Status: "Frozen", Pid: 4242}, haVolumeDevices), "/config/configuration.yaml",
			inVolume, "config", "its status is Frozen", 1, ""},
		{"running: the instance's own file API", newFileFake("Running", stateRunning, haVolumeDevices), "/config/configuration.yaml",
			inInstance, "", "", 1, ""},
		{"ready (running, and said so): the instance's own file API", newFileFake("Running", &api.InstanceState{Status: "Ready", Pid: 4242}, haVolumeDevices), "/config/configuration.yaml",
			inInstance, "", "", 1, ""},
		{"stopped, under a host directory mount: withheld", newFileFake("Stopped", stateStopped, hostMediaDevices), "/media/list.txt",
			withheld, "media", "its status is Stopped", 1, ""},
		{"stopped, the file is the volume's mount path itself: withheld", newFileFake("Stopped", stateStopped, haVolumeDevices), "/config",
			withheld, "config", "its status is Stopped", 1, ""},
		{"running, under a host directory mount: the instance's own file API", newFileFake("Running", stateRunning, hostMediaDevices), "/media/list.txt",
			inInstance, "", "", 1, ""},
		{"stopped, on the root filesystem: the instance's own file API, and the state is not even asked", newFileFake("Stopped", stateStopped, haVolumeDevices), "/etc/caddy/Caddyfile",
			inInstance, "", "", 0, ""},
		{"stopped, a name that only starts like the mount path: not under it", newFileFake("Stopped", stateStopped, haVolumeDevices), "/configuration.yaml",
			inInstance, "", "", 0, ""},
		{"stopped, a path that is cleaned first", newFileFake("Stopped", stateStopped, haVolumeDevices), "/config//sub/../configuration.yaml",
			inVolume, "config", "its status is Stopped", 1, ""},
		{"stopped, no devices but the root disk: the instance's own file API", newFileFake("Stopped", stateStopped, map[string]map[string]string{"root": rootDisk()}), "/etc/x",
			inInstance, "", "", 0, ""},
		{"stopped, nested mounts: the deepest decides (host directory)", newFileFake("Stopped", stateStopped, nested), "/config/media/x",
			withheld, "media", "its status is Stopped", 1, ""},
		{"stopped, nested mounts: the deepest decides (volume)", newFileFake("Stopped", stateStopped, nested), "/config/x",
			inVolume, "config", "its status is Stopped", 1, ""},
		{"a virtual machine keeps the instance's own file API, mount or not", func() *fileFake {
			f := newFileFake("Stopped", stateStopped, haVolumeDevices)
			f.inst.Type = "virtual-machine"
			return f
		}(), "/config/configuration.yaml", inInstance, "", "", 0, ""},
		{"an instance that does not exist yet: the instance's own file API, which says 404 and plans a create", func() *fileFake {
			f := newFileFake("", nil, nil)
			f.inst = nil
			return f
		}(), "/config/configuration.yaml", inInstance, "", "", 0, ""},
		{"a state that cannot be read is an error, not a guess", func() *fileFake {
			f := newFileFake("Running", nil, haVolumeDevices)
			f.stateErr = api.StatusErrorf(http.StatusForbidden, "not authorized")
			return f
		}(), "/config/configuration.yaml", inInstance, "", "", 1, "reading the state of instance ha to see where /config/configuration.yaml goes"},
		{"a state that fails for another 500 is an error too", func() *fileFake {
			f := newFileFake("Running", nil, haVolumeDevices)
			f.stateErr = api.StatusErrorf(http.StatusInternalServerError, "database is locked")
			return f
		}(), "/config/configuration.yaml", inInstance, "", "", 1, "database is locked"},
		{"an instance that cannot be read is an error", func() *fileFake {
			f := newFileFake("Running", stateRunning, haVolumeDevices)
			f.instErr = api.StatusErrorf(http.StatusInternalServerError, "database is locked")
			return f
		}(), "/config/configuration.yaml", inInstance, "", "", 0, "reading instance ha to see whether /config/configuration.yaml is under a mount"},
		{"a state that cannot be read does not matter for a file outside every mount", func() *fileFake {
			f := newFileFake("Running", nil, haVolumeDevices)
			f.stateErr = api.StatusErrorf(http.StatusForbidden, "not authorized")
			return f
		}(), "/etc/x", inInstance, "", "", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fileAt("f", tc.path)
			got, err := decideFileTarget(tc.fake, f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr == "" {
				if got.Where != tc.want || got.Mount.Device != tc.wantDevice || !strings.Contains(got.Why, tc.wantWhy) || (tc.wantWhy == "" && got.Why != "") {
					t.Errorf("target = %+v, want where %v device %q why containing %q", got, tc.want, tc.wantDevice, tc.wantWhy)
				}
			}
			if tc.fake.states != tc.wantStates {
				t.Errorf("the state was asked %d time(s), want %d", tc.fake.states, tc.wantStates)
			}
		})
	}
}

// What plan says for a file, and where it looked. The instance's own copy under the mount is deliberately stale or different in each case where the
// volume is the right place, so a read from the wrong one shows.
func TestPlanPlacedFile(t *testing.T) {
	want := "http:\n  server_host: [203.0.113.10]\n"
	f := fileAt("ha-yaml", "/config/configuration.yaml")
	f.Content = want

	const volumeNote = "instance ha is not running (its status is Stopped), so /config/configuration.yaml is read from and written into volume default/e2e-ha-config, where the app finds it once the instance runs; pushed into the instance it would land beneath the /config mount (device config)"
	const deadNote = "instance ha is not running (Incus lists it as Running but reports no init process (pid -1)), so /config/configuration.yaml is read from and written into volume default/e2e-ha-config, where the app finds it once the instance runs; pushed into the instance it would land beneath the /config mount (device config)"

	cases := []struct {
		name      string
		fake      *fileFake
		setup     func(*fileFake)
		wantAct   Action
		wantNotes []string
		wantLog   []string
	}{
		{"stopped, the volume holds the old file: would update, read from the volume", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func(s *fileFake) { s.volumes[volCfg] = "old"; s.instance[instCfg] = want },
			ActionUpdate, []string{volumeNote}, []string{"read custom " + volCfg}},
		{"stopped, the volume holds the right file: no changes, even though the stale copy beneath the mount differs", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func(s *fileFake) { s.volumes[volCfg] = want; s.instance[instCfg] = "stale" },
			ActionNone, []string{volumeNote}, []string{"read custom " + volCfg}},
		{"stopped, the volume has no such file: would create, even though a copy beneath the mount is right", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func(s *fileFake) { s.instance[instCfg] = want },
			ActionCreate, []string{volumeNote}, []string{"read custom " + volCfg}},
		{"Running with a dead init (pid -1 in the state): the volume is read", newFileFake("Running", stateDeadInit, haVolumeDevices),
			func(s *fileFake) { s.volumes[volCfg] = "old" },
			ActionUpdate, []string{deadNote}, []string{"read custom " + volCfg}},
		{"Running with a dead init (the state call fails with Invalid PID -1): the volume is read", func() *fileFake {
			s := newFileFake("Running", nil, haVolumeDevices)
			s.stateErr = api.StatusErrorf(http.StatusInternalServerError, "Invalid PID -1")
			return s
		}(), func(s *fileFake) { s.volumes[volCfg] = want },
			ActionNone, []string{"instance ha is not running (Incus lists it as Running but cannot find its init process: Invalid PID -1), so /config/configuration.yaml is read from and written into volume default/e2e-ha-config, where the app finds it once the instance runs; pushed into the instance it would land beneath the /config mount (device config)"},
			[]string{"read custom " + volCfg}},
		{"running: the instance's own file API sees the mount, and the volume is not touched", newFileFake("Running", stateRunning, haVolumeDevices),
			func(s *fileFake) { s.instance[instCfg] = "old"; s.volumes[volCfg] = want },
			ActionUpdate, nil, []string{"read instance " + instCfg}},
		{"running, and equal: no changes, no note", newFileFake("Running", stateRunning, haVolumeDevices),
			func(s *fileFake) { s.instance[instCfg] = want },
			ActionNone, nil, []string{"read instance " + instCfg}},
		{"stopped, a file on the root filesystem: the instance's own file API, as before", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func(s *fileFake) { s.instance["ha:/etc/caddy/Caddyfile"] = "old" },
			ActionUpdate, nil, []string{"read instance ha:/etc/caddy/Caddyfile"}},
		{"stopped, under a host directory mount: deferred, and nothing is read", newFileFake("Stopped", stateStopped, hostMediaDevices),
			func(s *fileFake) {},
			ActionDeferred, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(tc.fake)
			file := f
			switch {
			case strings.Contains(tc.name, "root filesystem"):
				file = fileAt("caddy", "/etc/caddy/Caddyfile")
			case strings.Contains(tc.name, "host directory"):
				file = fileAt("playlist", "/media/list.txt")
			}
			p, err := planPlacedFile(tc.fake, file)
			if err != nil {
				t.Fatal(err)
			}
			if p.Action != tc.wantAct {
				t.Errorf("action = %v, want %v (changes %v)", p.Action, tc.wantAct, p.Changes)
			}
			if !reflect.DeepEqual(p.Notes, tc.wantNotes) {
				t.Errorf("notes = %q\nwant    %q", p.Notes, tc.wantNotes)
			}
			if !reflect.DeepEqual(tc.fake.log, tc.wantLog) {
				t.Errorf("calls = %v\nwant    %v", tc.fake.log, tc.wantLog)
			}
		})
	}
}

// A file that cannot be placed is deferred with the file, the device and the mount, and why; the plan is no longer a claim about "no changes".
func TestPlanPlacedFile_DeferredNamesTheFileTheDeviceTheMountAndWhy(t *testing.T) {
	cases := []struct {
		name string
		fake *fileFake
		file Resource
		want string
	}{
		{"a stopped instance and a host directory", newFileFake("Stopped", stateStopped, hostMediaDevices), fileAt("playlist", "/media/list.txt"),
			"not written, because instance ha is not running (its status is Stopped) and /media/list.txt is at or under /media, a mounted host directory (device media): pushed now it would land beneath the mount, where the app never sees it, so it is left for the next apply that finds the instance running"},
		{"a dead init and a host directory", newFileFake("Running", stateDeadInit, hostMediaDevices), fileAt("playlist", "/media/list.txt"),
			"not written, because instance ha is not running (Incus lists it as Running but reports no init process (pid -1)) and /media/list.txt is at or under /media, a mounted host directory (device media): pushed now it would land beneath the mount, where the app never sees it, so it is left for the next apply that finds the instance running"},
		{"a stopped instance and the mount path of a volume itself", newFileFake("Stopped", stateStopped, haVolumeDevices), fileAt("cfg", "/config"),
			"not written, because instance ha is not running (its status is Stopped) and /config is at or under /config, a mounted volume (device config): pushed now it would land beneath the mount, where the app never sees it, so it is left for the next apply that finds the instance running"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := planPlacedFile(tc.fake, tc.file)
			if err != nil {
				t.Fatal(err)
			}
			if p.Action != ActionDeferred || !reflect.DeepEqual(p.Warnings, []string{tc.want}) || len(p.Changes) != 0 || len(p.Blocked) != 0 {
				t.Errorf("plan = %+v\nwant a deferred plan with the warning %q", p, tc.want)
			}
			if len(tc.fake.log) != 0 {
				t.Errorf("a deferred file must not be read or written anywhere: %v", tc.fake.log)
			}
		})
	}
}

// An instance that is not there yet keeps planning a create, and a read that fails stops the plan, as ever.
func TestPlanPlacedFile_TheOldEdges(t *testing.T) {
	f := fileAt("ha-yaml", "/config/configuration.yaml")

	absent := newFileFake("", nil, nil)
	absent.inst = nil
	if p, err := planPlacedFile(absent, f); err != nil || p.Action != ActionCreate || len(p.Notes) != 0 {
		t.Errorf("an absent instance: %+v %v", p, err)
	}

	broken := newFileFake("Stopped", stateStopped, haVolumeDevices)
	broken.readErr = api.StatusErrorf(http.StatusForbidden, "not authorized")
	_, err := planPlacedFile(broken, f)
	if err == nil || !strings.Contains(err.Error(), "reading current content of /config/configuration.yaml on ha") || !errors.Is(err, broken.readErr) {
		t.Errorf("a volume that cannot be read stops the plan, naming the file: %v", err)
	}

	dir := newFileFake("Stopped", stateStopped, haVolumeDevices)
	dir.dirs[volCfg] = true
	if _, err := planPlacedFile(dir, f); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Errorf("a directory in the way: %v", err)
	}
}

// pushFile follows the same decision, and keeps its restart exactly as it was: restart: true starts a stopped instance and restarts a Running one.
func TestPushFile_WhereTheContentGoes(t *testing.T) {
	const content = "new"
	cases := []struct {
		name         string
		fake         *fileFake
		file         func() Resource
		setup        func(*fileFake)
		wantLog      []string
		wantVolume   string // what the volume holds afterwards ("" when it has nothing)
		wantInstance string // what the instance's own filesystem holds afterwards
		wantErr      string
	}{
		{"stopped, under a volume mount: written into the volume, the instance is not touched", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func() Resource { return fileAt("ha-yaml", "/config/configuration.yaml") },
			func(s *fileFake) { s.volumes[volCfg] = "old" },
			[]string{"read custom " + volCfg, "write custom " + volCfg}, "ha-yaml", "", ""},
		{"stopped, the volume has no such file yet: created in the volume", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func() Resource { return fileAt("ha-yaml", "/config/configuration.yaml") },
			func(*fileFake) {},
			[]string{"read custom " + volCfg, "write custom " + volCfg}, "ha-yaml", "", ""},
		{"stopped, the volume is already right: read, not written", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func() Resource { return fileAt("ha-yaml", "/config/configuration.yaml") },
			func(s *fileFake) { s.volumes[volCfg] = "ha-yaml" },
			[]string{"read custom " + volCfg}, "ha-yaml", "", ""},
		{"Running with a dead init: written into the volume", newFileFake("Running", stateDeadInit, haVolumeDevices),
			func() Resource { return fileAt("ha-yaml", "/config/configuration.yaml") },
			func(s *fileFake) { s.volumes[volCfg] = "old" },
			[]string{"read custom " + volCfg, "write custom " + volCfg}, "ha-yaml", "", ""},
		{"running: pushed into the instance, as before, and the volume is not touched", newFileFake("Running", stateRunning, haVolumeDevices),
			func() Resource { return fileAt("ha-yaml", "/config/configuration.yaml") },
			func(s *fileFake) { s.volumes[volCfg] = "old" },
			[]string{"push " + instCfg}, "old", "ha-yaml", ""},
		{"stopped, a file on the root filesystem: pushed into the instance, as before", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func() Resource { return fileAt("caddy", "/etc/caddy/Caddyfile") },
			func(*fileFake) {},
			[]string{"push ha:/etc/caddy/Caddyfile"}, "", "caddy", ""},
		{"stopped, under a host directory mount: not pushed, and an error if the apply gets that far", newFileFake("Stopped", stateStopped, hostMediaDevices),
			func() Resource { return fileAt("playlist", "/media/list.txt") },
			func(*fileFake) {},
			nil, "", "", "/media/list.txt was not pushed to ha, because instance ha is not running (its status is Stopped) and /media/list.txt is at or under /media, a mounted host directory (device media)"},
		{"stopped, a write into the volume that fails is an error naming the volume", func() *fileFake {
			s := newFileFake("Stopped", stateStopped, haVolumeDevices)
			s.writeErr = api.StatusErrorf(http.StatusNotFound, "no such file or directory")
			return s
		}(), func() Resource { return fileAt("ha-yaml", "/config/sub/configuration.yaml") },
			func(*fileFake) {},
			[]string{"read custom default/e2e-ha-config:/sub/configuration.yaml", "write custom default/e2e-ha-config:/sub/configuration.yaml"}, "", "", "writing /sub/configuration.yaml into volume default/e2e-ha-config failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(tc.fake)
			f := tc.file()
			f.Content = content
			if f.Name == "ha-yaml" || f.Name == "caddy" {
				f.Content = f.Name // so a file already right can be set up by name
			}
			err := pushFile(tc.fake, f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.fake.log, tc.wantLog) {
				t.Errorf("calls = %v\nwant    %v", tc.fake.log, tc.wantLog)
			}
			if got := tc.fake.volumes[volCfg]; f.Path == "/config/configuration.yaml" && got != tc.wantVolume {
				t.Errorf("volume holds %q, want %q", got, tc.wantVolume)
			}
			if got := tc.fake.instance["ha:"+f.Path]; got != tc.wantInstance {
				t.Errorf("the instance's own filesystem holds %q at %s, want %q", got, f.Path, tc.wantInstance)
			}
		})
	}
}

// restart: true keeps its meaning. Writing into the volume of an instance that is not running does not change when a restart happens: after a write,
// EnsureRunning starts a stopped instance and restarts one Incus lists as Running, as it did after a push.
func TestPushFile_RestartIsAsItWas(t *testing.T) {
	cases := []struct {
		name    string
		fake    *fileFake
		restart bool
		want    []string
	}{
		{"stopped, no restart asked: the volume is written and nothing is started", newFileFake("Stopped", stateStopped, haVolumeDevices), false,
			[]string{"read custom " + volCfg, "write custom " + volCfg}},
		{"stopped, restart: true: the volume is written, then the instance is started", newFileFake("Stopped", stateStopped, haVolumeDevices), true,
			[]string{"read custom " + volCfg, "write custom " + volCfg, "start ha"}},
		{"Running with a dead init, restart: true: the volume is written, then a restart is asked for (Incus may refuse it)", newFileFake("Running", stateDeadInit, haVolumeDevices), true,
			[]string{"read custom " + volCfg, "write custom " + volCfg, "restart ha"}},
		{"running, restart: true: pushed into the instance, then restarted, as ever", newFileFake("Running", stateRunning, haVolumeDevices), true,
			[]string{"push " + instCfg, "restart ha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fileAt("ha-yaml", "/config/configuration.yaml")
			f.Restart = tc.restart
			if err := pushFile(tc.fake, f); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.fake.log, tc.want) {
				t.Errorf("calls = %v\nwant    %v", tc.fake.log, tc.want)
			}
		})
	}
}

// The whole of an apply for a file: what it prints, what it writes, how it counts.
func TestApplyOne_AFileUnderAMountOfAnInstanceThatIsNotRunning(t *testing.T) {
	const volumeNote = "instance ha is not running (its status is Stopped), so /config/configuration.yaml is read from and written into volume default/e2e-ha-config, where the app finds it once the instance runs; pushed into the instance it would land beneath the /config mount (device config)"
	collect := func() (func(string, ...any), *[]string) {
		var lines []string
		return func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, &lines
	}
	f := fileAt("ha-yaml", "/config/configuration.yaml")
	f.Content = "new"

	t.Run("different in the volume: updated there, and it says where", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, haVolumeDevices)
		s.volumes[volCfg] = "old"
		s.instance[instCfg] = "new" // the hidden copy must not fool the compare
		note, lines := collect()
		out, err := applyOne(s, f, PlanOptions{}, note)
		if err != nil || out != outChanged {
			t.Fatalf("outcome %v err %v", out, err)
		}
		want := []string{"file/ha-yaml: updated (content of /config/configuration.yaml on ha differs)", "file/ha-yaml: " + volumeNote}
		if !reflect.DeepEqual(*lines, want) {
			t.Errorf("lines = %q\nwant    %q", *lines, want)
		}
		if s.volumes[volCfg] != "new" {
			t.Errorf("volume holds %q", s.volumes[volCfg])
		}
		if s.lastWrite.Mode != 0o644 || s.lastWrite.WriteMode != "overwrite" {
			t.Errorf("written with %+v, want what placeFile uses", s.lastWrite)
		}
	})

	t.Run("absent in the volume: created there", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, haVolumeDevices)
		note, lines := collect()
		out, err := applyOne(s, f, PlanOptions{}, note)
		if err != nil || out != outChanged {
			t.Fatalf("outcome %v err %v", out, err)
		}
		if want := []string{"file/ha-yaml: created", "file/ha-yaml: " + volumeNote}; !reflect.DeepEqual(*lines, want) {
			t.Errorf("lines = %q\nwant    %q", *lines, want)
		}
		if s.volumes[volCfg] != "new" || s.touchesInstance() {
			t.Errorf("volume holds %q; calls %v", s.volumes[volCfg], s.log)
		}
	})

	t.Run("equal in the volume: no changes, no write, and it says which copy was compared", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, haVolumeDevices)
		s.volumes[volCfg] = "new"
		note, lines := collect()
		out, err := applyOne(s, f, PlanOptions{}, note)
		if err != nil || out != outConverged {
			t.Fatalf("outcome %v err %v", out, err)
		}
		if want := []string{"file/ha-yaml: no changes", "file/ha-yaml: " + volumeNote}; !reflect.DeepEqual(*lines, want) {
			t.Errorf("lines = %q\nwant    %q", *lines, want)
		}
		if want := []string{"read custom " + volCfg}; !reflect.DeepEqual(s.log, want) {
			t.Errorf("calls = %v, want a single read", s.log)
		}
	})

	t.Run("a host directory: a warning with the file, the device, the mount and why, nothing written, deferred, and it is not a failure", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, hostMediaDevices)
		note, lines := collect()
		out, err := applyOne(s, fileAt("playlist", "/media/list.txt"), PlanOptions{}, note)
		if err != nil || out != outDeferred {
			t.Fatalf("outcome %v err %v, want deferred and no error", out, err)
		}
		want := []string{
			"file/playlist: warning: not written, because instance ha is not running (its status is Stopped) and /media/list.txt is at or under /media, a mounted host directory (device media): pushed now it would land beneath the mount, where the app never sees it, so it is left for the next apply that finds the instance running",
			"file/playlist: deferred until the instance is running (see the warning above)",
		}
		if !reflect.DeepEqual(*lines, want) {
			t.Errorf("lines = %q\nwant    %q", *lines, want)
		}
		if len(s.log) != 0 {
			t.Errorf("nothing may be read or written: %v", s.log)
		}
	})

	t.Run("the same file once the instance runs: pushed into it, no note", func(t *testing.T) {
		s := newFileFake("Running", stateRunning, hostMediaDevices)
		note, lines := collect()
		out, err := applyOne(s, fileAt("playlist", "/media/list.txt"), PlanOptions{}, note)
		if err != nil || out != outChanged {
			t.Fatalf("outcome %v err %v", out, err)
		}
		if want := []string{"file/playlist: created"}; !reflect.DeepEqual(*lines, want) {
			t.Errorf("lines = %q\nwant    %q", *lines, want)
		}
		if s.instance["ha:/media/list.txt"] != "playlist" || s.touchesVolume() {
			t.Errorf("calls = %v", s.log)
		}
	})

	t.Run("a file on the root filesystem of a stopped instance: as before", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, haVolumeDevices)
		note, lines := collect()
		out, err := applyOne(s, fileAt("caddy", "/etc/caddy/Caddyfile"), PlanOptions{}, note)
		if err != nil || out != outChanged {
			t.Fatalf("outcome %v err %v", out, err)
		}
		if want := []string{"file/caddy: created"}; !reflect.DeepEqual(*lines, want) {
			t.Errorf("lines = %q\nwant    %q", *lines, want)
		}
		if s.touchesVolume() || s.states != 0 {
			t.Errorf("calls = %v, states asked %d", s.log, s.states)
		}
	})
}

// A deferred file is not a failure and not a block: the exit status is the apply's, and the summary says how many were left.
func TestApplyLevels_ADeferredFileIsNotAFailure(t *testing.T) {
	levels := [][]Resource{{{Kind: KindFile, Name: "a"}, {Kind: KindFile, Name: "b"}}}
	notes, err := applyLevels(levels, func(r Resource, _ func(string, ...any)) (outcome, error) {
		if r.Name == "a" {
			return outDeferred, nil
		}
		return outConverged, nil
	})
	if err != nil {
		t.Fatalf("err = %v, want none: a deferred file does not fail the apply", err)
	}
	if last := notes[len(notes)-1]; last != "summary: 1 converged, 0 changed, 0 blocked, 0 failed, 1 deferred" {
		t.Errorf("summary = %q", last)
	}

	// and the line is exactly what it was when nothing is deferred
	notes, err = applyLevels(levels, func(Resource, func(string, ...any)) (outcome, error) { return outConverged, nil })
	if last := notes[len(notes)-1]; err != nil || last != "summary: 2 converged, 0 changed, 0 blocked, 0 failed" {
		t.Errorf("summary = %q err %v", last, err)
	}
}

// The state is read for a file under a mount only, however many files an apply has.
func TestPlan_AFileOutsideEveryMountNeverAsksForTheState(t *testing.T) {
	s := newFileFake("Stopped", stateStopped, haVolumeDevices)
	s.stateErr = errors.New("must not be asked")
	s.instance["ha:/etc/x"] = "same"
	f := fileAt("x", "/etc/x")
	f.Content = "same"
	p, err := planOne(s, f, PlanOptions{})
	if err != nil || p.Action != ActionNone || s.states != 0 {
		t.Errorf("%+v %v (state asked %d times)", p, err, s.states)
	}
}
