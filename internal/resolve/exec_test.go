package resolve

import (
	"testing"
	"time"

	"github.com/minihci/tink/internal/incusapi"
)

func TestAgentRetry_UnsetTimeoutUsesPackageDefault(t *testing.T) {
	attempts, delay := agentRetry(Resource{})
	if attempts != incusapi.DefaultAgentRetryAttempts || delay != incusapi.DefaultAgentRetryDelay {
		t.Errorf("agentRetry(zero AgentTimeout) = (%d, %v), want (%d, %v)",
			attempts, delay, incusapi.DefaultAgentRetryAttempts, incusapi.DefaultAgentRetryDelay)
	}
}

func TestAgentRetry_ConvertsTimeoutToAttemptsAtDefaultCadence(t *testing.T) {
	attempts, delay := agentRetry(Resource{AgentTimeout: 45 * time.Second})
	if delay != incusapi.DefaultAgentRetryDelay {
		t.Errorf("agentRetry() delay = %v, want the default cadence %v unchanged", delay, incusapi.DefaultAgentRetryDelay)
	}
	if attempts != 45 {
		t.Errorf("agentRetry(45s) attempts = %d, want 45 at a 1s cadence", attempts)
	}
}

func TestAgentRetry_RoundsUpPartialAttempt(t *testing.T) {
	attempts, _ := agentRetry(Resource{AgentTimeout: 1500 * time.Millisecond})
	if attempts != 2 {
		t.Errorf("agentRetry(1.5s) attempts = %d, want 2 (rounded up)", attempts)
	}
}

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
