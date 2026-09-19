package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidate_RejectsUnknownKind(t *testing.T) {
	r := Resource{Kind: Kind("bogus"), Name: "x"}
	if err := Validate(r); err == nil {
		t.Error("expected an error for an unknown kind")
	}
}

func TestValidate_AllowsFieldsCommonToEveryKind(t *testing.T) {
	for kind := range kindPriority {
		r := Resource{
			Kind:      kind,
			Name:      "x",
			Project:   "p",
			DependsOn: []string{"y"},
		}
		switch kind {
		case KindIncus:
			r.Check = []string{"image", "list"}
			r.Command = []string{"image", "list"}
		case KindImage:
			r.Alias = "a"
			r.Source = "s"
		}
		if err := Validate(r); err != nil {
			t.Errorf("kind %q with only common fields set: Validate() error = %v", kind, err)
		}
	}
}

func TestValidate_RejectsFieldNotOwnedByKind(t *testing.T) {
	tests := []struct {
		name string
		r    Resource
	}{
		{"project with pool", Resource{Kind: KindProject, Name: "x", Pool: "default"}},
		{"profile with image", Resource{Kind: KindProfile, Name: "x", Image: "docker-oci:library/redis:7-alpine"}},
		{"storage-volume with config", Resource{Kind: KindStorageVolume, Name: "x", Config: map[string]string{"a": "b"}}},
		{"instance with check/command", Resource{Kind: KindInstance, Name: "x", Check: []string{"image", "list"}}},
		{"instance with path", Resource{Kind: KindInstance, Name: "x", Path: "/etc/foo"}},
		{"file with profiles", Resource{Kind: KindFile, Name: "x", Instance: "i", Path: "/x", Profiles: []string{"default"}}},
		{"incus with devices", Resource{Kind: KindIncus, Name: "x", Check: []string{"a"}, Command: []string{"b"}, Devices: map[string]map[string]string{"eth0": {"type": "nic"}}}},
		{"image with vm", Resource{Kind: KindImage, Name: "x", Alias: "a", Source: "s", VM: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Validate(tt.r); err == nil {
				t.Errorf("Validate(%+v) = nil, want an error", tt.r)
			}
		})
	}
}

func TestValidate_AllowsRestartOnBothFileAndInstance(t *testing.T) {
	file := Resource{Kind: KindFile, Name: "x", Instance: "i", Path: "/x", Restart: true}
	if err := Validate(file); err != nil {
		t.Errorf("file with Restart: Validate() error = %v", err)
	}
	instance := Resource{Kind: KindInstance, Name: "x", Image: "docker-oci:library/redis:7-alpine", Restart: true}
	if err := Validate(instance); err != nil {
		t.Errorf("instance with Restart: Validate() error = %v", err)
	}
}

func TestLoadFile_RejectsFieldNotOwnedByKind(t *testing.T) {
	dir := t.TempDir()
	stack := "kind: project\nname: x\npool: default\n"
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(filepath.Join(dir, "stack.yaml")); err == nil {
		t.Error("expected an error when a project resource sets pool")
	}
}
