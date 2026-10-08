package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/volbackup"
)

// handoffServer is a server with a helper instance, some volumes, and a trust store.
type handoffServer struct {
	instancesServer
	vols map[string][]api.StorageVolume // by pool
}

func (s handoffServer) GetStoragePoolNames() ([]string, error) {
	var out []string
	for p := range s.vols {
		out = append(out, p)
	}
	return out, nil
}

func (s handoffServer) GetStoragePoolVolumesAllProjects(pool string) ([]api.StorageVolume, error) {
	return s.vols[pool], nil
}

func (s handoffServer) UseProject(string) incus.InstanceServer { return s }

func policyOf(t *testing.T, copies ...backupmeta.PolicyCopy) map[string]string {
	t.Helper()
	text, err := backupmeta.EncodePolicy(backupmeta.BackupPolicy{Proto: backupmeta.PolicyProto, Copies: copies})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{backupmeta.PolicyKey: text}
}

func nasCopy() backupmeta.PolicyCopy {
	return backupmeta.PolicyCopy{Target: backupmeta.PolicyTarget{Name: "nas", Pool: "nas2510"}, Schedule: "0 3 * * *", Retain: "7d"}
}

func libItems() []backuprun.Item {
	v := volbackup.Volume{Pool: "default", Name: "lib"}
	return []backuprun.Item{{Volume: v, Label: "lib", Copies: []backuprun.Copy{{
		Target: volbackup.Target{Name: "nas", Pool: "nas2510"}, Schedule: "0 3 * * *", Retain: "7d"}}}}
}

func serverWith(t *testing.T, inst []api.Instance, libConfig map[string]string) handoffServer {
	t.Helper()
	cert := api.Certificate{CertificatePut: api.CertificatePut{Name: helper.TrustName, Type: "client"}}
	vols := map[string][]api.StorageVolume{"default": {}}
	if libConfig != nil {
		vols["default"] = append(vols["default"], api.StorageVolume{Name: "lib", Type: "custom", Project: "default", StorageVolumePut: api.StorageVolumePut{Config: libConfig}})
	}
	return handoffServer{instancesServer: instancesServer{all: inst, certs: []api.Certificate{cert}}, vols: vols}
}

func TestAHealthyHelperTakesTheRunByName(t *testing.T) {
	srv := serverWith(t, []api.Instance{helperInstance("tink-helper", "helper", "Running", goodStatus(nil))}, policyOf(t, nasCopy()))
	h, note, err := prepareHandoff(srv, libItems(), nil, false, helperNow)
	if err != nil || note != "" || h == nil {
		t.Fatalf("%+v %q %v", h, note, err)
	}
	if len(h.Names) != 1 || h.Names[0] != "lib" || h.Found.Label() != "tink-helper/helper" {
		t.Errorf("%+v", h)
	}
}

func TestNoHelperRunsHereAndSaysNothing(t *testing.T) {
	srv := serverWith(t, nil, policyOf(t, nasCopy()))
	h, note, err := prepareHandoff(srv, libItems(), nil, false, helperNow)
	if h != nil || note != "" || err != nil {
		t.Errorf("a server without a helper is the ordinary case: %+v %q %v", h, note, err)
	}
	if _, _, err := prepareHandoff(srv, libItems(), nil, true, helperNow); err == nil || !strings.Contains(err.Error(), "no helper") {
		t.Errorf("--helper insists: %v", err)
	}
}

func TestAHelperThatCannotTakeItRunsHereAndSaysWhy(t *testing.T) {
	for name, tc := range map[string]struct {
		inst api.Instance
		want string
	}{
		"stopped":     {helperInstance("p", "helper", "Stopped", goodStatus(nil)), "stopped"},
		"stale":       {helperInstance("p", "helper", "Running", goodStatus(func(s *helper.Status) { s.Tick = helperNow.Add(-2 * time.Hour) })), "stale"},
		"no document": {helperInstance("p", "helper", "Running", nil), "status document"},
		"protocol":    {helperInstance("p", "helper", "Running", goodStatus(func(s *helper.Status) { s.JobProto = 2 })), "job protocol 2"},
		"draining":    {helperInstance("p", "helper", "Running", goodStatus(func(s *helper.Status) { s.Draining = true })), "draining"},
	} {
		srv := serverWith(t, []api.Instance{tc.inst}, policyOf(t, nasCopy()))
		h, note, err := prepareHandoff(srv, libItems(), nil, false, helperNow)
		if h != nil || err != nil || !strings.Contains(note, tc.want) {
			t.Errorf("%s: %+v %q %v", name, h, note, err)
		}
		if _, _, err := prepareHandoff(srv, libItems(), nil, true, helperNow); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s with --helper must fail, not fall back: %v", name, err)
		}
	}
}

func TestADegradedHelperStillTakesARun(t *testing.T) {
	st := goodStatus(func(s *helper.Status) { s.Failing = []helper.Failing{{Volume: "docs", Target: "nas", Count: 3}} })
	srv := serverWith(t, []api.Instance{helperInstance("p", "helper", "Running", st)}, policyOf(t, nasCopy()))
	if h, note, err := prepareHandoff(srv, libItems(), nil, false, helperNow); h == nil || note != "" || err != nil {
		t.Errorf("a failing copy is when a run by hand is most wanted: %+v %q %v", h, note, err)
	}
}

func TestAPolicyThatIsNotWhatTheStackDeclaresIsRefused(t *testing.T) {
	inst := []api.Instance{helperInstance("p", "helper", "Running", goodStatus(nil))}
	for name, cfg := range map[string]map[string]string{
		"no policy": {},
		"different": policyOf(t, backupmeta.PolicyCopy{Target: backupmeta.PolicyTarget{Name: "nas", Pool: "nas2510"}, Schedule: "0 5 * * *", Retain: "7d"}),
	} {
		_, _, err := prepareHandoff(serverWith(t, inst, cfg), libItems(), nil, false, helperNow)
		if err == nil || !strings.Contains(err.Error(), "tink plan apply") || !strings.Contains(err.Error(), "lib:") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestACopyToARemoteTheHelperLacksIsRefusedEarly(t *testing.T) {
	items := libItems()
	items[0].Copies = append(items[0].Copies, backuprun.Copy{Target: volbackup.Target{Name: "off", Remote: "vps"}, Schedule: "0 4 * * *", Retain: "3d"})
	cfg := policyOf(t, nasCopy(), backupmeta.PolicyCopy{Target: backupmeta.PolicyTarget{Name: "off", Remote: "vps"}, Schedule: "0 4 * * *", Retain: "3d"})
	lacking := goodStatus(func(s *helper.Status) { s.Remotes = []helper.Remote{{Name: "host"}} })
	_, _, err := prepareHandoff(serverWith(t, []api.Instance{helperInstance("p", "helper", "Running", lacking)}, cfg), items, nil, false, helperNow)
	if err == nil || !strings.Contains(err.Error(), `"vps"`) || !strings.Contains(err.Error(), "tink helper remote add") {
		t.Errorf("%v", err)
	}
	having := goodStatus(func(s *helper.Status) { s.Remotes = []helper.Remote{{Name: "host"}, {Name: "vps"}} })
	if h, _, err := prepareHandoff(serverWith(t, []api.Instance{helperInstance("p", "helper", "Running", having)}, cfg), items, nil, false, helperNow); h == nil || err != nil {
		t.Errorf("%v", err)
	}
	silent := goodStatus(nil) // Remotes nil: it did not say, so it is not held to it
	if h, _, err := prepareHandoff(serverWith(t, []api.Instance{helperInstance("p", "helper", "Running", silent)}, cfg), items, nil, false, helperNow); h == nil || err != nil {
		t.Errorf("%v", err)
	}
}

func TestSeveralHelpersAndUnknownVolumes(t *testing.T) {
	two := []api.Instance{helperInstance("a", "h1", "Running", goodStatus(nil)), helperInstance("b", "h2", "Running", goodStatus(nil))}
	if _, _, err := prepareHandoff(serverWith(t, two, policyOf(t, nasCopy())), libItems(), nil, false, helperNow); err == nil || !strings.Contains(err.Error(), "--local") {
		t.Errorf("%v", err)
	}
	one := []api.Instance{helperInstance("a", "h1", "Running", goodStatus(nil))}
	if _, _, err := prepareHandoff(serverWith(t, one, policyOf(t, nasCopy())), libItems(), []string{"nope"}, false, helperNow); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("%v", err)
	}
	if h, note, err := prepareHandoff(serverWith(t, one, nil), nil, nil, false, helperNow); h != nil || note != "" || err != nil {
		t.Errorf("nothing to run is the local run's to say: %+v %q %v", h, note, err)
	}
}

func TestHandingOffQueuesAJobAndFollowsItToItsOutcome(t *testing.T) {
	store := jobs.Store{Dir: t.TempDir()}
	h := &handoff{Found: helper.Found{Project: "tink-helper", Name: "helper"}, Store: store, Names: []string{"lib"}}
	// a job already running ahead of this one
	ahead, _ := store.Enqueue(jobs.Request{Kind: daemon.KindBackupRun, Origin: jobs.OriginSchedule}, helperNow.Add(-time.Minute))
	_ = ahead

	var queued string
	sleeps := 0
	opt := jobs.FollowOptions{Sleep: func(_ context.Context, d time.Duration) {
		list, _ := store.List()
		for _, st := range list {
			if st.Origin == jobs.OriginTrigger {
				queued = st.ID
			}
		}
		if sleeps++; sleeps == 2 {
			writeJob(t, store, queued, `{"proto":1,"id":"`+queued+`","state":"failed","error":"lib -> nas: boom"}`, "FAILED lib -> nas: boom\n")
		}
	}}
	var out, errOut bytes.Buffer
	err := h.run(context.Background(), true, true, &out, &errOut, helperNow, opt)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failed job fails the command: %v", err)
	}
	for _, want := range []string{"handed to the helper tink-helper/helper as job " + queued, "1 ahead of it", "FAILED lib -> nas: boom"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
	b, _ := readFile(store, queued, "request.json")
	var req jobs.Request
	if json.Unmarshal(b, &req) != nil || req.Kind != daemon.KindBackupRun || req.Origin != jobs.OriginTrigger {
		t.Fatalf("%s", b)
	}
	var args daemon.BackupRunArgs
	if json.Unmarshal(req.Args, &args) != nil || len(args.Volumes) != 1 || args.Volumes[0] != "lib" || !args.Due || !args.DryRun {
		t.Errorf("the job must carry exactly what was asked: %+v", args)
	}
}

func TestDetachingLeavesTheHandedOffJobRunning(t *testing.T) {
	store := jobs.Store{Dir: t.TempDir()}
	h := &handoff{Found: helper.Found{Project: "p", Name: "helper"}, Store: store, Names: []string{"lib"}}
	ctx, cancel := context.WithCancel(context.Background())
	var out, errOut bytes.Buffer
	err := h.run(ctx, false, false, &out, &errOut, helperNow, jobs.FollowOptions{Sleep: func(context.Context, time.Duration) { cancel() }})
	if err != nil {
		t.Fatalf("detaching is not a failure: %v", err)
	}
	if !strings.Contains(errOut.String(), "tink helper log -f") || !strings.Contains(errOut.String(), "tink helper cancel") {
		t.Errorf("say how to come back: %q", errOut.String())
	}
	list, _ := store.List()
	if len(list) != 1 || list[0].State != jobs.Queued {
		t.Errorf("the job must be left as it was: %+v", list)
	}
}
