package resolve

import (
	"strings"
	"testing"
)

func TestLevels_DependsOnMissingTargetIsAnError(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "app", DependsOn: []string{"database"}},
		{Kind: KindInstance, Name: "db"},
	}
	_, err := Levels(resources)
	if err == nil {
		t.Fatal("expected an error: depends_on names a resource that is not in the stack")
	}
	for _, want := range []string{`"app"`, `"database"`, "not a resource in this stack"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestLevels_DependsOnMissingTargetSuggestsTheNearName(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "app", DependsOn: []string{"immich-dp"}},
		{Kind: KindInstance, Name: "immich-db"},
		{Kind: KindInstance, Name: "immich-redis"},
	}
	_, err := Levels(resources)
	if err == nil || !strings.Contains(err.Error(), `did you mean "immich-db"?`) {
		t.Fatalf("expected a did-you-mean for immich-db, got %v", err)
	}
}

func TestLevels_DependsOnMissingTargetFarFromEverythingSuggestsNothing(t *testing.T) {
	resources := []Resource{
		{Kind: KindInstance, Name: "app", DependsOn: []string{"something-unrelated"}},
		{Kind: KindInstance, Name: "db"},
	}
	_, err := Levels(resources)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("a name that is nowhere near any resource should not get a guess: %v", err)
	}
}

func TestLevels_DependsOnItselfIsAnError(t *testing.T) {
	resources := []Resource{{Kind: KindInstance, Name: "app", DependsOn: []string{"app"}}}
	_, err := Levels(resources)
	if err == nil || !strings.Contains(err.Error(), "names itself") {
		t.Fatalf("expected a depends_on-itself error, got %v", err)
	}
}

func TestLevels_DependsOnTheStackNameIsAnError(t *testing.T) {
	// the stack's name is metadata, not a resource, so it cannot be waited for
	resources := []Resource{
		{Kind: KindStack, Name: "mystack"},
		{Kind: KindInstance, Name: "app", DependsOn: []string{"mystack"}},
	}
	if _, err := Levels(resources); err == nil {
		t.Fatal("expected an error: depends_on cannot name the stack")
	}
}

func TestLevels_DependsOnIsReportedDeterministically(t *testing.T) {
	// two bad references: the one in the earlier resource is the one reported, whatever Go does with map order
	resources := []Resource{
		{Kind: KindInstance, Name: "a", DependsOn: []string{"nope-one"}},
		{Kind: KindInstance, Name: "b", DependsOn: []string{"nope-two"}},
	}
	for i := 0; i < 20; i++ {
		_, err := Levels(resources)
		if err == nil || !strings.Contains(err.Error(), `"a"`) {
			t.Fatalf("run %d: expected the error about resource a, got %v", i, err)
		}
	}
}

func TestLevels_StructuralReferencesToUndeclaredObjectsStillPass(t *testing.T) {
	// Profiles, volumes and projects may already exist on the server without being declared here: that is not an error.
	// Only depends_on, which exists purely to order this stack's own resources, is checked.
	resources := []Resource{{
		Kind: KindInstance, Name: "app", Project: "elsewhere", Profiles: []string{"already-on-server"},
		Devices: map[string]map[string]string{"data": {"type": "disk", "source": "existing-volume"}},
	}}
	if _, err := Levels(resources); err != nil {
		t.Fatalf("a structural reference to an undeclared object must stay lenient, got %v", err)
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0}, {"abc", "abc", 0}, {"abc", "abd", 1}, {"abc", "ab", 1}, {"", "abc", 3}, {"immich-dp", "immich-db", 1}, {"kitten", "sitting", 3},
	}
	for _, c := range cases {
		if got := editDistance(c.a, c.b); got != c.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
