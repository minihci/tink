package resolve

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeOps struct {
	running bool
	snaps   []string
	fail    map[string]error
	calls   []string
}

func (f *fakeOps) rec(name string) error {
	f.calls = append(f.calls, name)
	return f.fail[name]
}
func (f *fakeOps) InstanceRunning(string) (bool, error)       { return f.running, f.rec("running") }
func (f *fakeOps) InstanceSnapshots(string) ([]string, error) { return f.snaps, f.rec("snapshots") }
func (f *fakeOps) PullImage(string) (string, error)           { return "abcdef012345", f.rec("pull") }
func (f *fakeOps) Stop(string) error                          { return f.rec("stop") }
func (f *fakeOps) Start(string) error                         { return f.rec("start") }
func (f *fakeOps) SnapshotVolumes(string, string) ([]string, error) {
	return []string{"default/ha-config"}, f.rec("snapshot-volumes")
}
func (f *fakeOps) Rebuild(string, string) error { return f.rec("rebuild") }
func (f *fakeOps) ApplyConfig(Resource) error   { return f.rec("config") }

func (f *fakeOps) seq() string { return strings.Join(f.calls, " > ") }

func rebuildResource(snapshot bool) Resource {
	return Resource{Kind: KindInstance, Name: "ha", Image: "ghcr:x/y@sha256:aa", OnImageChange: OnImageChangeRebuild, SnapshotVolumes: snapshot}
}

var fixedNow = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }

func TestRunRebuild(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		ops      fakeOps
		snapshot bool
		wantSeq  string
		wantErr  string // substring; empty means success
	}{
		{"running instance: pull before stop, config after rebuild, restart", fakeOps{running: true}, false,
			"running > snapshots > pull > stop > rebuild > config > start", ""},
		{"stopped instance stays stopped", fakeOps{running: false}, false,
			"running > snapshots > pull > rebuild > config", ""},
		{"volume snapshot happens after the stop, before the rebuild", fakeOps{running: true}, true,
			"running > snapshots > pull > stop > snapshot-volumes > rebuild > config > start", ""},

		// failure table
		{"instance gained snapshots since planning: nothing touched", fakeOps{running: true, snaps: []string{"s0"}}, false,
			"running > snapshots", "gained instance snapshots"},
		{"pull fails: nothing changed, never stopped", fakeOps{running: true, fail: map[string]error{"pull": boom}}, false,
			"running > snapshots > pull", "nothing was changed"},
		{"stop fails: nothing changed", fakeOps{running: true, fail: map[string]error{"stop": boom}}, false,
			"running > snapshots > pull > stop", "nothing was changed"},
		{"volume snapshot fails: restarted on the old image", fakeOps{running: true, fail: map[string]error{"snapshot-volumes": boom}}, true,
			"running > snapshots > pull > stop > snapshot-volumes > start", "restored to its previous state"},
		{"volume snapshot fails and restart fails too", fakeOps{running: true, fail: map[string]error{"snapshot-volumes": boom, "start": errors.New("nostart")}}, true,
			"running > snapshots > pull > stop > snapshot-volumes > start", "restarting ha failed too"},
		{"rebuild fails, previous state restored", fakeOps{running: true, fail: map[string]error{"rebuild": boom}}, false,
			"running > snapshots > pull > stop > rebuild > start", "restored to its previous state"},
		{"rebuild fails and the instance will not start: root may be destroyed", fakeOps{running: true, fail: map[string]error{"rebuild": boom, "start": errors.New("no rootfs")}}, false,
			"running > snapshots > pull > stop > rebuild > start", "may have been destroyed"},
		{"rebuild fails on a stopped instance: nothing to restore", fakeOps{running: false, fail: map[string]error{"rebuild": boom}}, false,
			"running > snapshots > pull > rebuild", "restored to its previous state"},
		{"config fails after rebuild: left stopped, not started", fakeOps{running: true, fail: map[string]error{"config": boom}}, false,
			"running > snapshots > pull > stop > rebuild > config", "left stopped"},
		{"start fails at the end", fakeOps{running: true, fail: map[string]error{"start": boom}}, false,
			"running > snapshots > pull > stop > rebuild > config > start", "starting ha failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := tc.ops
			var notes []string
			err := runRebuild(&ops, rebuildResource(tc.snapshot), fixedNow, func(f string, a ...any) { notes = append(notes, f) })
			if ops.seq() != tc.wantSeq {
				t.Errorf("calls:\n got  %s\n want %s", ops.seq(), tc.wantSeq)
			}
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestHasDataVolume(t *testing.T) {
	root := map[string]string{"type": "disk", "pool": "default", "path": "/"}
	vol := map[string]string{"type": "disk", "pool": "default", "source": "ha-config", "path": "/config"}
	host := map[string]string{"type": "disk", "source": "/srv/media", "path": "/media"}
	nic := map[string]string{"type": "nic", "network": "incusbr0"}
	if hasDataVolume(map[string]map[string]string{"root": root, "eth0": nic}) {
		t.Error("root disk and a NIC are not a data volume (the live mosquitto looks like this)")
	}
	if hasDataVolume(map[string]map[string]string{"root": root, "media": host}) {
		t.Error("a host-path bind mount is not a managed data volume")
	}
	if !hasDataVolume(map[string]map[string]string{"root": root, "config": vol}) {
		t.Error("a custom volume must be detected (the live ha looks like this)")
	}
}

func TestSnapshotVolumesName(t *testing.T) {
	var got string
	ops := &namingOps{fakeOps: fakeOps{running: true}, name: &got}
	if err := runRebuild(ops, rebuildResource(true), fixedNow, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if got != "tink-pre-rebuild-20261004T120000Z" {
		t.Errorf("snapshot name = %q", got)
	}
}

type namingOps struct {
	fakeOps
	name *string
}

func (n *namingOps) SnapshotVolumes(_, snapshot string) ([]string, error) {
	*n.name = snapshot
	return []string{"default/ha-config"}, nil
}
