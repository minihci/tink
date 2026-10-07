package jobs

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

func put(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildBundleShipsTheStackAndEverythingItReads(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "stacks", "app.yaml"), "kind: file\nname: f\ninstance: i\npath: /etc/x\nsource_path: ../shared/x.txt\n---\nkind: storage-volume\nname: lib\nbackup:\n  none: scratch\n")
	put(t, filepath.Join(root, "stacks", "more.yaml"), "kind: storage-volume\nname: more\nbackup:\n  none: scratch\n")
	put(t, filepath.Join(root, "shared", "x.txt"), "shared content")
	put(t, filepath.Join(root, "unrelated", "big.bin"), "not referenced")

	b, err := BuildBundle([]string{filepath.Join(root, "stacks", "app.yaml"), filepath.Join(root, "stacks", "more.yaml")})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"shared/x.txt", "stacks/app.yaml", "stacks/more.yaml"}; !reflect.DeepEqual(b.Names(), want) {
		t.Errorf("files = %v, want %v (and not the unreferenced one)", b.Names(), want)
	}
	if want := []string{"stacks/app.yaml", "stacks/more.yaml"}; !reflect.DeepEqual(b.Entries, want) {
		t.Errorf("entries = %v, want %v", b.Entries, want)
	}
	if string(b.Files["shared/x.txt"]) != "shared content" {
		t.Error("a referenced file must carry its content")
	}
	// and the bundle delivers: the stack loads from where it lands, with its relative reference intact
	dir := t.TempDir()
	if err := writeFiles(dir, b.Files); err != nil {
		t.Fatalf("a bundle must be writable where it is delivered: %v", err)
	}
	var loaded []resolve.Resource
	for _, e := range b.Entries {
		rs, err := resolve.LoadFileConfined(filepath.Join(dir, e), dir)
		if err != nil {
			t.Fatalf("a bundle must load where it is delivered: %v", err)
		}
		loaded = append(loaded, rs...)
	}
	if len(loaded) != 3 {
		t.Fatalf("%d resources", len(loaded))
	}
	for _, r := range loaded {
		if r.Name == "f" && r.Content != "shared content" {
			t.Errorf("the referenced file's content must arrive: %q", r.Content)
		}
	}
}

func TestBuildBundleOfOneSimpleFile(t *testing.T) {
	root := t.TempDir()
	put(t, filepath.Join(root, "tink.yaml"), "kind: storage-volume\nname: lib\nbackup:\n  none: scratch\n")
	b, err := BuildBundle([]string{filepath.Join(root, "tink.yaml"), filepath.Join(root, "tink.yaml")}) // the same file twice
	if err != nil || !reflect.DeepEqual(b.Names(), []string{"tink.yaml"}) || !reflect.DeepEqual(b.Entries, []string{"tink.yaml"}) {
		t.Errorf("%v %v %v", err, b.Names(), b.Entries)
	}
	// a stack that does not load is not bundled
	put(t, filepath.Join(root, "bad.yaml"), "kind: storage-volume\nname: x\nnonsense: 1\n")
	if _, err := BuildBundle([]string{filepath.Join(root, "bad.yaml")}); err == nil {
		t.Error("a stack that does not load must not be bundled")
	}
	if _, err := BuildBundle([]string{filepath.Join(root, "missing.yaml")}); err == nil {
		t.Error("a missing stack file must be an error")
	}
}

func TestBuildBundleRefusesWhatCannotBeShipped(t *testing.T) {
	root := t.TempDir()
	// an image whose source is outside the stack's tree
	elsewhere := t.TempDir()
	put(t, filepath.Join(root, "tink.yaml"), "kind: image\nname: i\nalias: a\nsource: ../"+filepath.Base(elsewhere)+"/disk.qcow2\n")
	if _, err := BuildBundle([]string{filepath.Join(root, "tink.yaml")}); err == nil || !strings.Contains(err.Error(), "image source") {
		t.Errorf("an image source outside the tree: %v", err)
	}
	// an image whose source is inside the tree is fine, and is not shipped
	put(t, filepath.Join(root, "ok", "tink.yaml"), "kind: image\nname: i\nalias: a\nsource: disk.qcow2\n")
	put(t, filepath.Join(root, "ok", "disk.qcow2"), strings.Repeat("x", 1000))
	b, err := BuildBundle([]string{filepath.Join(root, "ok", "tink.yaml")})
	if err != nil || !reflect.DeepEqual(b.Names(), []string{"tink.yaml"}) {
		t.Errorf("an image source is not shipped: %v %v", err, b.Names())
	}

	// a source_path that is too large
	old := MaxBundleBytes
	MaxBundleBytes = 100
	defer func() { MaxBundleBytes = old }()
	put(t, filepath.Join(root, "big", "tink.yaml"), "kind: file\nname: f\ninstance: i\npath: /x\nsource_path: blob\n")
	put(t, filepath.Join(root, "big", "blob"), strings.Repeat("x", 500))
	if _, err := BuildBundle([]string{filepath.Join(root, "big", "tink.yaml")}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("a bundle over the limit: %v", err)
	}
}

func TestCommonDir(t *testing.T) {
	sep := string(filepath.Separator)
	for in, want := range map[string]string{
		"/a/b/x /a/b/y":   "/a/b",
		"/a/b/x /a/c/y":   "/a",
		"/a/b/c/x /a/b/y": "/a/b",
		"/a/x /b/y":       "/",
		"/a/b/x":          "/a/b",
	} {
		var files []string
		for _, f := range strings.Fields(in) {
			files = append(files, filepath.FromSlash(f))
		}
		if got := commonDir(files); got != filepath.FromSlash(want) && !(want == "/" && got == sep) {
			t.Errorf("commonDir(%v) = %q, want %q", files, got, want)
		}
	}
	if commonDir(nil) != "" {
		t.Error("no files, no directory")
	}
}
