package daemon

import (
	"bytes"
	"context"
	"errors"
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
