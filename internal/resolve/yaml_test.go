package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFile_ParsesEveryKindAndField(t *testing.T) {
	resources, err := LoadFile("testdata/spike.yaml")
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 6 {
		t.Fatalf("LoadFile() returned %d resources, want 6", len(resources))
	}

	byName := make(map[string]Resource, len(resources))
	for _, r := range resources {
		byName[r.Name] = r
	}

	db, ok := byName["spike-db"]
	if !ok {
		t.Fatal("spike-db not found")
	}
	if db.Kind != KindInstance || db.Project != "resolve-spike-test" {
		t.Errorf("spike-db = %+v, want kind instance in project resolve-spike-test", db)
	}
	if got := db.Devices["data"]["source"]; got != "spike-data" {
		t.Errorf("spike-db data device source = %q, want spike-data", got)
	}

	app, ok := byName["spike-app"]
	if !ok {
		t.Fatal("spike-app not found")
	}
	if len(app.DependsOn) != 2 {
		t.Errorf("spike-app DependsOn = %v, want 2 entries", app.DependsOn)
	}
}

func TestLoadFile_SourcePathResolvedRelativeToYAMLDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Caddyfile"), []byte("example content"), 0o644); err != nil {
		t.Fatal(err)
	}
	stack := "kind: file\nname: f\ninstance: caddy\npath: /etc/caddy/Caddyfile\nsource_path: Caddyfile\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 || resources[0].Content != "example content" {
		t.Errorf("LoadFile() = %+v, want one resource with Content %q", resources, "example content")
	}
}

func TestLoadFile_ContentAndSourcePathMutuallyExclusive(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: file\nname: f\ninstance: caddy\npath: /x\nsource_path: whatever\ncontent: inline\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when both content and source_path are set")
	}
}

func TestLoadFile_RejectsUnknownKind(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: bogus\nname: x\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error for an unknown kind")
	}
}

func TestLoadFile_RequiresName(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: project\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when name is missing")
	}
}
