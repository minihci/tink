package bootstrap

import "testing"

// Regression test for a real bug caught testing against a live host:
// GetStoragePoolVolumeNames returns type-prefixed names ("custom/foo",
// "container/bar"), and comparing bare names against them always missed,
// so apply kept trying (and failing) to recreate volumes that already
// existed.
func TestExistingCustomVolumeNames(t *testing.T) {
	got := existingCustomVolumeNames([]string{
		"container/authelia",
		"container/incus-ui",
		"custom/authelia-config",
		"custom/incus-ui-caddy-data",
	})

	if !got["authelia-config"] || !got["incus-ui-caddy-data"] {
		t.Fatalf("expected both custom volumes to be recognized, got %+v", got)
	}
	if got["authelia"] || got["incus-ui"] {
		t.Fatalf("container volumes must not be matched against custom volume names, got %+v", got)
	}
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 recognized custom volumes, got %d: %+v", len(got), got)
	}
}
