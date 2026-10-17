package resolve

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/helper"
)

// helperKindServer is a server that lists instances: GetInstancesAllProjects for helper.Find, and GetInstance in the project UseProject chose.
type helperKindServer struct {
	incus.InstanceServer
	project   string
	instances []api.Instance
	// volumes are the status volumes a helper may point at, by "project/pool/name" (the project UseProject chose): their config, or an
	// error to read them with.
	volumes map[string]map[string]string
	volErr  map[string]error
}

func (s *helperKindServer) GetStoragePoolVolume(pool, _, name string) (*api.StorageVolume, string, error) {
	key := s.project + "/" + pool + "/" + name
	if err := s.volErr[key]; err != nil {
		return nil, "", err
	}
	cfg, ok := s.volumes[key]
	if !ok {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "Storage volume not found")
	}
	v := &api.StorageVolume{Name: name}
	v.Config = cfg
	return v, "", nil
}

func (s *helperKindServer) UseProject(p string) incus.InstanceServer {
	c := *s
	c.project = p
	return &c
}

func (s *helperKindServer) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return s.instances, nil
}

func (s *helperKindServer) GetInstance(name string) (*api.Instance, string, error) {
	for _, i := range s.instances {
		if i.Project == s.project && i.Name == name {
			x := i
			return &x, "", nil
		}
	}
	return nil, "", api.StatusErrorf(http.StatusNotFound, "Instance not found")
}

var helperNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// helperInstance is a helper as Incus lists it: the marker, and a status document published at tick.
func helperInstance(t *testing.T, project, name, state string, st helper.Status) api.Instance {
	t.Helper()
	doc, err := helper.Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	return api.Instance{Name: name, Project: project, Status: state, InstancePut: api.InstancePut{Config: map[string]string{
		helper.MarkerKey: "true", helper.StatusKey: doc}}}
}

func goodStatus() helper.Status {
	return helper.Status{Proto: 1, Version: "v0.1.4", Tick: helperNow.Add(-2 * time.Minute), HeartbeatSeconds: 600,
		Remotes: []helper.Remote{{Name: "host"}, {Name: "vps"}}}
}

func helperRes(mod func(*Resource)) Resource {
	r := Resource{Kind: KindHelper, Name: "helper", Project: "tink-helper", Timezone: "America/Denver", Remotes: []string{"host", "vps"}}
	if mod != nil {
		mod(&r)
	}
	return r
}

func joined(ss []string) string { return strings.Join(ss, " | ") }

func TestPlanHelperInstallsWhenThereIsNone(t *testing.T) {
	p, err := planHelper(&helperKindServer{}, helperRes(nil), helperNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionCreate || !strings.Contains(joined(p.Changes), "install the daemon helper tink-helper/helper") {
		t.Errorf("plan = %+v", p)
	}
	if !strings.Contains(joined(p.Warnings), `remote "vps"`) || strings.Contains(joined(p.Warnings), `remote "host"`) {
		t.Errorf("only the remote that needs another server's token is warned about: %q", joined(p.Warnings))
	}
	if strings.Contains(joined(p.Warnings), "timezone") {
		t.Errorf("a timezone was given: %q", joined(p.Warnings))
	}
	p, _ = planHelper(&helperKindServer{}, helperRes(func(r *Resource) { r.Timezone = "" }), helperNow)
	if !strings.Contains(joined(p.Warnings), "no timezone") {
		t.Errorf("no timezone should be said: %q", joined(p.Warnings))
	}
}

func TestPlanHelperAdoptsAHealthyOneAndChangesNothing(t *testing.T) {
	srv := &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Running", goodStatus())}}
	p, err := planHelper(srv, helperRes(nil), helperNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNone || len(p.Warnings) != 0 || len(p.Blocked) != 0 {
		t.Fatalf("plan = %+v", p)
	}
	n := joined(p.Notes)
	if !strings.Contains(n, "the daemon helper tink-helper/helper is healthy") || !strings.Contains(n, "it runs v0.1.4;") || !strings.Contains(n, "tink helper upgrade") {
		t.Errorf("notes = %q", n)
	}
}

func TestPlanHelperWarnsWhenItIsNotWellButStillChangesNothing(t *testing.T) {
	stopped := &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Stopped", goodStatus())}}
	p, _ := planHelper(stopped, helperRes(nil), helperNow)
	if p.Action != ActionNone || !strings.Contains(joined(p.Warnings), "the daemon helper is down") {
		t.Errorf("stopped: %+v", p)
	}
	silent := goodStatus()
	silent.Tick = helperNow.Add(-3 * time.Hour)
	srv := &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Running", silent)}}
	p, _ = planHelper(srv, helperRes(nil), helperNow)
	if p.Action != ActionNone || !strings.Contains(joined(p.Warnings), "stale") {
		t.Errorf("silent: %+v", p)
	}
	failing := goodStatus()
	failing.Failing = []helper.Failing{{Volume: "identity-data", Target: "offsite", Count: 3, Since: helperNow.Add(-time.Hour)}}
	srv = &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Running", failing)}}
	p, _ = planHelper(srv, helperRes(nil), helperNow)
	if p.Action != ActionNone || !strings.Contains(joined(p.Warnings), "degraded") {
		t.Errorf("failing a copy: %+v", p)
	}
}

// A helper whose status document is on a volume (the default since the helper stopped holding a right to edit its instance) is planned
// exactly as one that keeps it on the instance: healthy when the volume has a fresh document, degraded with the reason when it cannot be read.
func TestPlanHelperReadsAStatusVolumeTheInstancePointsAt(t *testing.T) {
	inst := helperInstance(t, "tink-helper", "helper", "Running", helper.Status{})
	delete(inst.Config, helper.StatusKey)
	inst.Config[helper.StatusVolumeKey] = "default/helper-status"
	doc, err := helper.Encode(goodStatus())
	if err != nil {
		t.Fatal(err)
	}
	srv := &helperKindServer{instances: []api.Instance{inst}, volumes: map[string]map[string]string{"tink-helper/default/helper-status": {helper.StatusKey: doc}}}
	p, err := planHelper(srv, helperRes(nil), helperNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNone || len(p.Warnings) != 0 || !strings.Contains(joined(p.Notes), "the daemon helper tink-helper/helper is healthy") || !strings.Contains(joined(p.Notes), "it runs v0.1.4;") {
		t.Errorf("read from the volume: %+v", p)
	}

	// not readable: degraded, with the reason, and nothing about "no status yet"; the listing itself does not fail
	srv.volErr = map[string]error{"tink-helper/default/helper-status": api.StatusErrorf(http.StatusForbidden, "not authorized")}
	p, err = planHelper(srv, helperRes(nil), helperNow)
	if err != nil {
		t.Fatalf("an unreadable status volume must not make the plan fail: %v", err)
	}
	w := joined(p.Warnings)
	if p.Action != ActionNone || !strings.Contains(w, "the daemon helper is degraded: cannot read its status volume default/helper-status: not authorized") || strings.Contains(w, "has not published") {
		t.Errorf("%+v", p)
	}
	if !strings.Contains(joined(p.Notes), "is degraded") {
		t.Errorf("notes = %q", joined(p.Notes))
	}
}

func TestPlanHelperChecksTheRemotesTheStackSays(t *testing.T) {
	noVPS := goodStatus()
	noVPS.Remotes = []helper.Remote{{Name: "host"}}
	srv := &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Running", noVPS)}}
	p, _ := planHelper(srv, helperRes(nil), helperNow)
	if !strings.Contains(joined(p.Warnings), `needs remote "vps"`) || !strings.Contains(joined(p.Warnings), "tink helper remote add vps") || strings.Contains(joined(p.Warnings), `remote "host"`) {
		t.Errorf("warnings = %q", joined(p.Warnings))
	}
	// an older helper that does not say what it reaches is not checked
	older := goodStatus()
	older.Remotes = nil
	srv = &helperKindServer{instances: []api.Instance{helperInstance(t, "tink-helper", "helper", "Running", older)}}
	if p, _ = planHelper(srv, helperRes(nil), helperNow); len(p.Warnings) != 0 {
		t.Errorf("a helper that did not say is not checked: %q", joined(p.Warnings))
	}
}

func TestPlanHelperIsBlockedByAnInstanceOfThatNameThatIsNotAHelper(t *testing.T) {
	srv := &helperKindServer{instances: []api.Instance{{Name: "helper", Project: "tink-helper", Status: "Running"}}}
	p, err := planHelper(srv, helperRes(nil), helperNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionBlocked || !strings.Contains(joined(p.Blocked), "is not a helper") {
		t.Errorf("plan = %+v", p)
	}
	// the same name in another project is no obstacle
	srv = &helperKindServer{instances: []api.Instance{{Name: "helper", Project: "elsewhere"}}}
	if p, _ = planHelper(srv, helperRes(nil), helperNow); p.Action != ActionCreate {
		t.Errorf("another project: %+v", p)
	}
}

func TestPlanHelperDefaultsTheProjectAndLooksOnlyThere(t *testing.T) {
	other := helperInstance(t, "somewhere-else", "helper", "Running", goodStatus())
	srv := &helperKindServer{instances: []api.Instance{other}}
	p, _ := planHelper(srv, helperRes(func(r *Resource) { r.Project = "" }), helperNow)
	if p.Action != ActionCreate || !strings.Contains(joined(p.Changes), helper.DefaultProject+"/helper") {
		t.Errorf("a helper in another project is not this one: %+v", p)
	}
}

func TestCreateHelperPassesWhatTheStackSaysToTheInstaller(t *testing.T) {
	var got helper.InstallOptions
	old := installHelper
	installHelper = func(_ incus.InstanceServer, o helper.InstallOptions) error { got = o; return nil }
	defer func() { installHelper = old }()
	r := helperRes(func(r *Resource) { r.Pool, r.Network, r.Image = "fast", "incusbr0", "ghcr:minihci/tink-helper:v0.1.4" })
	if err := createHelper(&helperKindServer{}, r); err != nil {
		t.Fatal(err)
	}
	want := helper.InstallOptions{Project: "tink-helper", Name: "helper", Pool: "fast", Network: "incusbr0", Image: "ghcr:minihci/tink-helper:v0.1.4", TZ: "America/Denver"}
	if got != want {
		t.Errorf("options = %+v, want %+v", got, want)
	}
	if err := createHelper(&helperKindServer{}, helperRes(func(r *Resource) { r.Project = "" })); err != nil || got.Project != helper.DefaultProject {
		t.Errorf("default project: %v %q", err, got.Project)
	}
}

func TestAHelperDocumentLoadsAndIsPartOfTheGraph(t *testing.T) {
	rs, err := loadStack(t, "kind: stack\nname: p\nproject: tink\n---\nkind: helper\nname: helper\nproject: tink-helper\ntimezone: America/Denver\nnetwork: incusbr0\npool: default\nimage: ghcr:minihci/tink-helper:v0.1.4\nremotes: [host, vps]\n")
	if err != nil {
		t.Fatal(err)
	}
	var h Resource
	for _, r := range rs {
		if r.Kind == KindHelper {
			h = r
		}
	}
	if h.Name != "helper" || h.Project != "tink-helper" || h.Timezone != "America/Denver" || h.Network != "incusbr0" || h.Pool != "default" || h.Image == "" || len(h.Remotes) != 2 {
		t.Errorf("helper = %+v", h)
	}
	if levels, err := Levels(rs); err != nil || len(levels) != 1 || levels[0][0].Kind != KindHelper {
		t.Errorf("levels %v err %v: a helper is planned like any resource", levels, err)
	}
}

func TestAHelperDoesNotTakeTheStacksDefaultProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.yaml")
	if err := os.WriteFile(path, []byte("kind: stack\nname: p\nproject: tink\n---\nkind: helper\nname: helper\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Kind == KindHelper && r.Project != "" {
			t.Errorf("the helper is not an app of the stack: project = %q, want it left to helper.DefaultProject", r.Project)
		}
	}
}

func TestAMalformedHelperIsRejectedAtLoad(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"a name that is not an instance name", "kind: helper\nname: -bad\n", "instance name"},
		{"a time zone nobody knows", "kind: helper\nname: helper\ntimezone: Mars/Olympus\n", "time zone"},
		{"a remote listed twice", "kind: helper\nname: helper\nremotes: [vps, vps]\n", "twice"},
		{"an empty remote name", "kind: helper\nname: helper\nremotes: [\"\"]\n", "empty name"},
		{"a field of another kind", "kind: helper\nname: helper\ncontent: x\n", `does not use field "Content"`},
		{"timezone on an instance", "kind: instance\nname: a\nimage: docker-oci:x:1\ntimezone: UTC\n", `does not use field "Timezone"`},
		{"remotes on a publish", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [f]\nremotes: [x]\n", `does not use field "Remotes"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadStack(t, c.doc)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}
