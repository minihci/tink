package resolve

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A plan is pure reads, so it can stop at once: with its context already ended it asks the server nothing.
func TestPlanWithEndedContextReadsNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A nil server would panic on the first read.
	_, err := PlanWithOptions(nil, []Resource{
		{Kind: KindInstance, Name: "a", Image: "img"},
		{Kind: KindProject, Name: "p"},
	}, PlanOptions{Offline: true}.WithContext(ctx))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
}

// Rebuilds run one at a time; one that is waiting its turn gives up when the context ends, having changed nothing, and does not hold the
// apply for as long as the rebuild ahead of it takes.
func TestRebuildQueueIsLeftWhenTheContextEnds(t *testing.T) {
	if err := lockRebuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer unlockRebuild()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- lockRebuild(ctx) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("still waiting for its turn 5s after the context ended")
	}
}
