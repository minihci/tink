package run

import (
	"strings"
	"testing"
)

// Run's dry-run path never calls incusapi.Connect or shells out to incus
// (see run.go), so it's exercised directly here without a live daemon --
// matching this repo's existing convention of unit-testing pure logic
// only and leaving daemon interaction to live verification (see
// DESIGN.md's testing plan and internal/bootstrap's own tests).
func TestRun_DryRunDoesNotTouchIncus(t *testing.T) {
	result, err := Run(Options{
		DryRun:   true,
		Name:     "nextcloud-app",
		Image:    "docker-oci:nextcloud:apache",
		Env:      []string{"FOO=bar"},
		Publish:  []string{"8080:80"},
		Network:  "incusbr0",
		Profiles: []string{"nextcloud-app"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Spec.Name != "nextcloud-app" {
		t.Errorf("Spec.Name = %q, want nextcloud-app", result.Spec.Name)
	}

	joined := strings.Join(result.Actions, "\n")
	for _, want := range []string{"would launch", "would layer profile nextcloud-app", "would add device", "would set config"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Actions = %q, want a line containing %q", joined, want)
		}
	}
}

func TestRun_DryRunSurfacesBuildErrors(t *testing.T) {
	if _, err := Run(Options{DryRun: true, Image: "i"}); err == nil {
		t.Error("expected an error when --name is missing, even in dry-run")
	}
}
