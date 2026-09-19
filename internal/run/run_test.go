package run

import (
	"strings"
	"testing"
)

// Run's dry-run path never calls incusapi.Connect or shells out to incus,
// so it's exercised directly here without a live daemon.
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

// A devices-only run (no Config at all) still needs its initial start:
// Create() never starts the instance itself, regardless of whether any
// Config was set, so a run with only a Volume/Publish/Network device
// still needs one.
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
		t.Errorf("Actions = %q, should never mention a restart -- Create() never starts the instance itself", joined)
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

func TestRun_DryRunNotesEphemeral(t *testing.T) {
	result, err := Run(Options{DryRun: true, Name: "n", Image: "i", Rm: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Actions, "\n")
	if !strings.Contains(joined, "ephemeral") {
		t.Errorf("Actions = %q, want a line noting the instance is ephemeral", joined)
	}
}

func TestRun_DryRunOmitsEphemeralNoteByDefault(t *testing.T) {
	result, err := Run(Options{DryRun: true, Name: "n", Image: "i"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Actions, "\n")
	if strings.Contains(joined, "ephemeral") {
		t.Errorf("Actions = %q, should not mention ephemeral when --rm was not given", joined)
	}
}

func TestRun_DryRunNotesVirtualMachine(t *testing.T) {
	result, err := Run(Options{DryRun: true, Name: "n", Image: "i", VM: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Actions, "\n")
	if !strings.Contains(joined, "virtual machine") {
		t.Errorf("Actions = %q, want a line noting this is a virtual machine", joined)
	}
}

func TestRun_DryRunNotesContainerByDefault(t *testing.T) {
	result, err := Run(Options{DryRun: true, Name: "n", Image: "i"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(result.Actions, "\n")
	if !strings.Contains(joined, "container") {
		t.Errorf("Actions = %q, want a line noting this is a container by default", joined)
	}
}
