package bootstrap

import "testing"

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
