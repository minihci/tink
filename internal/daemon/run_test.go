package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minihci/tink/internal/ingress"
)

func TestRun_ReconcilesImmediatelyOnStart(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(20 * time.Millisecond) // shorter than the interval below
		cancel()
	}()

	err := run(ctx, &bytes.Buffer{}, time.Hour, func() (*ingress.Result, error) {
		atomic.AddInt32(&calls, 1)
		return &ingress.Result{}, nil
	})
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 immediate reconcile call before cancellation, got %d", got)
	}
}

func TestRun_SurvivesAFailedPass(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	out := &bytes.Buffer{}

	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	err := run(ctx, out, time.Hour, func() (*ingress.Result, error) {
		atomic.AddInt32(&calls, 1)
		return nil, errors.New("simulated Incus connection failure")
	})
	if err != nil {
		t.Fatalf("run returned error: %v (a failed pass should not stop the loop)", err)
	}
	if !bytes.Contains(out.Bytes(), []byte("reconcile error")) {
		t.Errorf("expected the failure to be logged, got:\n%s", out.String())
	}
}

func TestRun_TicksAtTheGivenInterval(t *testing.T) {
	var calls int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		_ = run(ctx, &bytes.Buffer{}, 10*time.Millisecond, func() (*ingress.Result, error) {
			n := atomic.AddInt32(&calls, 1)
			if n >= 3 { // 1 immediate + at least 2 ticks
				cancel()
			}
			return &ingress.Result{}, nil
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not stop within 2s of hitting the call threshold")
	}

	if got := atomic.LoadInt32(&calls); got < 3 {
		t.Fatalf("expected at least 3 reconcile calls (1 immediate + ticks), got %d", got)
	}
}

func TestRun_RetriesAFailedPassSoonAndKeepsTheIntervalAfterASuccess(t *testing.T) {
	old := ingressRetryAfter
	ingressRetryAfter = 5 * time.Millisecond
	t.Cleanup(func() { ingressRetryAfter = old })

	var mu sync.Mutex
	passes := 0
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = run(ctx, &bytes.Buffer{}, time.Hour, func() (*ingress.Result, error) {
			mu.Lock()
			defer mu.Unlock()
			passes++
			if passes <= 2 { // the proxy to the host's API is not up yet
				return nil, errors.New("connection refused")
			}
			return &ingress.Result{}, nil
		})
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := passes
		mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(60 * time.Millisecond) // many retry periods: a pass that worked waits the whole (hour-long) interval
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if passes != 3 {
		t.Errorf("passes = %d: the two failures were retried within milliseconds, and the success was not followed by another until the interval", passes)
	}
}

func TestRun_SaysOnceWhichInstancesStillUseTheOldIngressKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	out := &safeBuffer{}
	var calls int32
	go func() {
		for atomic.LoadInt32(&calls) < 4 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	err := run(ctx, out, 2*time.Millisecond, func() (*ingress.Result, error) {
		atomic.AddInt32(&calls, 1)
		return &ingress.Result{Legacy: []string{"ns-caddy"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "register with the old user.ingress.* keys"); got != 1 {
		t.Errorf("said %d times over four passes, want once:\n%s", got, out.String())
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *safeBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }
