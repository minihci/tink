package resolve

import (
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// treeFake is an Incus server whose instance "ha" and custom volume default/vol each hold a small tree, and which answers like Incus does: a write
// whose parent directory is missing is 404, and so is a read of a path that is not there. Any other method panics (nil embedded interface).
type treeFake struct {
	incus.InstanceServer
	instance tree
	volume   tree
	log      []string
	mkdirErr error // makes every directory creation fail
	pushErr  error // makes every file write fail with this, before it looks at the parents
	lastDir  incus.InstanceFileArgs
}

type tree struct {
	dirs  map[string]bool
	files map[string]string
}

func newTree(dirs ...string) tree {
	t := tree{dirs: map[string]bool{"/": true}, files: map[string]string{}}
	for _, d := range dirs {
		t.dirs[d] = true
	}
	return t
}

func newTreeFake() *treeFake {
	return &treeFake{instance: newTree(), volume: newTree()}
}

func (s *treeFake) UseProject(string) incus.InstanceServer { return s }

func parentOf(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

func (t tree) read(p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	if t.dirs[p] {
		return nil, &incus.InstanceFileResponse{Type: "directory"}, nil
	}
	if c, ok := t.files[p]; ok {
		return io.NopCloser(strings.NewReader(c)), &incus.InstanceFileResponse{Type: "file"}, nil
	}
	return nil, nil, api.StatusErrorf(http.StatusNotFound, "not found")
}

func (t tree) write(p string, args incus.InstanceFileArgs) error {
	if !t.dirs[parentOf(p)] {
		return api.StatusErrorf(http.StatusNotFound, "no such file or directory")
	}
	if args.Type == "directory" {
		t.dirs[p] = true
		return nil
	}
	b, _ := io.ReadAll(args.Content)
	t.files[p] = string(b)
	return nil
}

func (s *treeFake) GetInstanceFile(instance, p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	s.log = append(s.log, "stat "+p)
	return s.instance.read(p)
}

func (s *treeFake) CreateInstanceFile(instance, p string, args incus.InstanceFileArgs) error {
	if args.Type == "directory" {
		s.log = append(s.log, "mkdir "+p)
		s.lastDir = args
		if s.mkdirErr != nil {
			return s.mkdirErr
		}
	} else {
		s.log = append(s.log, "push "+p)
		if s.pushErr != nil {
			return s.pushErr
		}
	}
	return s.instance.write(p, args)
}

func (s *treeFake) GetStorageVolumeFile(pool, volType, volume, p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	s.log = append(s.log, "stat "+p)
	return s.volume.read(p)
}

func (s *treeFake) CreateStorageVolumeFile(pool, volType, volume, p string, args incus.InstanceFileArgs) error {
	if args.Type == "directory" {
		s.log = append(s.log, "mkdir "+p)
		s.lastDir = args
		if s.mkdirErr != nil {
			return s.mkdirErr
		}
	} else {
		s.log = append(s.log, "write "+p)
	}
	return s.volume.write(p, args)
}

func fileAtPath(p string) Resource {
	return Resource{Kind: KindFile, Name: "f", Instance: "ha", Project: "p", Path: p, Content: "content\n"}
}

func TestPlaceFile_MakesTheMissingParents(t *testing.T) {
	t.Run("nothing missing: one push, no reads, no directories", func(t *testing.T) {
		s := newTreeFake()
		s.instance.dirs["/config"] = true
		if err := placeFile(s, fileAtPath("/config/a.yaml")); err != nil {
			t.Fatal(err)
		}
		if want := []string{"push /config/a.yaml"}; !reflect.DeepEqual(s.log, want) {
			t.Errorf("calls = %v, want %v", s.log, want)
		}
	})

	t.Run("one level missing: it is made, then the push is repeated", func(t *testing.T) {
		s := newTreeFake()
		s.instance.dirs["/config"] = true
		if err := placeFile(s, fileAtPath("/config/.storage/http")); err != nil {
			t.Fatal(err)
		}
		want := []string{"push /config/.storage/http", "stat /config/.storage", "stat /config", "mkdir /config/.storage", "push /config/.storage/http"}
		if !reflect.DeepEqual(s.log, want) {
			t.Errorf("calls = %v, want %v", s.log, want)
		}
		if got := s.instance.files["/config/.storage/http"]; got != "content\n" {
			t.Errorf("file holds %q", got)
		}
		if a := s.lastDir; a.Type != "directory" || a.Mode != 0o755 {
			t.Errorf("directory args = %+v, want a 0755 directory", a)
		}
	})

	t.Run("several levels missing: made top down, and only the ones that are not there", func(t *testing.T) {
		s := newTreeFake()
		s.instance.dirs["/etc"] = true
		if err := placeFile(s, fileAtPath("/etc/caddy/routes/apps.caddy")); err != nil {
			t.Fatal(err)
		}
		var made []string
		for _, c := range s.log {
			if strings.HasPrefix(c, "mkdir ") {
				made = append(made, c)
			}
		}
		if want := []string{"mkdir /etc/caddy", "mkdir /etc/caddy/routes"}; !reflect.DeepEqual(made, want) {
			t.Errorf("directories made = %v, want %v", made, want)
		}
		if s.instance.files["/etc/caddy/routes/apps.caddy"] == "" {
			t.Error("the file was not written")
		}
	})

	t.Run("a file that sits in the way is an error that names it, and nothing is made", func(t *testing.T) {
		s := newTreeFake()
		s.instance.files["/config"] = "a file"
		err := placeFile(s, fileAtPath("/config/sub/a.yaml"))
		if err == nil || !strings.Contains(err.Error(), "/config is not a directory") {
			t.Fatalf("err = %v", err)
		}
		for _, c := range s.log {
			if strings.HasPrefix(c, "mkdir ") {
				t.Errorf("made a directory: %v", s.log)
			}
		}
	})

	t.Run("a directory that cannot be made: the error names it and says why the push failed", func(t *testing.T) {
		s := newTreeFake()
		s.mkdirErr = errors.New("permission denied")
		err := placeFile(s, fileAtPath("/config/.storage/http"))
		if err == nil {
			t.Fatal("no error")
		}
		for _, want := range []string{"pushing /config/.storage/http to ha", "parent directory is missing", "creating directory /config", "permission denied"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, missing %q", err, want)
			}
		}
	})

	t.Run("a failure that is not 'not found' is returned as it came, with no directory made", func(t *testing.T) {
		s := newTreeFake()
		s.pushErr = api.StatusErrorf(http.StatusInternalServerError, "disk full")
		err := placeFile(s, fileAtPath("/config/a.yaml"))
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err = %v", err)
		}
		if want := []string{"push /config/a.yaml"}; !reflect.DeepEqual(s.log, want) {
			t.Errorf("calls = %v, want one push", s.log)
		}
	})

	t.Run("not found with every parent in place (the instance is gone, say): one retry is not made", func(t *testing.T) {
		s := newTreeFake()
		s.pushErr = api.StatusErrorf(http.StatusNotFound, "instance not found")
		s.instance.dirs["/config"] = true
		err := placeFile(s, fileAtPath("/config/a.yaml"))
		if err == nil || !strings.Contains(err.Error(), "instance not found") {
			t.Fatalf("err = %v", err)
		}
		pushes := 0
		for _, c := range s.log {
			if strings.HasPrefix(c, "push ") {
				pushes++
			}
		}
		if pushes != 1 {
			t.Errorf("pushes = %d, want 1 (calls %v)", pushes, s.log)
		}
	})
}

func TestPlaceVolumeFile_MakesTheMissingParents(t *testing.T) {
	m := mount{Device: "config", Path: "/config", Pool: "default", Volume: "vol"}

	t.Run("a directory of the volume is made, with the path relative to the volume's root", func(t *testing.T) {
		s := newTreeFake()
		f := fileAtPath("/config/.storage/http")
		if err := placeVolumeFile(s, f, m); err != nil {
			t.Fatal(err)
		}
		if got := s.volume.files["/.storage/http"]; got != "content\n" {
			t.Errorf("volume holds %q", got)
		}
		if !s.volume.dirs["/.storage"] {
			t.Errorf("directory not made (calls %v)", s.log)
		}
	})

	t.Run("a directory that cannot be made: the error names the volume and the directory", func(t *testing.T) {
		s := newTreeFake()
		s.mkdirErr = errors.New("read-only")
		err := placeVolumeFile(s, fileAtPath("/config/sub/x"), m)
		if err == nil {
			t.Fatal("no error")
		}
		for _, want := range []string{"writing /sub/x into volume default/vol failed", "creating directory /sub", "read-only"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, missing %q", err, want)
			}
		}
	})

	t.Run("parents in place: the file is written, nothing else", func(t *testing.T) {
		s := newTreeFake()
		s.volume.dirs["/sub"] = true
		if err := placeVolumeFile(s, fileAtPath("/config/sub/x"), m); err != nil {
			t.Fatal(err)
		}
		for _, c := range s.log {
			if strings.HasPrefix(c, "mkdir ") {
				t.Errorf("made a directory: %v", s.log)
			}
		}
	})
}

func TestMakeParents_RootAndTopLevelFilesNeedNothing(t *testing.T) {
	ops := dirOps{
		stat:  func(string) (bool, bool, error) { t.Fatal("stat called"); return false, false, nil },
		mkdir: func(string) error { t.Fatal("mkdir called"); return nil },
	}
	for _, p := range []string{"/x", "/"} {
		if made, err := makeParents(p, ops); err != nil || len(made) != 0 {
			t.Errorf("%s: made %v, err %v", p, made, err)
		}
	}
}

func TestMakeParents_ReadFailureIsNotAbsence(t *testing.T) {
	ops := dirOps{
		stat:  func(string) (bool, bool, error) { return false, false, errors.New("connection reset") },
		mkdir: func(string) error { t.Fatal("mkdir called after a failed read"); return nil },
	}
	if _, err := makeParents("/a/b/c", ops); err == nil || !strings.Contains(err.Error(), "reading /a/b") {
		t.Fatalf("err = %v", err)
	}
}
