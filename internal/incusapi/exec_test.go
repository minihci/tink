package incusapi

import (
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

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
