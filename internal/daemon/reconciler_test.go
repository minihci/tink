package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stream is a fake event stream: a subscription that stays up until the test drops it.
type stream struct {
	mu     sync.Mutex
	subs   int
	handle func(string)
	drop   chan error
}

func newStream() *stream { return &stream{drop: make(chan error, 4)} }

func (s *stream) watch(ctx context.Context, ready func(), handle func(string)) error {
	s.mu.Lock()
	s.subs++
	s.handle = handle
	s.mu.Unlock()
	ready()
	select {
	case <-ctx.Done():
		return nil
	case err := <-s.drop:
		return err
	}
}

func (s *stream) emit(action string) {
	s.mu.Lock()
	h := s.handle
	s.mu.Unlock()
	h(action)
}

func (s *stream) subscriptions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.subs
}

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *logs) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// harness runs a Reconciler against a fake stream with short timings and counts its passes.
type harness struct {
	r      *Reconciler
	s      *stream
	passes atomic.Int32
	log    *logs
	cancel context.CancelFunc
	done   chan struct{}
	result atomic.Value // []ReconcileResult the next pass returns
}

func start(t *testing.T) *harness {
	t.Helper()
	h := &harness{s: newStream(), log: &logs{}, done: make(chan struct{})}
	h.result.Store([]ReconcileResult{{What: "dns", State: "converged"}})
	h.r = &Reconciler{
		Watch: h.s.watch,
		Reconcile: func(context.Context) []ReconcileResult {
			h.passes.Add(1)
			return h.result.Load().([]ReconcileResult)
		},
		Debounce: 30 * time.Millisecond,
		MaxWait:  300 * time.Millisecond,
		Resync:   time.Hour,
		Backoff:  5 * time.Millisecond,
		Live:     &Live{},
		Logf:     h.log.logf,
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { _ = h.r.Run(ctx); close(h.done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	})
	return h
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (h *harness) settle() { time.Sleep(150 * time.Millisecond) }

func TestTheFirstPassFollowsTheFirstSubscription(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	h.settle()
	if got := h.passes.Load(); got != 1 {
		t.Errorf("passes = %d, want 1", got)
	}
	if h.s.subscriptions() != 1 {
		t.Errorf("subscriptions = %d, want 1", h.s.subscriptions())
	}
}

func TestAnEventThatCanChangeTheRecordsStartsAPass(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	h.s.emit("instance-updated")
	eventually(t, "a pass after the event", func() bool { return h.passes.Load() == 2 })
}

func TestEventsThatCannotChangeTheRecordsStartNoPass(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	for _, a := range []string{"instance-file-retrieved", "instance-file-pushed", "instance-exec", "instance-started", "storage-volume-updated"} {
		h.s.emit(a)
	}
	h.settle()
	if got := h.passes.Load(); got != 1 {
		t.Errorf("passes = %d after events that are not in the wake set, want 1", got)
	}
}

func TestABurstOfEventsIsOnePass(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	for i := 0; i < 20; i++ {
		h.s.emit("instance-updated")
		time.Sleep(2 * time.Millisecond)
	}
	h.settle()
	if got := h.passes.Load(); got != 2 {
		t.Errorf("passes = %d after a burst, want 2 (the first and one for the burst)", got)
	}
}

func TestADroppedStreamIsSubscribedAgainAndFollowedByAFullPass(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	h.s.drop <- errors.New("connection reset")
	eventually(t, "a second subscription", func() bool { return h.s.subscriptions() == 2 })
	eventually(t, "a pass after resubscribing", func() bool { return h.passes.Load() == 2 })
	if !strings.Contains(h.log.text(), "subscribing again") {
		t.Errorf("the drop was not logged: %q", h.log.text())
	}
}

func TestTheResyncIsTheBackstopWhenNoEventComes(t *testing.T) {
	h := &harness{s: newStream(), log: &logs{}, done: make(chan struct{})}
	h.result.Store([]ReconcileResult{{What: "dns", State: "converged"}})
	h.r = &Reconciler{
		Watch:     h.s.watch,
		Reconcile: func(context.Context) []ReconcileResult { h.passes.Add(1); return h.result.Load().([]ReconcileResult) },
		Debounce:  20 * time.Millisecond, Resync: 60 * time.Millisecond, Backoff: 5 * time.Millisecond,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = h.r.Run(ctx) }()
	eventually(t, "passes from the resync alone", func() bool { return h.passes.Load() >= 3 })
}

func TestWhatThePassFoundIsInTheStatusAndKeepsItsSinceUntilItChanges(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	first := h.r.Live.reconcilesSnapshot()
	if len(first) != 1 || first[0].What != "dns" || first[0].State != "converged" || first[0].Since.IsZero() {
		t.Fatalf("status = %+v", first)
	}

	time.Sleep(20 * time.Millisecond)
	h.s.emit("instance-updated")
	eventually(t, "a second pass", func() bool { return h.passes.Load() == 2 })
	if again := h.r.Live.reconcilesSnapshot(); !again[0].Since.Equal(first[0].Since) {
		t.Errorf("a pass that found the same thing moved Since: %v -> %v", first[0].Since, again[0].Since)
	}

	h.result.Store([]ReconcileResult{{What: "dns", State: "blocked", Reason: "the apply was refused", Detail: []string{"detail line"}}})
	h.s.emit("instance-updated")
	eventually(t, "a third pass", func() bool { return h.passes.Load() == 3 })
	got := h.r.Live.reconcilesSnapshot()
	if got[0].State != "blocked" || got[0].Reason != "the apply was refused" || !got[0].Since.After(first[0].Since) {
		t.Errorf("status after a change = %+v", got)
	}
	eventually(t, "the change in the log, with its detail", func() bool {
		return strings.Contains(h.log.text(), "blocked") && strings.Contains(h.log.text(), "detail line")
	})
}

func TestAPassThatFindsTheSameThingIsNotLoggedAgain(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() == 1 })
	for i := 2; i <= 4; i++ {
		h.s.emit("instance-updated")
		n := int32(i)
		eventually(t, "another pass", func() bool { return h.passes.Load() == n })
	}
	if got := strings.Count(h.log.text(), "reconcile dns"); got != 1 {
		t.Errorf("logged %d times for an unchanged result, want 1:\n%s", got, h.log.text())
	}
}

func TestWakesReconcileIsTheDerivedStateEventsOnly(t *testing.T) {
	for _, a := range []string{"instance-created", "instance-deleted", "instance-renamed", "instance-updated", "profile-updated", "network-updated"} {
		if !WakesReconcile(a) {
			t.Errorf("%s should wake a reconcile", a)
		}
	}
	for _, a := range []string{"instance-file-retrieved", "instance-file-pushed", "instance-exec", "instance-started", "instance-shutdown", "storage-volume-updated", "image-created"} {
		if WakesReconcile(a) {
			t.Errorf("%s must not wake a reconcile: the reconciler's own reads and writes emit some of them", a)
		}
	}
}

func TestEveryPassThatRewroteSomethingIsInTheLogEvenWhenTheStateIsTheSame(t *testing.T) {
	h := start(t)
	h.result.Store([]ReconcileResult{{What: "dns", State: "converged", Wrote: true, Detail: []string{"1 record added"}}})
	eventually(t, "the first pass", func() bool { return h.passes.Load() >= 1 })
	for i := 2; i <= 3; i++ {
		h.s.emit("instance-updated")
		n := int32(i)
		eventually(t, "another pass", func() bool { return h.passes.Load() >= n })
	}
	eventually(t, "a log line for each rewrite", func() bool {
		return strings.Count(h.log.text(), "rewrote it") >= 3
	})
}
