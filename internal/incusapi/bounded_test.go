package incusapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// within fails the test, instead of hanging it, when f has not returned by limit.
func within(t *testing.T, limit time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("still running after %s: the deadline did not end the wait", limit)
	}
}

// A call the server never answers returns at the deadline, says its outcome is unknown, and is not cancelled: the work may still be running.
func TestBoundedReturnsAtTheDeadlineAndDoesNotCancel(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	var err error
	start := time.Now()
	within(t, 5*time.Second, func() {
		err = BoundedDo(50*time.Millisecond, "creating instance/web", func() error { <-block; return nil })
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want it to wrap ErrTimeout", err)
	}
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Errorf("returned after %s, before the deadline", d)
	}
	for _, want := range []string{"creating instance/web", "outcome is unknown", "tink plan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	var te *TimeoutError
	if !errors.As(err, &te) || te.After != 50*time.Millisecond {
		t.Errorf("errors.As(*TimeoutError) = %v, %v", te, errors.As(err, &te))
	}
}

// A call that answers in time returns its own result and error untouched, and a zero timeout is no bound at all.
func TestBoundedPassesResultsThrough(t *testing.T) {
	boom := errors.New("boom")
	got, err := Bounded(time.Minute, "x", func() (int, error) { return 7, boom })
	if got != 7 || err != boom {
		t.Errorf("got %d, %v; want 7, boom unwrapped", got, err)
	}
	got, err = Bounded(0, "x", func() (int, error) { time.Sleep(20 * time.Millisecond); return 3, nil })
	if got != 3 || err != nil {
		t.Errorf("unbounded: got %d, %v", got, err)
	}
}
