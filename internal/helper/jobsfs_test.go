package helper

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path"
	"sort"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/jobs"
)

// fileServer answers the instance file API from memory, the way Incus does: a directory read lists its entries, an absent path is a
// 404, and a file can only go into a directory that exists.
type fileServer struct {
	incus.InstanceServer
	project string
	files   map[string][]byte
	dirs    map[string]bool
	calls   int
	modes   map[string]int
}

func newFileServer() *fileServer {
	return &fileServer{files: map[string][]byte{}, dirs: map[string]bool{"/data/jobs": true}, modes: map[string]int{}}
}

func (s *fileServer) UseProject(p string) incus.InstanceServer { s.project = p; return s }

func (s *fileServer) GetInstanceFile(inst, p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	s.calls++
	if inst != "helper" {
		return nil, nil, api.StatusErrorf(http.StatusNotFound, "instance not found")
	}
	if s.dirs[p] {
		var names []string
		for f := range s.files {
			if path.Dir(f) == p {
				names = append(names, path.Base(f))
			}
		}
		for d := range s.dirs {
			if d != p && path.Dir(d) == p {
				names = append(names, path.Base(d))
			}
		}
		sort.Strings(names)
		return nil, &incus.InstanceFileResponse{Type: "directory", Entries: names}, nil
	}
	b, ok := s.files[p]
	if !ok {
		return nil, nil, api.StatusErrorf(http.StatusNotFound, "not found")
	}
	return io.NopCloser(bytes.NewReader(b)), &incus.InstanceFileResponse{Type: "file"}, nil
}

func (s *fileServer) CreateInstanceFile(inst, p string, a incus.InstanceFileArgs) error {
	if !s.dirs[path.Dir(p)] {
		return api.StatusErrorf(http.StatusNotFound, "parent not found")
	}
	s.modes[p] = a.Mode
	if a.Type == "directory" {
		s.dirs[p] = true
		return nil
	}
	b, _ := io.ReadAll(a.Content)
	s.files[p] = b
	return nil
}

func (s *fileServer) DeleteInstanceFile(inst, p string) error {
	if _, ok := s.files[p]; !ok && !s.dirs[p] {
		return api.StatusErrorf(http.StatusNotFound, "not found")
	}
	delete(s.files, p)
	delete(s.dirs, p)
	return nil
}

func TestTheJobsProtocolOverTheInstanceFileAPI(t *testing.T) {
	srv := newFileServer()
	store, err := JobsStore(srv, Found{Project: "tink-helper", Name: "helper", State: "Running"})
	if err != nil {
		t.Fatal(err)
	}
	if srv.project != "tink-helper" {
		t.Errorf("the file calls must be made in the helper's project, not the connection's: %q", srv.project)
	}

	id, err := store.Enqueue(jobs.Request{Kind: "backup-run", Origin: jobs.OriginTrigger, Args: []byte(`{"volumes":["p/v"]}`)}, t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"request.json", "READY"} {
		p := "/data/jobs/" + id + "/" + n
		if _, ok := srv.files[p]; !ok {
			t.Errorf("%s was not written", p)
		}
		if srv.modes[p] != 0o600 {
			t.Errorf("%s: mode %o", p, srv.modes[p])
		}
	}
	if srv.modes["/data/jobs/"+id] != 0o700 {
		t.Errorf("the job directory is private: %o", srv.modes["/data/jobs/"+id])
	}
	if list, err := store.List(); err != nil || len(list) != 1 || list[0].State != jobs.Queued {
		t.Fatalf("%v %v", list, err)
	}
	if err := store.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := srv.files["/data/jobs/"+id+"/cancel"]; !ok {
		t.Error("cancel was not written")
	}
	if store.Draining() {
		t.Error("not draining")
	}
	srv.files["/data/jobs/DRAIN"] = nil
	if !store.Draining() {
		t.Error("DRAIN is on the volume and must be seen")
	}
}

func TestAbsentThingsAreNotFoundErrorsAndNotFailures(t *testing.T) {
	f := InstanceFS{Server: newFileServer(), Instance: "helper", Root: "/data/jobs"}
	if _, err := f.ReadFile("nope/status.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a 404 is os.ErrNotExist: %v", err)
	}
	if ok, err := f.Exists("nope"); ok || err != nil {
		t.Errorf("%v %v", ok, err)
	}
	if err := f.Remove("nope"); err != nil {
		t.Errorf("removing what is not there: %v", err)
	}
	if err := f.WriteFile("nodir/x", nil); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a file into a directory that is not there: %v", err)
	}
	// an instance that does not exist is also a 404, and a caller must be able to tell the difference when it matters
	g := InstanceFS{Server: newFileServer(), Instance: "gone", Root: "/data/jobs"}
	if _, err := g.ReadDir(""); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%v", err)
	}
}

func TestPickAndAStoppedHelper(t *testing.T) {
	a := Found{Project: "tink-helper", Name: "helper", State: "Running"}
	b := Found{Project: "other", Name: "helper2", State: "Running"}
	if got, err := Pick([]Found{a}, "", ""); err != nil || got.Name != "helper" {
		t.Errorf("%v %v", got, err)
	}
	if _, err := Pick(nil, "", ""); err == nil || !strings.Contains(err.Error(), "tink helper install") {
		t.Errorf("none: %v", err)
	}
	if _, err := Pick([]Found{a, b}, "", ""); err == nil || !strings.Contains(err.Error(), "--instance") {
		t.Errorf("several: %v", err)
	}
	if got, err := Pick([]Found{a, b}, "helper2", ""); err != nil || got.Project != "other" {
		t.Errorf("%v %v", got, err)
	}
	a.State = "Stopped"
	if _, err := JobsStore(newFileServer(), a); err == nil || !strings.Contains(err.Error(), "incus start") {
		t.Errorf("a stopped helper says how to start it: %v", err)
	}
}
