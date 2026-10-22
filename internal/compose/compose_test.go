package compose

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/minihci/tink/internal/backupmeta" // registers the backup capability that loading a stack asks for (cmd/tink/capabilities.go)
	"github.com/minihci/tink/internal/resolve"
)

var update = flag.Bool("update", false, "rewrite the golden files from what the importer writes now")

// TestSurvey runs the importer over every compose file of the survey (testdata/*/compose.yaml, copied as published), writes down what it did, and
// checks the result against the golden files. A file the importer refuses is part of what is recorded: refused.golden.txt names each key and why.
// What is imported must also be a stack tink accepts: it is loaded with the real loader.
func TestSurvey(t *testing.T) {
	dirs, err := filepath.Glob("testdata/*/compose.yaml")
	if err != nil || len(dirs) == 0 {
		t.Fatalf("no survey files found: %v", err)
	}
	imported, refused := 0, 0
	for _, f := range dirs {
		dir := filepath.Dir(f)
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			res, err := Import(src, Options{Dir: dir, Stack: name})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			var messages strings.Builder
			for _, w := range res.Warnings {
				messages.WriteString("warning: " + w + "\n")
			}
			for _, n := range res.Notes {
				messages.WriteString("note: " + n + "\n")
			}
			compare(t, filepath.Join(dir, "messages.golden.txt"), messages.String())
			if len(res.Refused) > 0 {
				refused++
				var r strings.Builder
				for _, x := range res.Refused {
					r.WriteString(x.String() + "\n")
				}
				if res.YAML != "" {
					t.Error("a refused file must not produce a stack")
				}
				compare(t, filepath.Join(dir, "refused.golden.txt"), r.String())
				_ = os.Remove(filepath.Join(dir, "stack.golden.yaml"))
				return
			}
			imported++
			compare(t, filepath.Join(dir, "stack.golden.yaml"), res.YAML)
			// What was written is a stack the loader accepts.
			out := filepath.Join(t.TempDir(), "tink.yaml")
			if err := os.WriteFile(out, []byte(res.YAML), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := resolve.LoadFile(out); err != nil {
				t.Errorf("the imported stack does not load: %v\n%s", err, res.YAML)
			}
		})
	}
	t.Logf("survey: %d of %d compose files import, %d are refused", imported, imported+refused, refused)
}

// compare checks got against the golden file (or writes it with -update). A refused-file golden that is no longer needed is removed with -update.
func compare(t *testing.T, path, got string) {
	t.Helper()
	if *update {
		if got == "" {
			_ = os.Remove(path)
			return
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		if got == "" && os.IsNotExist(err) {
			return
		}
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from what the importer writes now (run with -update and review the diff):\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}
