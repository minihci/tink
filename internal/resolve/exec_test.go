package resolve

import "testing"

func TestTriggerHash_SameInputsSameHash(t *testing.T) {
	a := triggerHash([]string{"368b", "8d81"})
	b := triggerHash([]string{"368b", "8d81"})
	if a != b {
		t.Errorf("triggerHash(%q) = %q, want same hash both times, got %q", []string{"368b", "8d81"}, a, b)
	}
}

func TestTriggerHash_DifferentInputsDifferentHash(t *testing.T) {
	a := triggerHash([]string{"368b", "8d81"})
	b := triggerHash([]string{"368b", "8d80"})
	if a == b {
		t.Errorf("triggerHash of different triggers produced the same hash %q", a)
	}
}

// The whole reason triggerHash joins with "\x00" rather than plain
// concatenation: ["ab", "c"] and ["a", "bc"] must not collide just
// because they concatenate to the same string.
func TestTriggerHash_NoBoundaryCollision(t *testing.T) {
	a := triggerHash([]string{"ab", "c"})
	b := triggerHash([]string{"a", "bc"})
	if a == b {
		t.Errorf("triggerHash(%q) and triggerHash(%q) collided: %q", []string{"ab", "c"}, []string{"a", "bc"}, a)
	}
}

func TestTriggerHashConfigKey_NamespacedByResourceName(t *testing.T) {
	got := triggerHashConfigKey("new-id")
	want := "user.tink.exec.new-id.trigger-hash"
	if got != want {
		t.Errorf("triggerHashConfigKey(%q) = %q, want %q", "new-id", got, want)
	}
}
