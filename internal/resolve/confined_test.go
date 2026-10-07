package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const fileStack = "kind: file\nname: f\ninstance: i\npath: /etc/x\nsource_path: %s\n"

func TestLoadFileConfinedAllowsFilesInsideTheRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "data", "content.txt"), "hello")
	write(t, filepath.Join(root, "tink.yaml"), strings.Replace(fileStack, "%s", "data/content.txt", 1))
	rs, err := LoadFileConfined(filepath.Join(root, "tink.yaml"), root)
	if err != nil || len(rs) != 1 || rs[0].Content != "hello" {
		t.Fatalf("%v %+v", err, rs)
	}
}

func TestLoadFileConfinedRefusesWhatEscapes(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "do not read")
	for name, tc := range map[string]struct {
		source, wantErr string
	}{
		// the secret is a sibling of the root, so ../<its dir>/secret reaches it
		"a parent directory": {"../" + filepath.Base(outside) + "/secret", "outside the directory"},
		"a deeper way out":   {"a/../../" + filepath.Base(outside) + "/secret", "outside the directory"},
		// an absolute path is joined UNDER the stack's directory (filepath.Join), so it is never read; it is simply not found
		"an absolute path": {filepath.Join(outside, "secret"), "reading source_path"},
	} {
		root := t.TempDir()
		write(t, filepath.Join(root, "tink.yaml"), strings.Replace(fileStack, "%s", tc.source, 1))
		rs, err := LoadFileConfined(filepath.Join(root, "tink.yaml"), root)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: the stack must not be able to read %q: %v %+v", name, tc.source, err, rs)
		}
	}
}

func TestLoadFileConfinedRefusesASymlinkThatLeaves(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "do not read")
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "tink.yaml"), strings.Replace(fileStack, "%s", "link/secret", 1))
	if _, err := LoadFileConfined(filepath.Join(root, "tink.yaml"), root); err == nil || !strings.Contains(err.Error(), "outside the directory") {
		t.Errorf("a symlink out of the root must not be followed: %v", err)
	}
	// and a stack file that is itself a symlink out of the root
	other := filepath.Join(t.TempDir(), "stack.yaml")
	write(t, other, "kind: project\\nname: p\\n")
	if err := os.Symlink(other, filepath.Join(root, "evil.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFileConfined(filepath.Join(root, "evil.yaml"), root); err == nil {
		t.Error("a stack file reached through a symlink out of the root must be refused")
	}
}

func TestLoadFileConfinedImageSourceToo(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "tink.yaml"), "kind: image\nname: i\nalias: a\nsource: ../../escape.qcow2\n")
	if _, err := LoadFileConfined(filepath.Join(root, "tink.yaml"), root); err == nil || !strings.Contains(err.Error(), "outside the directory") {
		t.Errorf("an image source outside the root: %v", err)
	}
}

func TestPlainLoadFileIsUnchanged(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "x", "c.txt"), "plain")
	write(t, filepath.Join(root, "tink.yaml"), strings.Replace(fileStack, "%s", "x/c.txt", 1))
	if rs, err := LoadFile(filepath.Join(root, "tink.yaml")); err != nil || rs[0].Content != "plain" {
		t.Fatalf("%v", err)
	}
	// the operator's own stacks are not confined: a relative path may leave the directory, as it always could
	outside := t.TempDir()
	write(t, filepath.Join(outside, "c.txt"), "elsewhere")
	write(t, filepath.Join(root, "t2.yaml"), strings.Replace(fileStack, "%s", "../"+filepath.Base(outside)+"/c.txt", 1))
	if rs, err := LoadFile(filepath.Join(root, "t2.yaml")); err != nil || rs[0].Content != "elsewhere" {
		t.Fatalf("the operator's own stacks are not confined: %v", err)
	}
}
