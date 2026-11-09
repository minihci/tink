package incusapi

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// Incus writes a command's stdout and stderr to the same buffer from two goroutines. The writes
// below, including empty ones (an idle stderr stream), must neither be lost nor race; run with -race.
func TestSyncBufferKeepsEverythingFromConcurrentWriters(t *testing.T) {
	for round := 0; round < 200; round++ {
		var b syncBuffer
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { // stdout: real data
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b.Write([]byte("out\n"))
			}
		}()
		go func() { // stderr: mostly nothing, as for a command that succeeds
			defer wg.Done()
			for i := 0; i < 50; i++ {
				b.Write(nil)
				b.Write([]byte{})
			}
		}()
		wg.Wait()
		if got := strings.Count(b.String(), "out\n"); got != 50 {
			t.Fatalf("round %d: lost output: %d of 50 writes survived", round, got)
		}
	}
}

// execServer is an Incus server that runs "commands": it records what it was asked, reads standard input to the end, and answers with
// output and an exit status the test chose.
type execServer struct {
	incus.InstanceServer
	post  api.InstanceExecPost
	stdin string
	out   string
	code  float64
	err   error
}

type execOp struct {
	incus.Operation
	code float64
}

func (o execOp) Wait() error { return nil }
func (o execOp) Get() api.Operation {
	return api.Operation{Metadata: map[string]any{"return": o.code}}
}

func (s *execServer) ExecInstance(_ string, post api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.post = post
	b, _ := io.ReadAll(args.Stdin)
	s.stdin = string(b)
	args.Stdout.Write([]byte(s.out))
	close(args.DataDone)
	return execOp{code: s.code}, nil
}

func TestExecWithStdinDeliversStandardInputAndEnvironmentAndReturnsTheStatusAndOutput(t *testing.T) {
	s := &execServer{out: "added remote host\n", code: 0}
	code, out, err := ExecWithStdin(s, "helper", []string{"tink", "remote", "add", "host", "--token-file", "-"}, strings.NewReader("a-secret-trust-token"), map[string]string{"HOME": "/root"})
	if err != nil || code != 0 || out != "added remote host\n" {
		t.Fatalf("%d %q %v", code, out, err)
	}
	if s.stdin != "a-secret-trust-token" {
		t.Errorf("the token must arrive on standard input, whole: %q", s.stdin)
	}
	if strings.Contains(strings.Join(s.post.Command, " "), "a-secret-trust-token") {
		t.Error("and never on the command line")
	}
	if s.post.Environment["HOME"] != "/root" || !s.post.WaitForWS {
		t.Errorf("%+v", s.post)
	}
}

func TestExecWithStdinReturnsANonZeroStatusWithItsOutputAndNoError(t *testing.T) {
	s := &execServer{out: "error: the token was already used\n", code: 1}
	code, out, err := ExecWithStdin(s, "helper", []string{"x"}, strings.NewReader(""), nil)
	if err != nil || code != 1 || !strings.Contains(out, "already used") {
		t.Errorf("a command that ran and failed is its status and output, for the caller to judge: %d %q %v", code, out, err)
	}
}

func TestExecWithStdinReturnsAFailureToStart(t *testing.T) {
	s := &execServer{err: errors.New("instance is not running")}
	if _, _, err := ExecWithStdin(s, "helper", []string{"x"}, strings.NewReader(""), nil); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("%v", err)
	}
}

// hangOp is an exec that never finishes: Wait blocks until the operation is cancelled, as the real one does when a guest command never exits.
type hangOp struct {
	incus.Operation
	cancelled chan struct{}
}

func (o hangOp) Wait() error { <-o.cancelled; return errors.New("operation cancelled") }
func (o hangOp) Cancel() error {
	close(o.cancelled)
	return nil
}
func (o hangOp) Get() api.Operation { return api.Operation{} }

type hangServer struct {
	incus.InstanceServer
	op hangOp
}

func (s *hangServer) ExecInstance(_ string, _ api.InstanceExecPost, _ *incus.InstanceExecArgs) (incus.Operation, error) {
	return s.op, nil
}

func TestExecInGuestReturnsAtTheDeadlineWhenTheGuestNeverAnswers(t *testing.T) {
	s := &hangServer{op: hangOp{cancelled: make(chan struct{})}}
	done := make(chan error, 1)
	go func() {
		_, _, err := ExecInGuestContext(context.Background(), s, "vm", []string{"sleep", "infinity"}, 1, 0, 50*time.Millisecond)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrExecTimeout) {
			t.Fatalf("want ErrExecTimeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exec did not return at its deadline")
	}
	select {
	case <-s.op.cancelled:
	default:
		t.Error("the operation was not cancelled, so the command keeps running in the guest")
	}
}

func TestExecInGuestStopsWhenTheContextIsCancelled(t *testing.T) {
	s := &hangServer{op: hangOp{cancelled: make(chan struct{})}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := ExecInGuestContext(ctx, s, "vm", []string{"sleep", "infinity"}, 1, 0, time.Hour)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrExecTimeout) {
			t.Fatalf("want context.Canceled and not a timeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the exec did not return after the context was cancelled")
	}
}

func TestExecInGuestDoesNotWaitOutTheAgentRetryDelayOnceCancelled(t *testing.T) {
	s := &execServer{err: errors.New(agentOfflineMessage)}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, _, err := ExecInGuestContext(ctx, s, "vm", []string{"true"}, 30, time.Hour, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("the retry loop slept through the cancellation")
	}
}

func TestExecOutputIsCapped(t *testing.T) {
	big := strings.Repeat("x", MaxExecOutput+4096)
	s := &execServer{out: big}
	_, out, err := ExecInGuest(s, "vm", []string{"cat", "/big"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxExecOutput+200 {
		t.Fatalf("output is %d bytes, want it capped near %d", len(out), MaxExecOutput)
	}
	if !strings.Contains(out, "output truncated") {
		t.Error("a truncated output must say so")
	}
	if !strings.HasPrefix(out, "xxxx") {
		t.Error("the start of the output must be kept")
	}
}

// lostListenerOp is an exec whose event stream dropped: the Incus client (since 7.5) fails the operation with this message even though the
// command is still running on the server.
type lostListenerOp struct {
	incus.Operation
	err error
}

func (o lostListenerOp) Wait() error { return o.err }
func (o lostListenerOp) Get() api.Operation {
	return api.Operation{ID: "op-1"}
}

// pollServer runs an exec whose operation fails as above and answers GetOperationWait from a list, one answer per call (the last repeats).
type pollServer struct {
	incus.InstanceServer
	op      lostListenerOp
	answers []func() (*api.Operation, error)
	calls   int
}

func (s *pollServer) ExecInstance(_ string, _ api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	args.Stdout.Write([]byte("out\n"))
	close(args.DataDone)
	return s.op, nil
}

func (s *pollServer) GetOperationWait(id string, _ int) (*api.Operation, string, error) {
	if id != "op-1" {
		return nil, "", errors.New("asked for the wrong operation: " + id)
	}
	i := min(s.calls, len(s.answers)-1)
	s.calls++
	o, err := s.answers[i]()
	return o, "", err
}

func running() (*api.Operation, error) {
	return &api.Operation{ID: "op-1", StatusCode: api.Running}, nil
}
func finished(code float64, errText string) func() (*api.Operation, error) {
	return func() (*api.Operation, error) {
		return &api.Operation{ID: "op-1", StatusCode: api.Success, Err: errText, Metadata: map[string]any{"return": code}}, nil
	}
}

func shortListenerWait(t *testing.T) {
	t.Helper()
	w, r := lostListenerWindow, lostListenerRetry
	lostListenerWindow, lostListenerRetry = 30*time.Millisecond, time.Millisecond
	t.Cleanup(func() { lostListenerWindow, lostListenerRetry = w, r })
}

func lostListener() error {
	return errors.New(lostListenerMessage + ": websocket: close 1006 (abnormal closure): unexpected EOF")
}

// The event stream dropped while the command ran: the exec is still waited for, by asking the server, and its exit status comes from the
// server's answer. Before this, the exec was reported failed, and the command was killed when tink closed its connections.
func TestExecWaitsOnTheServerWhenTheEventStreamDrops(t *testing.T) {
	shortListenerWait(t)
	s := &pollServer{op: lostListenerOp{err: lostListener()}, answers: []func() (*api.Operation, error){running, running, finished(3, "")}}
	code, out, err := ExecWithStdin(s, "probe", []string{"sh", "-c", "sleep 25"}, strings.NewReader(""), nil)
	if err != nil || code != 3 || out != "out\n" {
		t.Fatalf("the command ended with status 3 and no error: %d %q %v", code, out, err)
	}
	if s.calls != 3 {
		t.Errorf("it should have asked until the operation was final: %d requests", s.calls)
	}
}

func TestExecWaitKeepsTheServersErrorWhenTheOperationEndedInOne(t *testing.T) {
	shortListenerWait(t)
	s := &pollServer{op: lostListenerOp{err: lostListener()}, answers: []func() (*api.Operation, error){finished(0, "the instance was stopped")}}
	_, _, err := ExecWithStdin(s, "probe", []string{"x"}, strings.NewReader(""), nil)
	if err == nil || !strings.Contains(err.Error(), "the instance was stopped") || strings.Contains(err.Error(), lostListenerMessage) {
		t.Errorf("the operation's own error is the one to report: %v", err)
	}
}

func TestExecWaitGivesTheOriginalErrorWhenTheServerCannotBeReached(t *testing.T) {
	shortListenerWait(t)
	down := func() (*api.Operation, error) { return nil, errors.New("connection refused") }
	s := &pollServer{op: lostListenerOp{err: lostListener()}, answers: []func() (*api.Operation, error){down}}
	_, _, err := ExecWithStdin(s, "probe", []string{"x"}, strings.NewReader(""), nil)
	if err == nil || !strings.Contains(err.Error(), lostListenerMessage) {
		t.Errorf("with the server unreachable the client's error stands: %v", err)
	}
	if s.calls < 2 {
		t.Errorf("it should have tried more than once: %d", s.calls)
	}
}

func TestExecWaitGivesTheOriginalErrorWhenTheServerNoLongerKnowsTheOperation(t *testing.T) {
	shortListenerWait(t)
	gone := func() (*api.Operation, error) { return nil, api.StatusErrorf(404, "Operation not found") }
	s := &pollServer{op: lostListenerOp{err: lostListener()}, answers: []func() (*api.Operation, error){gone}}
	_, _, err := ExecWithStdin(s, "probe", []string{"x"}, strings.NewReader(""), nil)
	if err == nil || !strings.Contains(err.Error(), lostListenerMessage) || s.calls != 1 {
		t.Errorf("a 404 is final, no retries: %d requests, %v", s.calls, err)
	}
}

// Any other error from the operation is returned as it came, without asking the server (the VM-agent retry depends on its text). The server
// here has no GetOperationWait, so a request to it would panic.
func TestExecWaitReturnsOtherErrorsUnchanged(t *testing.T) {
	s := &pollServer{op: lostListenerOp{err: errors.New("VM agent isn't currently running")}}
	_, _, err := ExecWithStdin(s, "vm", []string{"x"}, strings.NewReader(""), nil)
	if err == nil || !strings.Contains(err.Error(), "VM agent isn't currently running") || s.calls != 0 {
		t.Errorf("%d requests, %v", s.calls, err)
	}
}
