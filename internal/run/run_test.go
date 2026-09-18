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
	for _, want := range []string{"would launch", "would layer profile nextcloud-app", "would add device", "would set config", "would restart"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Actions = %q, want a line containing %q", joined, want)
		}
	}
}

// Regression test for a real bug caught testing against a real,
// disposable test host: environment.* and oci.entrypoint are
// process-launch parameters, so setting them via UpdateInstance alone
// leaves an already-running instance's original entrypoint process
// running untouched -- a restart is required to actually apply them. A
// managed-volume-only run has nothing that needs a restart to take
// effect, so it shouldn't get one.
func TestRun_DryRunSkipsRestartNoteWhenNoConfigIsSet(t *testing.T) {
	result, err := Run(Options{
		DryRun: true,
		Name:   "n",
		Image:  "i",
		Volume: []string{"/host:/container"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Actions, "\n")
	if strings.Contains(joined, "would restart") {
		t.Errorf("Actions = %q, should not mention a restart when Config is empty", joined)
	}
}

func TestRun_DryRunSurfacesBuildErrors(t *testing.T) {
	if _, err := Run(Options{DryRun: true, Image: "i"}); err == nil {
		t.Error("expected an error when --name is missing, even in dry-run")
	}
}
