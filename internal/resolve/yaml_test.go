package resolve

import (
	"os"
	"path/filepath"
	"reflect"
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

func TestLoadFiles_DefaultsToTinkYAMLWhenNoPathsGiven(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultFile), []byte("kind: project\nname: p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	resources, err := LoadFiles(nil)
	if err != nil {
		t.Fatalf("LoadFiles(nil) error = %v", err)
	}
	if len(resources) != 1 || resources[0].Name != "p" {
		t.Errorf("LoadFiles(nil) = %+v, want the project resource from %s", resources, DefaultFile)
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

func TestLoadFile_ParsesVM(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: instance\nname: i\nimage: images:homeassistant/haos/generic-x86-64\nvm: true\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 || !resources[0].VM {
		t.Errorf("LoadFile() = %+v, want one resource with VM = true", resources)
	}
}

func TestLoadFile_VMDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: instance\nname: i\nimage: docker-oci:library/redis:7-alpine\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 || resources[0].VM {
		t.Errorf("LoadFile() = %+v, want one resource with VM = false", resources)
	}
}

func TestLoadFile_ParsesInstanceRestart(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: instance\nname: i\nimage: docker-oci:library/redis:7-alpine\nrestart: true\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 || !resources[0].Restart {
		t.Errorf("LoadFile() = %+v, want one instance resource with Restart = true", resources)
	}
}

func TestLoadFile_InstanceRestartDefaultsFalse(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: instance\nname: i\nimage: docker-oci:library/redis:7-alpine\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 || resources[0].Restart {
		t.Errorf("LoadFile() = %+v, want one instance resource with Restart = false", resources)
	}
}

func TestLoadFile_ParsesImageResolvesSourceRelativeToYAMLDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "haos.qcow2"), []byte("fake qcow2 content"), 0o644); err != nil {
		t.Fatal(err)
	}
	stack := "kind: image\nname: haos-image\nalias: haos-x86-64-18.3\nsource: haos.qcow2\nproperties:\n  os: HAOS\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("LoadFile() = %+v, want 1 resource", resources)
	}
	r := resources[0]
	wantSource := filepath.Join(dir, "haos.qcow2")
	if r.Alias != "haos-x86-64-18.3" || r.Source != wantSource {
		t.Errorf("LoadFile() = %+v, want Alias haos-x86-64-18.3 and Source %q", r, wantSource)
	}
	if r.Architecture != "x86_64" {
		t.Errorf("Architecture = %q, want default x86_64", r.Architecture)
	}
	if r.Properties["os"] != "HAOS" {
		t.Errorf("Properties[os] = %q, want HAOS", r.Properties["os"])
	}
}

func TestLoadFile_ImageArchitectureOverridable(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: image\nname: i\nalias: a\nsource: x.qcow2\narchitecture: aarch64\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(resources) != 1 || resources[0].Architecture != "aarch64" {
		t.Errorf("LoadFile() = %+v, want Architecture aarch64", resources)
	}
}

func TestLoadFile_ImageRequiresAlias(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: image\nname: i\nsource: x.qcow2\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when an image resource has no alias")
	}
}

func TestLoadFile_ImageRequiresSource(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: image\nname: i\nalias: a\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when an image resource has no source")
	}
}

func TestLoadFile_ParsesIncusCheckAndCommand(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: incus\nname: haos-image\ncheck: [image, info, haos-x86-64]\ncommand: [image, import, /var/lib/tink/haos.qcow2, --alias, haos-x86-64]\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}

	resources, err := LoadFile(filepath.Join(dir, "stack.yaml"))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	wantCheck := []string{"image", "info", "haos-x86-64"}
	wantCommand := []string{"image", "import", "/var/lib/tink/haos.qcow2", "--alias", "haos-x86-64"}
	if len(resources) != 1 ||
		!reflect.DeepEqual(resources[0].Check, wantCheck) ||
		!reflect.DeepEqual(resources[0].Command, wantCommand) {
		t.Errorf("LoadFile() = %+v, want Check %v and Command %v", resources, wantCheck, wantCommand)
	}
}

func TestLoadFile_IncusRequiresCheck(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: incus\nname: x\ncommand: [image, list]\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when an incus resource has no check")
	}
}

func TestLoadFile_IncusRequiresCommand(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: incus\nname: x\ncheck: [image, list]\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when an incus resource has no command")
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
