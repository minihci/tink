package bootstrap

import (
	"path/filepath"
	"strings"
	"testing"
)

// Regression test for a real bug caught testing against a live host:
// GetStoragePoolVolumeNames returns type-prefixed names ("custom/foo",
// "container/bar"), and comparing bare names against them always missed,
// so deploy kept trying (and failing) to recreate volumes that already
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

// deploy's --socket is empty by default, and an empty Options.Socket must reach Incus as empty so that $INCUS_SOCKET is followed. It was
// once turned into a hard-coded path here, so a daemon whose package keeps its socket under /run could not be found without --socket.
func TestAnEmptySocketIsLeftToIncusToResolve(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "elsewhere.sock")
	t.Setenv("INCUS_SOCKET", sock)
	err := applyStorageVolumes(&runner{}, Options{})
	if err == nil || !strings.Contains(err.Error(), sock) {
		t.Fatalf("with no Options.Socket the connection must go to $INCUS_SOCKET (%s): %v", sock, err)
	}
}
