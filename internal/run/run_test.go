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
	for _, want := range []string{"would create", "would layer profile nextcloud-app", "would add device", "would set config", "would start"} {
		if !strings.Contains(joined, want) {
			t.Errorf("Actions = %q, want a line containing %q", joined, want)
		}
	}
}

// Regression test for a real bug caught testing against a real,
// disposable test host: Postgres's and Nextcloud's own images run a
// one-shot, config-gated action on first boot (Postgres's init scripts
// need POSTGRES_PASSWORD already present; Nextcloud's installer needs
// its DB credentials already present) -- launching bare and configuring
// afterward either misses that moment entirely or crashes the instance
// before config can be applied at all. create()+applyConfig()+
// ensureRunning() always creates without starting first, so the
// instance is always started for the very first time only after every
// flag has already been translated into its config -- true regardless
// of whether any Config was actually set, unlike the disproven
// config-gated-restart design this replaced.
func TestRun_DryRunAlwaysNotesAStartEvenWithNoConfig(t *testing.T) {
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
	if !strings.Contains(joined, "would start n") {
		t.Errorf("Actions = %q, want a line noting the initial start even with no Config set", joined)
	}
	if strings.Contains(joined, "restart") {
		t.Errorf("Actions = %q, should never mention a restart -- create() never starts the instance itself", joined)
	}
}

func TestRun_DryRunSurfacesBuildErrors(t *testing.T) {
	if _, err := Run(Options{DryRun: true, Image: "i"}); err == nil {
		t.Error("expected an error when --name is missing, even in dry-run")
	}
}

func TestRun_DryRunNotesProject(t *testing.T) {
	result, err := Run(Options{DryRun: true, Name: "n", Image: "i", Project: "nextcloud-tink-test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Actions, "\n")
	if !strings.Contains(joined, "in project nextcloud-tink-test") {
		t.Errorf("Actions = %q, want a line naming the target project", joined)
	}
}
