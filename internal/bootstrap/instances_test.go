package bootstrap

import (
	"errors"
	"strings"
	"testing"
)

// Regression test for a real bug caught testing against a live host:
// deploy.sh's blind `incus restart` fails outright ("the instance is
// already stopped") when an app container crashes to Stopped before
// config is pushed into it -- true on every first boot. restart only
// ever works on an already-Running instance.
func TestRestartAction(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{"Running", "restart"},
		{"Stopped", "start"},
		{"Error", "start"},
		{"", "start"},
	}
	for _, c := range cases {
		if got := restartAction(c.status); got != c.want {
			t.Errorf("restartAction(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestPollForDirStopsAtTheFirstSuccess(t *testing.T) {
	r := &runner{}
	calls := 0
	err := pollForDir(r, "web", "/config", 5, 0, func() error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("err = %v after %d probes, want nil after 3", err, calls)
	}
	if len(r.actions) != 1 || r.actions[0] != "/config appeared in web" {
		t.Errorf("actions = %v", r.actions)
	}
}

func TestPollForDirGivesUpAfterTheAttemptsAndSaysWhyTheLastOneFailed(t *testing.T) {
	r := &runner{}
	calls := 0
	err := pollForDir(r, "web", "/config", 4, 0, func() error { calls++; return errors.New("test -d /config exited 1") })
	if calls != 4 {
		t.Errorf("%d probes, want 4", calls)
	}
	if err == nil || !strings.Contains(err.Error(), "/config never appeared in web") || !strings.Contains(err.Error(), "exited 1") {
		t.Errorf("err = %v", err)
	}
}

func TestWaitForDirDryRunProbesNothing(t *testing.T) {
	r := &runner{dryRun: true}
	if err := waitForDir(r, nil, "web", "/config"); err != nil { // a nil server would panic if it were used
		t.Fatal(err)
	}
	if len(r.actions) != 1 || !strings.HasPrefix(r.actions[0], "would wait for /config") {
		t.Errorf("actions = %v", r.actions)
	}
}
