package ingress

import (
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// fakeIngress is the file API of an ingress instance: a small filesystem, plus the Incus listing the reconcile discovers from, and the
// commands run in it. Anything else panics through the nil embedded interface.
type fakeIngress struct {
	incus.InstanceServer
	instance  string
	dirs      map[string]bool
	files     map[string]string
	instances []api.InstanceFull
	log       []string
	reloadErr string // output of a failing `caddy reload`
	denied    error  // every file read fails with this (a 403, say)
}

func newFakeIngress() *fakeIngress {
	return &fakeIngress{instance: "ingress", dirs: map[string]bool{"/etc/caddy/routes": true}, files: map[string]string{}}
}

func (f *fakeIngress) GetInstancesFullAllProjects(api.InstanceType) ([]api.InstanceFull, error) {
	return f.instances, nil
}

func (f *fakeIngress) GetInstanceFile(instance, p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	if f.denied != nil {
		return nil, nil, f.denied
	}
	if f.dirs[p] {
		var names []string
		for file := range f.files {
			if i := strings.LastIndex(file, "/"); file[:i] == p {
				names = append(names, file[i+1:])
			}
		}
		sort.Strings(names)
		return io.NopCloser(strings.NewReader("")), &incus.InstanceFileResponse{Type: "directory", Entries: names}, nil
	}
	if c, ok := f.files[p]; ok {
		return io.NopCloser(strings.NewReader(c)), &incus.InstanceFileResponse{Type: "file"}, nil
	}
	return nil, nil, api.StatusErrorf(http.StatusNotFound, "not found")
}

func (f *fakeIngress) CreateInstanceFile(instance, p string, args incus.InstanceFileArgs) error {
	if args.Type == "directory" {
		f.dirs[p] = true
		f.log = append(f.log, "mkdir "+p)
		return nil
	}
	b, _ := io.ReadAll(args.Content)
	f.files[p] = string(b)
	f.log = append(f.log, "write "+p)
	return nil
}

func (f *fakeIngress) DeleteInstanceFile(instance, p string) error {
	delete(f.files, p)
	f.log = append(f.log, "delete "+p)
	return nil
}

type reloadOp struct {
	incus.Operation
	code float64
}

func (o reloadOp) Wait() error { return nil }
func (o reloadOp) Get() api.Operation {
	return api.Operation{Metadata: map[string]any{"return": o.code}}
}

func (f *fakeIngress) ExecInstance(instance string, post api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	f.log = append(f.log, "exec "+strings.Join(post.Command, " "))
	code := 0.0
	if f.reloadErr != "" {
		code = 1
		args.Stdout.Write([]byte(f.reloadErr))
	}
	close(args.DataDone)
	return reloadOp{code: code}, nil
}

const routes = "/etc/caddy/routes/generated"

func TestApplyViaWritesWhatIsDesiredBeforeRemovingWhatIsStaleAndReloadsLast(t *testing.T) {
	f := newFakeIngress()
	f.dirs[routes] = true
	f.files[routes+"/old.caddy"] = "old"
	f.files[routes+"/keep.caddy"] = "same"
	f.files[routes+"/notes.txt"] = "not ours: not a route file"

	if err := applyVia(f, routes, "ingress", map[string]string{"keep.caddy": "changed", "new.caddy": "new"}); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(f.log, " | ")
	for _, want := range []string{"write " + routes + "/keep.caddy", "write " + routes + "/new.caddy", "delete " + routes + "/old.caddy"} {
		if !strings.Contains(log, want) {
			t.Errorf("%q missing from %s", want, log)
		}
	}
	lastWrite := strings.LastIndex(log, "write ")
	if strings.Index(log, "delete ") < lastWrite || strings.Index(log, "exec caddy reload") < strings.Index(log, "delete ") {
		t.Errorf("write the desired files, then remove the stale ones (there is never a moment with no routes), then reload: %s", log)
	}
	if f.files[routes+"/keep.caddy"] != "changed" || f.files[routes+"/new.caddy"] != "new" {
		t.Errorf("%v", f.files)
	}
	if _, has := f.files[routes+"/old.caddy"]; has {
		t.Error("the stale route is removed")
	}
	if f.files[routes+"/notes.txt"] == "" {
		t.Error("a file that is not a route file is left alone")
	}
	if !strings.Contains(log, "exec caddy reload --config /etc/caddy/Caddyfile") {
		t.Errorf("the reload is the same command as on the host: %s", log)
	}
}

func TestApplyViaCreatesTheGeneratedDirectoryTheFirstTime(t *testing.T) {
	f := newFakeIngress() // /etc/caddy/routes exists (the volume), generated/ does not
	if err := applyVia(f, routes, "ingress", map[string]string{"a.caddy": "x"}); err != nil {
		t.Fatal(err)
	}
	if !f.dirs[routes] || f.log[0] != "mkdir "+routes {
		t.Errorf("the directory is made before anything is written into it: %v", f.log)
	}
}

func TestReadCurrentViaReadsOnlyRouteFilesAndAMissingDirectoryIsNoRoutes(t *testing.T) {
	f := newFakeIngress()
	if got, err := readCurrentVia(f, "ingress", routes); err != nil || len(got) != 0 {
		t.Errorf("not there yet is no routes, as on the host: %v %v", got, err)
	}
	f.dirs[routes] = true
	f.files[routes+"/a.caddy"] = "A"
	f.files[routes+"/b.txt"] = "B"
	got, err := readCurrentVia(f, "ingress", routes)
	if err != nil || len(got) != 1 || got["a.caddy"] != "A" {
		t.Errorf("%v %v", got, err)
	}
}

func TestAFailedReadIsAnErrorAndNeverNoRoutes(t *testing.T) {
	f := newFakeIngress()
	f.denied = api.StatusErrorf(http.StatusForbidden, "not authorized")
	if _, err := readCurrentVia(f, "ingress", routes); err == nil || !errors.Is(err, f.denied) {
		t.Fatalf("err = %v: a failed read read as 'no routes' would have the next apply delete every route's file as stale... and write them all again", err)
	}
	// and so applying cannot be reached on one: nothing is written
	if err := applyVia(f, routes, "ingress", map[string]string{"a.caddy": "x"}); err == nil || len(f.files) != 0 {
		t.Errorf("%v %v", err, f.files)
	}
	f.denied = nil
	f.files[routes] = "a file where a directory should be"
	if _, err := readCurrentVia(f, "ingress", routes); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("%v", err)
	}
}

func TestAReloadThatFailsIsReportedAfterTheFilesAreInPlace(t *testing.T) {
	f := newFakeIngress()
	f.dirs[routes] = true
	f.reloadErr = "caddy: not found"
	err := applyVia(f, routes, "ingress", map[string]string{"a.caddy": "x"})
	if err == nil || !strings.Contains(err.Error(), "reloading caddy: exit 1") || !strings.Contains(err.Error(), "caddy: not found") {
		t.Errorf("%v", err)
	}
	if f.files[routes+"/a.caddy"] != "x" {
		t.Error("the files are in place; only the reload failed")
	}
}

func TestReconcileViaTheAPIDiscoversRendersWritesAndReloads(t *testing.T) {
	f := newFakeIngress()
	f.instances = []api.InstanceFull{instanceFixture("ns-caddy", map[string]string{
		"user.ingress.enabled": "true", "user.ingress.domain": "ns.example.test", "user.ingress.port": "8080",
	}, "10.0.0.7")}
	opts := Options{IngressInstance: "ingress", RoutesDir: routes, ViaAPI: true}

	res, err := reconcileOn(f, opts)
	if err != nil || !res.Applied || len(res.Registrations) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	var written string
	for p, c := range f.files {
		if strings.HasPrefix(p, routes+"/") {
			written = c
		}
	}
	if !strings.Contains(written, "ns.example.test") || !strings.Contains(written, "10.0.0.7:8080") {
		t.Errorf("the route that was rendered: %q", written)
	}

	// a second pass with nothing changed writes nothing and does not reload
	f.log = nil
	res, err = reconcileOn(f, opts)
	if err != nil || res.Applied || len(f.log) != 0 {
		t.Errorf("a pass that changes nothing touches nothing: %+v %v %v", res, err, f.log)
	}

	// a dry run computes and changes nothing
	f.instances = nil
	opts.DryRun = true
	res, err = reconcileOn(f, opts)
	if err != nil || res.Applied || len(res.Diff.Removed) != 1 || len(f.files) != 1 {
		t.Errorf("%+v %v %v", res, err, f.files)
	}
	// and when it is real, the route that no instance asks for any more is removed
	opts.DryRun = false
	if res, err = reconcileOn(f, opts); err != nil || !res.Applied || len(f.files) != 0 {
		t.Errorf("%+v %v %v", res, err, f.files)
	}
}

func TestAReconcileNamesTheInstancesStillOnTheOldKeysAndStillRoutesThem(t *testing.T) {
	f := newFakeIngress()
	f.instances = []api.InstanceFull{
		instanceFixture("old", map[string]string{"user.ingress.enabled": "true", "user.ingress.domain": "old.example.test"}, "10.0.0.7"),
		instanceFixture("new", map[string]string{KeyEnabled: "true", KeyDomain: "new.example.test"}, "10.0.0.8"),
	}
	res, err := reconcileOn(f, Options{IngressInstance: "ingress", RoutesDir: routes, ViaAPI: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Registrations) != 2 || strings.Join(res.Legacy, ",") != "old" {
		t.Errorf("both are routed, and only the old one is named: %+v", res)
	}
	if len(res.Warnings) != 0 {
		t.Errorf("the rename request is not a warning, which the daemon would repeat every pass: %v", res.Warnings)
	}
}
