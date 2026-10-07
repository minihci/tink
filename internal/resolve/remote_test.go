package resolve

import (
	"strings"
	"testing"

	"github.com/minihci/tink/internal/incusapi"
)

// kind: incus is argv for the local `incus` CLI, so under a remote it must be blocked, with the reason, and
// without ever running the CLI (the Check here would fail loudly if it were run).
func TestKindIncusIsBlockedUnderARemote(t *testing.T) {
	t.Cleanup(func() { incusapi.UseRemote("") })
	incusapi.UseRemote("tron")
	r := Resource{Kind: KindIncus, Name: "import", Check: []string{"definitely-not-a-real-subcommand"}, Command: []string{"also-not-real"}}
	got, err := planIncus(r)
	if err != nil {
		t.Fatal(err)
	}
	if got.Action != ActionBlocked || len(got.Blocked) != 1 {
		t.Fatalf("expected a BLOCKED resource, got %+v", got)
	}
	if !strings.Contains(got.Blocked[0], `"tron"`) || !strings.Contains(got.Blocked[0], "local `incus` CLI") {
		t.Errorf("the reason must say why and name the remote: %q", got.Blocked[0])
	}
}
