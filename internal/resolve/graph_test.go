package resolve

import "testing"

func names(rs []Resource) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func inLevel(levels [][]Resource, level int, name string) bool {
	if level >= len(levels) {
		return false
	}
	for _, r := range levels[level] {
		if r.Name == name {
			return true
		}
	}
	return false
}

func TestLevels_StructuralDependencyFromProjectAndProfiles(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "app", Project: "myproj", Profiles: []string{"web"}},
		{Kind: KindProfile, Name: "web", Project: "myproj"},
		{Kind: KindProject, Name: "myproj"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if !inLevel(levels, 0, "myproj") {
		t.Errorf("expected myproj in level 0, got %v", levels)
	}
	if !inLevel(levels, 1, "web") {
		t.Errorf("expected web in level 1, got %v", levels)
	}
	if !inLevel(levels, 2, "app") {
		t.Errorf("expected app in level 2, got %v", levels)
	}
}

func TestLevels_StructuralDependencyFromDeviceSource(t *testing.T) {
	resources := []Resource{
		{
			Kind: KindInstance, Name: "db",
			Devices: map[string]map[string]string{
				"data": {"type": "disk", "source": "db-data"},
			},
		},
		{Kind: KindStorageVolume, Name: "db-data"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if !inLevel(levels, 0, "db-data") {
		t.Errorf("expected db-data in level 0, got %v", levels)
	}
	if !inLevel(levels, 1, "db") {
		t.Errorf("expected db in level 1, got %v", levels)
	}
}

func TestLevels_StructuralDependencyFromFileInstance(t *testing.T) {
	resources := []Resource{
		{Kind: KindFile, Name: "caddyfile", Instance: "caddy", Path: "/etc/caddy/Caddyfile"},
		{Kind: KindInstance, Name: "caddy"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if !inLevel(levels, 0, "caddy") {
		t.Errorf("expected caddy in level 0, got %v", levels)
	}
	if !inLevel(levels, 1, "caddyfile") {
		t.Errorf("expected caddyfile in level 1, got %v", levels)
	}
}

// Regression test for a real bug caught live: a file resource with no
// project of its own silently targeted the daemon's default project
// instead of its target instance's real one -- the exact same class of
// mistake as the incus-apply#68 bug this whole exploration started
// from, just made by hand in a YAML file instead of by that tool's own
// code. Fixed by inheriting the project from the referenced instance
// rather than trusting every file resource to redeclare it.
func TestLevels_FileInheritsProjectFromInstance(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "caddy", Project: "myproj"},
		{Kind: KindFile, Name: "caddyfile", Instance: "caddy", Path: "/etc/caddy/Caddyfile"},
	}
	if _, err := Levels(resources); err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if resources[1].Project != "myproj" {
		t.Errorf("file resource's Project = %q, want inherited %q", resources[1].Project, "myproj")
	}
}

func TestLevels_FileProjectNotOverriddenWhenExplicitlySet(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "caddy", Project: "myproj"},
		{Kind: KindFile, Name: "caddyfile", Instance: "caddy", Path: "/etc/caddy/Caddyfile", Project: "other-proj"},
	}
	if _, err := Levels(resources); err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if resources[1].Project != "other-proj" {
		t.Errorf("file resource's Project = %q, want explicit %q preserved", resources[1].Project, "other-proj")
	}
}

func TestLevels_ExplicitDependsOn(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "app", DependsOn: []string{"db", "redis"}},
		{Kind: KindInstance, Name: "db"},
		{Kind: KindInstance, Name: "redis"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if len(levels) != 2 {
		t.Fatalf("expected 2 levels, got %d: %v", len(levels), levels)
	}
	if !inLevel(levels, 0, "db") || !inLevel(levels, 0, "redis") {
		t.Errorf("expected db and redis both in level 0 (parallel, independent), got %v", levels[0])
	}
	if !inLevel(levels, 1, "app") {
		t.Errorf("expected app in level 1, got %v", levels)
	}
}

func TestLevels_IndependentResourcesShareALevel(t *testing.T) {
	// The real proof this matters: unrelated resources should be able to
	// run in parallel, not be serialized just because they appeared in
	// the same file. This is what we actually watched Terraform do
	// against the real nextcloud-tink-test stack.
	resources := []Resource{
		{Kind: KindInstance, Name: "redis"},
		{Kind: KindInstance, Name: "db"},
		{Kind: KindInstance, Name: "caddy"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if len(levels) != 1 {
		t.Fatalf("expected all 3 independent instances in a single level, got %d levels: %v", len(levels), levels)
	}
}

func TestLevels_DetectsCycle(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "a", DependsOn: []string{"b"}},
		{Kind: KindInstance, Name: "b", DependsOn: []string{"a"}},
	}
	if _, err := Levels(resources); err == nil {
		t.Error("expected an error for a cyclic dependency")
	}
}

func TestLevels_RejectsDuplicateNames(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "dup"},
		{Kind: KindProfile, Name: "dup"},
	}
	if _, err := Levels(resources); err == nil {
		t.Error("expected an error for duplicate resource names")
	}
}

func TestLevels_KindPriorityBreaksTiesWithinALevel(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "app", Project: "myproj"},
		{Kind: KindProject, Name: "myproj"},
	}
	levels, err := Levels(resources)
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if got := names(levels[0]); len(got) != 1 || got[0] != "myproj" {
		t.Errorf("level 0 = %v, want [myproj]", got)
	}
}
