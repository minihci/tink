package instance

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// hangFake is a server whose changes are accepted and never finish: each operation's Wait blocks until the test is over, as for a daemon that has
// stopped answering. It records the calls it was asked to make.
type hangFake struct {
	readFake
	release chan struct{}
	mu      sync.Mutex // the calls are made from the goroutines a deadline abandons
	calls   []string
}

func (f *hangFake) record(c string) { f.mu.Lock(); f.calls = append(f.calls, c); f.mu.Unlock() }

func (f *hangFake) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, " ")
}

func newHangFake() *hangFake {
	return &hangFake{readFake: readFake{aliasErr: errNotFound}, release: make(chan struct{})}
}

func (f *hangFake) UseProject(string) incus.InstanceServer { return f }

func (f *hangFake) CreateInstanceFromImage(incus.ImageServer, api.Image, api.InstancesPost) (incus.RemoteOperation, error) {
	f.record("create")
	return hangRemoteOp{release: f.release}, nil
}

func (f *hangFake) GetInstance(string) (*api.Instance, string, error) {
	f.record("get")
	return &api.Instance{Status: "Stopped"}, "", nil
}

func (f *hangFake) UpdateInstance(string, api.InstancePut, string) (incus.Operation, error) {
	f.record("update")
	return hangOp{release: f.release}, nil
}

func (f *hangFake) UpdateInstanceState(string, api.InstanceStatePut, string) (incus.Operation, error) {
	f.record("state")
	return hangOp{release: f.release}, nil
}

type hangRemoteOp struct {
	incus.RemoteOperation
	release chan struct{}
}

func (o hangRemoteOp) Wait() error                                              { <-o.release; return nil }
func (hangRemoteOp) AddHandler(func(api.Operation)) (*incus.EventTarget, error) { return nil, nil }

type hangOp struct {
	incus.Operation
	release chan struct{}
}

func (o hangOp) Wait() error { <-o.release; return nil }

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

func wantUnknown(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, incusapi.ErrTimeout) {
		t.Fatalf("err = %v, want it to wrap incusapi.ErrTimeout", err)
	}
	if !strings.Contains(err.Error(), "outcome is unknown") {
		t.Errorf("err = %q: it must say the outcome is unknown", err)
	}
}

func TestCreateReturnsAtTheDeadlineWhenTheServerNeverFinishes(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	f := newHangFake()
	defer close(f.release)
	var err error
	within(t, 5*time.Second, func() {
		err = Create(f, &Spec{Name: "web", Image: "local-image", OpTimeout: 50 * time.Millisecond}, "")
	})
	wantUnknown(t, err)
	if !strings.Contains(err.Error(), "creating web") {
		t.Errorf("err = %q: it must name what was being done", err)
	}
}

func TestApplyConfigReturnsAtTheDeadlineWhenTheUpdateNeverFinishes(t *testing.T) {
	f := newHangFake()
	defer close(f.release)
	var err error
	within(t, 5*time.Second, func() {
		err = ApplyConfig(f, &Spec{Name: "web", Config: map[string]string{"user.a": "b"}, OpTimeout: 50 * time.Millisecond})
	})
	wantUnknown(t, err)
}

// A start that timed out is not tried again: the first may still complete, and a second state change on top of it is a race.
func TestEnsureRunningDoesNotRetryAStartThatTimedOut(t *testing.T) {
	f := newHangFake()
	defer close(f.release)
	var err error
	within(t, 5*time.Second, func() { err = EnsureRunningWithin(f, "web", 50*time.Millisecond) })
	wantUnknown(t, err)
	if n := strings.Count(f.called(), "state"); n != 1 {
		t.Errorf("%d state changes were asked for (%v), want exactly 1", n, f.called())
	}
}

// Without a deadline (tink run, the helper) nothing is bounded and nothing is abandoned: the wait is the caller's.
func TestNoDeadlineWaitsForTheServer(t *testing.T) {
	f := newHangFake()
	done := make(chan error, 1)
	go func() { done <- EnsureRunning(f, "web") }()
	select {
	case err := <-done:
		t.Fatalf("returned %v while the server had not finished", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(f.release)
	if err := <-done; err != nil {
		t.Errorf("err = %v", err)
	}
}
