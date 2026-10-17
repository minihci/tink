package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/helper"
)

func TestStatusTargetRefusesTwoPlacesAndAPoolForNoVolume(t *testing.T) {
	for name, tc := range map[string]struct {
		t    statusTarget
		want string
	}{
		"neither is fine (publish nothing)":    {statusTarget{}, ""},
		"an instance alone is the old way":     {statusTarget{Instance: "helper", Project: "p"}, ""},
		"a volume alone is the new way":        {statusTarget{Volume: "helper-status", Pool: "default"}, ""},
		"a volume with its pool given":         {statusTarget{Volume: "helper-status", Pool: "fast", PoolGiven: true}, ""},
		"both is two places for one document":  {statusTarget{Instance: "helper", Volume: "helper-status", Pool: "default"}, "two places"},
		"a pool with no volume means nothing":  {statusTarget{Instance: "helper", Pool: "fast", PoolGiven: true}, "meaningless without"},
		"a pool given and nothing else at all": {statusTarget{Pool: "fast", PoolGiven: true}, "meaningless without"},
	} {
		err := tc.t.validate()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestDaemonRunRefusesBothStatusFlagsBeforeItDoesAnything(t *testing.T) {
	// If the refusal ever goes missing the daemon would simply run, so the command is given a deadline (and a socket that is not there)
	// to turn that into a failed assertion instead of a hang.
	run := func(args ...string) error {
		c := newDaemonRunCmd()
		c.SetArgs(append([]string{"--socket", "/nonexistent/tink-test.sock"}, args...))
		c.SilenceUsage, c.SilenceErrors = true, true
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return c.ExecuteContext(ctx)
	}
	if err := run("--status-instance", "helper", "--status-volume", "helper-status"); err == nil || !strings.Contains(err.Error(), "--status-instance and --status-volume") {
		t.Errorf("err = %v", err)
	}
	if err := run("--status-instance", "helper", "--status-pool", "fast"); err == nil || !strings.Contains(err.Error(), "--status-pool") {
		t.Errorf("err = %v", err)
	}
	for _, flag := range []string{"status-volume", "status-pool", "status-instance", "status-project"} {
		if newDaemonRunCmd().Flags().Lookup(flag) == nil {
			t.Errorf("--%s", flag)
		}
	}
	if f := newDaemonRunCmd().Flags().Lookup("status-pool"); f.DefValue != "default" {
		t.Errorf("--status-pool defaults to the default pool: %q", f.DefValue)
	}
}

// publishServer is an Incus server that has one custom volume with some config of its own, keeps its ETag, and records an instance PATCH.
type publishServer struct {
	incus.InstanceServer
	project string
	cfg     map[string]string
	desc    string
	etag    int
	puts    []string // the etag each volume write carried
	patches []string
}

func (s *publishServer) UseProject(p string) incus.InstanceServer { s.project = p; return s }

func (s *publishServer) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	if pool != "fast" || volType != "custom" || name != "helper-status" {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	v := &api.StorageVolume{Name: name}
	v.Config, v.Description = map[string]string{}, s.desc
	for k, val := range s.cfg {
		v.Config[k] = val
	}
	return v, string(rune('a' + s.etag)), nil
}

func (s *publishServer) UpdateStoragePoolVolume(_, _, _ string, put api.StorageVolumePut, etag string) error {
	s.puts = append(s.puts, etag)
	s.cfg, s.desc = put.Config, put.Description
	s.etag++
	return nil
}

func (s *publishServer) RawQuery(method, path string, data any, _ string) (*api.Response, string, error) {
	b, _ := json.Marshal(data)
	s.patches = append(s.patches, method+" "+path+" "+string(b))
	return &api.Response{Type: "sync", StatusCode: 200}, "", nil
}

func TestTheVolumeFormPublishesTheDocumentWithTheETagAndKeepsTheVolumesOtherKeys(t *testing.T) {
	srv := &publishServer{cfg: map[string]string{"user.mine": "keep", "volatile.uuid": "u"}, desc: "status"}
	target := statusTarget{Volume: "helper-status", Pool: "fast", Project: "tink-helper"}
	p := target.publisher(func() (incus.InstanceServer, error) { return srv, nil }, time.Hour)
	if p == nil || !p.NoMarker {
		t.Fatalf("a publisher that leaves the marker to the instance: %+v", p)
	}
	if wrote, err := p.Publish(helper.Status{JobProto: 1, Version: "v1"}); !wrote || err != nil {
		t.Fatalf("%v %v", wrote, err)
	}
	if srv.project != "tink-helper" || len(srv.puts) != 1 || srv.puts[0] != "a" {
		t.Errorf("one write, in the helper's project, with the ETag that was read: project %q, etags %v", srv.project, srv.puts)
	}
	st, err := helper.Parse(srv.cfg[helper.StatusKey])
	if err != nil || st.Version != "v1" || st.Proto != helper.StatusProto {
		t.Errorf("the document, stamped: %+v %v", st, err)
	}
	if srv.cfg["user.mine"] != "keep" || srv.cfg["volatile.uuid"] != "u" || srv.desc != "status" {
		t.Errorf("every other key (and the description) survives the write: %v %q", srv.cfg, srv.desc)
	}
	if _, has := srv.cfg[helper.MarkerKey]; has {
		t.Errorf("the marker is not written to the volume: %v", srv.cfg)
	}
	if len(srv.patches) != 0 {
		t.Errorf("and the instance is never written: %v", srv.patches)
	}
}

func TestTheInstanceFormStillPatchesTheInstanceAndMarksIt(t *testing.T) {
	srv := &publishServer{}
	p := statusTarget{Instance: "helper", Project: "tink-helper"}.publisher(func() (incus.InstanceServer, error) { return srv, nil }, time.Hour)
	if p == nil || p.NoMarker {
		t.Fatalf("%+v", p)
	}
	if _, err := p.Publish(helper.Status{JobProto: 1}); err != nil {
		t.Fatal(err)
	}
	if len(srv.patches) != 1 || !strings.HasPrefix(srv.patches[0], "PATCH /1.0/instances/helper?project=tink-helper ") || !strings.Contains(srv.patches[0], `"user.tink.helper":"1"`) {
		t.Errorf("unchanged for helpers installed before the status volume: %v", srv.patches)
	}
	if len(srv.puts) != 0 {
		t.Errorf("no volume is written: %v", srv.puts)
	}
	if (statusTarget{}).publisher(nil, time.Hour) != nil {
		t.Error("with neither flag the daemon publishes nothing")
	}
}

// statusVolumeServer is a server that lists instances and reads one status volume, for `tink helper status`.
type statusVolumeServer struct {
	instancesServer
	doc string
	err error
}

func (s statusVolumeServer) UseProject(string) incus.InstanceServer { return s }

func (s statusVolumeServer) GetStoragePoolVolume(string, string, string) (*api.StorageVolume, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	v := &api.StorageVolume{Name: "helper-status"}
	v.Config = map[string]string{helper.StatusKey: s.doc}
	return v, "", nil
}

func pointedAtVolume(inst api.Instance) api.Instance {
	inst.Config[helper.StatusVolumeKey] = "default/helper-status"
	delete(inst.Config, helper.StatusKey)
	return inst
}

func TestHelperStatusReadsTheVolumeAndSaysWhereAndSaysWhenItCannot(t *testing.T) {
	doc, _ := helper.Encode(*goodStatus(nil))
	inst := pointedAtVolume(helperInstance("tink-helper", "helper", "Running", nil))
	srv := statusVolumeServer{instancesServer: instancesServer{all: []api.Instance{inst}}, doc: doc}

	out, err := statusRun(t, srv, "", "", true, false)
	if err != nil || !strings.HasPrefix(out, "healthy: tink-helper/helper") {
		t.Fatalf("%v\n%s", err, out)
	}
	out, _ = statusRun(t, srv, "", "", false, false)
	if !strings.Contains(out, "status on:   volume default/helper-status") || !strings.Contains(out, "version:     v1") {
		t.Errorf("the details say where the document was read from:\n%s", out)
	}
	if out, _ = statusRun(t, srv, "", "", false, true); !strings.Contains(out, `"status_volume": "default/helper-status"`) {
		t.Errorf("and so does the JSON:\n%s", out)
	}

	srv.err = api.StatusErrorf(http.StatusForbidden, "not authorized")
	out, err = statusRun(t, srv, "", "", true, false)
	var exit *exitCodeError
	if !errors.As(err, &exit) || exit.code != int(helper.Degraded) {
		t.Errorf("an unreadable volume is degraded (exit 1), not healthy: %v", err)
	}
	if !strings.Contains(out, "degraded: tink-helper/helper: cannot read its status volume default/helper-status: not authorized") {
		t.Errorf("%s", out)
	}
}

func TestAHelperWhoseStatusVolumeCannotBeReadCannotTakeABackupRun(t *testing.T) {
	f := helper.Found{Project: "p", Name: "helper", State: "Running", Config: map[string]string{helper.MarkerKey: "1"},
		StatusVolume: "default/helper-status", StatusProblem: "not authorized"}
	r := helper.Evaluate(f, helperNow)
	why := helperCannotTake(r)
	if !strings.Contains(why, "status volume default/helper-status cannot be read") || strings.Contains(why, "has not published") {
		t.Errorf("%q", why)
	}
}
