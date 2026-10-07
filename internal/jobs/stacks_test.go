package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func stackYAML(volume string) []byte {
	return []byte(fmt.Sprintf("kind: backup-target\nname: nas\nlocation: other-host\nengine: incus\npool: nas\n---\nkind: storage-volume\nname: %s\nbackup:\n  copies:\n    - {target: nas, schedule: \"@daily\", retain: 30d}\n", volume))
}

func volumeNames(st Stack) []string {
	var out []string
	for _, r := range st.Resources {
		if r.Kind == "storage-volume" {
			out = append(out, r.Name)
		}
	}
	return out
}

func TestSyncThenLoad(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	if err := s.Sync("home", map[string][]byte{"tink.yaml": stackYAML("lib")}, []string{"tink.yaml"}, t0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Names(); len(got) != 1 || got[0] != "home" {
		t.Errorf("names = %v", got)
	}
	st, err := s.Load("home")
	if err != nil || len(volumeNames(st)) != 1 || volumeNames(st)[0] != "lib" || st.Version == "" || st.Name != "home" {
		t.Fatalf("%v %+v", err, st)
	}
	if !st.Synced.Equal(t0) {
		t.Errorf("synced = %v, want %v", st.Synced, t0)
	}
	// several stack files, and a file a stack references
	files := map[string][]byte{"a.yaml": stackYAML("one"), "b.yaml": []byte("kind: storage-volume\nname: two\nbackup:\n  none: scratch\n"), "data/x.txt": []byte("x")}
	if err := s.Sync("home", files, []string{"a.yaml", "b.yaml"}, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Load("home")
	if got := volumeNames(st); len(got) != 2 {
		t.Errorf("two stack files, both loaded: %v", got)
	}
}

func TestOnlyTheActiveAndPreviousVersionsAreKept(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	for i := 0; i < 5; i++ {
		if err := s.Sync("home", map[string][]byte{"tink.yaml": stackYAML(fmt.Sprintf("v%d", i))}, []string{"tink.yaml"}, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "home"))
	versions := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "v-") {
			versions++
		}
	}
	if versions != 2 {
		t.Errorf("%d versions kept, want the active one and the one before it", versions)
	}
	if st, _ := s.Load("home"); volumeNames(st)[0] != "v4" {
		t.Errorf("the newest must be active: %v", volumeNames(st))
	}
}

// A stack that does not parse must never replace one that does.
func TestABadSyncNeverReplacesAGoodStack(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	if err := s.Sync("home", map[string][]byte{"tink.yaml": stackYAML("good")}, []string{"tink.yaml"}, t0); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		files   map[string][]byte
		entries []string
		wantErr string
	}{
		"does not parse":                       {map[string][]byte{"tink.yaml": []byte("kind: storage-volume\nname: x\nnot_a_field: 1\n")}, []string{"tink.yaml"}, "does not load"},
		"an escaping source_path":              {map[string][]byte{"tink.yaml": []byte("kind: file\nname: f\ninstance: i\npath: /x\nsource_path: ../../../etc/passwd\n")}, []string{"tink.yaml"}, "outside the directory"},
		"a file name that leaves":              {map[string][]byte{"tink.yaml": stackYAML("x"), "../evil": []byte("x")}, []string{"tink.yaml"}, "leaves the directory"},
		"an entry that is not among the files": {map[string][]byte{"other.yaml": stackYAML("x")}, []string{"tink.yaml"}, "not among the files"},
		"no entries":                           {map[string][]byte{"tink.yaml": stackYAML("x")}, nil, "at least one"},
		"an absolute entry":                    {map[string][]byte{"/etc/x": stackYAML("x")}, []string{"/etc/x"}, "absolute"},
	} {
		err := s.Sync("home", tc.files, tc.entries, t0.Add(time.Hour))
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: %v, want an error containing %q", name, err, tc.wantErr)
		}
		if st, err := s.Load("home"); err != nil || volumeNames(st)[0] != "good" {
			t.Errorf("%s: the good stack must still be active: %v %v", name, err, volumeNames(st))
		}
	}
	// and a failed sync leaves no stray version directory
	entries, _ := os.ReadDir(filepath.Join(s.Dir, "home"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "v-") && len(entries) > 3 {
			t.Errorf("failed syncs must clean up after themselves: %v", entries)
			break
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "escape")); err == nil {
		t.Error("a file escaped")
	}
}

func TestSyncRefusesABadName(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	for _, bad := range []string{"", "Home", "../x", "a/b", ".hidden", strings.Repeat("a", 64)} {
		if err := s.Sync(bad, map[string][]byte{"t.yaml": stackYAML("x")}, []string{"t.yaml"}, t0); err == nil {
			t.Errorf("stack name %q must be refused", bad)
		}
	}
	if _, err := s.Load("../x"); err == nil {
		t.Error("loading a stack by a path-like name must be refused")
	}
}

// A version still being written has no READY and, not being linked from `current`, is never seen.
func TestAHalfWrittenVersionIsInvisible(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	if err := s.Sync("home", map[string][]byte{"tink.yaml": stackYAML("good")}, []string{"tink.yaml"}, t0); err != nil {
		t.Fatal(err)
	}
	half := filepath.Join(s.Dir, "home", "v-99999999T000000Z-zzzzzz")
	os.MkdirAll(half, 0o700)
	os.WriteFile(filepath.Join(half, "tink.yaml"), stackYAML("half"), 0o600)
	if st, err := s.Load("home"); err != nil || volumeNames(st)[0] != "good" {
		t.Errorf("an unfinished version must not be loaded: %v %v", err, volumeNames(st))
	}
	if _, err := loadVersion("home", half); err == nil || !strings.Contains(err.Error(), "READY") {
		t.Errorf("a version with no READY must not load: %v", err)
	}
}

func TestOneBrokenStackDoesNotHideTheOthers(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		if err := s.Sync(n, map[string][]byte{"tink.yaml": stackYAML(n)}, []string{"tink.yaml"}, t0); err != nil {
			t.Fatal(err)
		}
	}
	// bravo is damaged after the fact (a disk problem, a hand edit)
	vdir, _ := filepath.EvalSymlinks(filepath.Join(s.Dir, "bravo", "current"))
	os.WriteFile(filepath.Join(vdir, "tink.yaml"), []byte("kind: nonsense\nname: x\n"), 0o600)

	ok, bad := s.LoadAll()
	if len(ok) != 2 || ok[0].Name != "alpha" || ok[1].Name != "charlie" {
		t.Errorf("the healthy stacks must load: %+v", ok)
	}
	if err := bad["bravo"]; err == nil {
		t.Errorf("the broken stack must be reported by name: %v", bad)
	}
}

// While new versions are synced, a reader sees a whole stack every time: the old one or the new one.
func TestAReaderNeverSeesAMixtureWhileSyncing(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	if err := s.Sync("home", map[string][]byte{"tink.yaml": stackYAML("v0")}, []string{"tink.yaml"}, t0); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s.Sync("home", map[string][]byte{"tink.yaml": stackYAML(fmt.Sprintf("v%d", i))}, []string{"tink.yaml"}, t0.Add(time.Duration(i)*time.Second))
		}
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	loads := 0
	for time.Now().Before(deadline) {
		st, err := s.Load("home")
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("a reader failed to load while a sync was in progress: %v", err)
		}
		if got := volumeNames(st); len(got) != 1 || !strings.HasPrefix(got[0], "v") {
			close(stop)
			wg.Wait()
			t.Fatalf("a reader saw something that is neither version: %v", got)
		}
		loads++
	}
	close(stop)
	wg.Wait()
	if loads < 10 {
		t.Errorf("the reader barely ran (%d loads)", loads)
	}
}

func TestLoadingAStackThatWasNeverSynced(t *testing.T) {
	s := Stacks{Dir: t.TempDir()}
	if _, err := s.Load("nope"); err == nil || !strings.Contains(err.Error(), "no active version") {
		t.Errorf("%v", err)
	}
	if got, _ := (Stacks{Dir: filepath.Join(s.Dir, "missing")}).Names(); len(got) != 0 {
		t.Error("a missing directory has no stacks")
	}
}
