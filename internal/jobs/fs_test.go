package jobs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"
)

// memFS is a jobs directory held in memory and reached the way a remote client reaches one: one call per file, no recursion, a
// directory must exist before a file goes into it. failWrite makes the named file fail to write.
type memFS struct {
	files     map[string][]byte
	dirs      map[string]bool
	failWrite string
	reads     int
}

func newMemFS() *memFS { return &memFS{files: map[string][]byte{}, dirs: map[string]bool{"": true}} }

func (m *memFS) ReadFile(name string) ([]byte, error) {
	m.reads++
	b, ok := m.files[name]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	return b, nil
}

func (m *memFS) WriteFile(name string, data []byte) error {
	if !m.dirs[path.Dir(name)] && path.Dir(name) != "." {
		return &fs.PathError{Op: "write", Path: name, Err: fs.ErrNotExist}
	}
	if m.failWrite != "" && path.Base(name) == m.failWrite {
		return fmt.Errorf("write %s: refused", name)
	}
	m.files[name] = append([]byte(nil), data...)
	return nil
}

func (m *memFS) Mkdir(name string) error { m.dirs[name] = true; return nil }

func (m *memFS) Remove(name string) error {
	if m.dirs[name] {
		for f := range m.files {
			if path.Dir(f) == name {
				return fmt.Errorf("%s: directory not empty", name)
			}
		}
		delete(m.dirs, name)
		return nil
	}
	delete(m.files, name)
	return nil
}

func (m *memFS) Exists(name string) (bool, error) {
	m.reads++
	_, f := m.files[name]
	return f || m.dirs[name], nil
}

func (m *memFS) ReadDir(name string) ([]string, error) {
	if !m.dirs[name] {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	seen := map[string]bool{}
	for f := range m.files {
		if name == "" || strings.HasPrefix(f, name+"/") {
			seen[strings.SplitN(strings.TrimPrefix(f, name+"/"), "/", 2)[0]] = true
		}
	}
	for d := range m.dirs {
		if d != "" && path.Dir(d) == path.Clean(name) || (name == "" && !strings.Contains(d, "/") && d != "") {
			seen[path.Base(d)] = true
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func TestAClientReachingTheDirectoryByFileCalls(t *testing.T) {
	m := newMemFS()
	s := Store{FS: m}

	id, err := s.Enqueue(Request{Kind: "backup-run", Origin: OriginTrigger, Args: []byte(`{"volumes":["p/v"]}`)}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.files[id+"/READY"]; !ok {
		t.Fatal("READY must be written")
	}
	if st, err := s.Status(id); err != nil || st.State != Queued || st.Origin != OriginTrigger {
		t.Fatalf("queued job: %+v %v", st, err)
	}
	if list, err := s.List(); err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list: %+v %v", list, err)
	}
	if pending, err := s.Pending("backup-run"); err != nil || !pending {
		t.Fatalf("pending: %v %v", pending, err)
	}

	if err := s.Cancel(id); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.files[id+"/cancel"]; !ok {
		t.Fatal("cancel must be written")
	}
	if err := s.Cancel("20260101T000000Z-nothere"); err == nil || !strings.Contains(err.Error(), "no such job") {
		t.Fatalf("cancelling a job that is not there: %v", err)
	}

	// what the executor leaves
	m.files[id+"/status.json"] = []byte(`{"proto":1,"id":"` + id + `","state":"succeeded","kind":"backup-run"}`)
	m.files[id+"/log"] = []byte("copied\n")
	if st, _ := s.Status(id); st.State != Succeeded {
		t.Fatalf("a finished job: %+v", st)
	}
	if log, err := s.Log(id); err != nil || log != "copied\n" {
		t.Fatalf("log: %q %v", log, err)
	}
	if pending, _ := s.Pending("backup-run"); pending {
		t.Fatal("a finished job is not pending")
	}

	if s.Draining() {
		t.Fatal("not draining")
	}
	m.files["DRAIN"] = nil
	if !s.Draining() {
		t.Fatal("DRAIN must be seen")
	}
}

func TestAFinishedJobIsOneReadOverTheNetwork(t *testing.T) {
	m := newMemFS()
	m.dirs["20260101T000000Z-aaaaaa"] = true
	m.files["20260101T000000Z-aaaaaa/status.json"] = []byte(`{"proto":1,"id":"20260101T000000Z-aaaaaa","state":"failed"}`)
	if _, err := (Store{FS: m}).Status("20260101T000000Z-aaaaaa"); err != nil {
		t.Fatal(err)
	}
	if m.reads != 1 {
		t.Errorf("each read is a call that leaves an event in the host's log; a finished job took %d", m.reads)
	}
}

func TestAJobIsNotSeenBeforeREADYThroughTheFileCalls(t *testing.T) {
	m := newMemFS()
	s := Store{FS: m}
	var seen []Status
	beforeREADY = func(string) { seen, _ = s.List() }
	defer func() { beforeREADY = nil }()
	if _, err := s.Enqueue(Request{Kind: "backup-run"}, t0); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("a reader saw a job between request.json and READY: %+v", seen)
	}
}

func TestAFailedEnqueueLeavesNothingBehind(t *testing.T) {
	for _, failing := range []string{"request.json", "READY"} {
		m := newMemFS()
		m.failWrite = failing
		if _, err := (Store{FS: m}).Enqueue(Request{Kind: "backup-run"}, t0); err == nil {
			t.Fatalf("a refused write of %s must fail the enqueue", failing)
		}
		if len(m.files) != 0 || len(m.dirs) != 1 {
			t.Errorf("after a refused %s: files %v dirs %v", failing, m.files, m.dirs)
		}
	}
}

func TestAnAbsentDirectoryHasNoJobs(t *testing.T) {
	m := newMemFS()
	delete(m.dirs, "")
	list, err := (Store{FS: m}).List()
	if err != nil || len(list) != 0 {
		t.Fatalf("%v %v", list, err)
	}
	if !errors.Is(&fs.PathError{Err: fs.ErrNotExist}, fs.ErrNotExist) {
		t.Fatal("sanity")
	}
}
