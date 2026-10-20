package resolve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

func TestOutputMatches_IgnoresOnlyTrailingWhitespace(t *testing.T) {
	for name, c := range map[string]struct {
		got, want string
		match     bool
	}{
		"identical":                  {"a\nb", "a\nb", true},
		"policy get adds a newline":  {"a\nb\n", "a\nb", true},
		"declared has one, live not": {"a\nb", "a\nb\n", true},
		"several trailing":           {"a \r\n\n", "a", true},
		"a different byte":           {"a\nc", "a\nb", false},
		"leading space matters":      {" a", "a", false},
		"inner whitespace matters":   {"a  b", "a b", false},
		"CRLF inside is not folded":  {"a\r\nb", "a\nb", false},
		"empty against content":      {"", "a", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := outputMatches(c.got, c.want); got != c.match {
				t.Errorf("outputMatches(%q, %q) = %v, want %v", c.got, c.want, got, c.match)
			}
		})
	}
}

func fixedRun(code int, out string, err error) func([]string) (int, string, error) {
	return func([]string) (int, string, error) { return code, out, err }
}

func TestOutputCheckConverged(t *testing.T) {
	c := &OutputCheck{Command: []string{"get"}, Equals: "policy", Expected: "{\n}\n"}
	cases := map[string]struct {
		run       func([]string) (int, string, error)
		converged bool
		why       string
		err       string
	}{
		"equal":               {fixedRun(0, "{\n}\n", nil), true, "", ""},
		"equal but a newline": {fixedRun(0, "{\n}", nil), true, "", ""},
		"differs":             {fixedRun(0, "{}", nil), false, "differs from file/policy", ""},
		"check failed":        {fixedRun(1, "acl policy not found", nil), false, "exited 1", ""},
		"could not run":       {fixedRun(0, "", errors.New("instance gone")), false, "", "instance gone"},
		"too much to compare": {fixedRun(0, strings.Repeat("x", maxCheckOutput+1), nil), false, "", "too much to compare"},
		"a failed check's output is not compared": {fixedRun(2, "{\n}\n", nil), false, "exited 2", ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ok, why, err := outputCheckConverged(tc.run, c)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want one containing %q", err, tc.err)
				}
				return
			}
			if err != nil || ok != tc.converged || !strings.Contains(why, tc.why) || (tc.why == "" && why != "") {
				t.Errorf("= (%v, %q, %v), want (%v, containing %q, nil)", ok, why, err, tc.converged, tc.why)
			}
		})
	}
}

func loadStack(t *testing.T, stack string) ([]Resource, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stack.yaml"), []byte(stack), 0o644); err != nil {
		t.Fatal(err)
	}
	return LoadFile(filepath.Join(dir, "stack.yaml"))
}

const policyStack = `kind: file
name: policy
instance: hs
path: /etc/policy.hujson
content: "{}"
---
kind: exec
name: set
instance: hs
command: [headscale, policy, set, -f, /etc/policy.hujson]
check_output:
  command: [headscale, policy, get]
  equals: policy
`

func TestLoadFile_ParsesExecCheckOutput(t *testing.T) {
	rs, err := loadStack(t, policyStack)
	if err != nil {
		t.Fatal(err)
	}
	var e Resource
	for _, r := range rs {
		if r.Kind == KindExec {
			e = r
		}
	}
	if e.CheckOutput == nil || e.CheckOutput.Equals != "policy" || strings.Join(e.CheckOutput.Command, " ") != "headscale policy get" {
		t.Fatalf("CheckOutput = %+v", e.CheckOutput)
	}
	if e.CheckOutput.Expected != "" {
		t.Errorf("Expected comes from the graph builder, not the YAML: %q", e.CheckOutput.Expected)
	}
}

func TestLoadFile_ExecNeedsExactlyOneGuard(t *testing.T) {
	head := "kind: exec\nname: x\ninstance: hs\ncommand: [true]\n"
	check := "check: [true]\n"
	triggers := "triggers: [a]\n"
	checkOutput := "check_output: {command: [get], equals: p}\n"
	for name, c := range map[string]struct {
		body string
		want string
	}{
		"none":                   {"", "requires one of check, check_output or triggers"},
		"check and triggers":     {check + triggers, "mutually exclusive"},
		"check and output":       {check + checkOutput, "mutually exclusive"},
		"output and triggers":    {checkOutput + triggers, "mutually exclusive"},
		"all three":              {check + checkOutput + triggers, "mutually exclusive"},
		"output without equals":  {"check_output: {command: [get]}\n", "requires both command and equals"},
		"output without command": {"check_output: {equals: p}\n", "requires both command and equals"},
		"an unknown key inside":  {"check_output: {command: [get], equals: p, extra: 1}\n", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadStack(t, head+c.body)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
	if _, err := loadStack(t, head+checkOutput); err != nil {
		t.Errorf("check_output alone must load (the equals check is the graph's): %v", err)
	}
}

func TestValidate_CheckOutputBelongsToExecOnly(t *testing.T) {
	r := Resource{Kind: KindFile, Name: "f", Instance: "i", CheckOutput: &OutputCheck{Command: []string{"x"}, Equals: "y"}}
	if err := Validate(r); err == nil {
		t.Error("check_output on a file resource must be rejected")
	}
}

func TestValidateCheckOutputs(t *testing.T) {
	file := Resource{Kind: KindFile, Name: "policy", Instance: "hs", Content: "{}"}
	exec := func(equals string) Resource {
		return Resource{Kind: KindExec, Name: "set", Instance: "hs", CheckOutput: &OutputCheck{Command: []string{"get"}, Equals: equals}}
	}
	if err := validateCheckOutputs([]Resource{file, exec("policy")}); err != nil {
		t.Errorf("valid case: %v", err)
	}
	for name, c := range map[string]struct {
		rs   []Resource
		want string
	}{
		"names nothing":     {[]Resource{file, exec("nothing")}, "not a resource in this stack"},
		"a near miss":       {[]Resource{file, exec("polcy")}, `did you mean "policy"?`},
		"names an instance": {[]Resource{{Kind: KindInstance, Name: "policy"}, exec("policy")}, "not a file"},
		"another instance":  {[]Resource{{Kind: KindFile, Name: "policy", Instance: "other", Content: "{}"}, exec("policy")}, `instance "other"`},
		"no content":        {[]Resource{{Kind: KindFile, Name: "policy", Instance: "hs"}, exec("policy")}, "no declared content"},
	} {
		t.Run(name, func(t *testing.T) {
			err := validateCheckOutputs(c.rs)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestLevels_AnExecWithCheckOutputWaitsForItsFileAndCompareswithItsContent(t *testing.T) {
	rs, err := loadStack(t, policyStack)
	if err != nil {
		t.Fatal(err)
	}
	// declared the other way round on purpose: the edge, not the order in the file, must put the file first
	rs[0], rs[1] = rs[1], rs[0]
	var original *OutputCheck
	for _, r := range rs {
		if r.Kind == KindExec {
			original = r.CheckOutput
		}
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	var exec Resource
	for i, l := range levels {
		for _, r := range l {
			pos[r.Name] = i
			if r.Kind == KindExec {
				exec = r
			}
		}
	}
	if pos["policy"] >= pos["set"] {
		t.Errorf("the file must be placed before the exec runs: levels %v", pos)
	}
	if exec.CheckOutput == nil || exec.CheckOutput.Expected != "{}" {
		t.Errorf("Expected = %+v, want the file's content", exec.CheckOutput)
	}
	// Levels updates the caller's resources in place (as it does for project inheritance), so the resource now holds a resolved copy; the
	// OutputCheck it held before must not have been written through.
	if original.Expected != "" {
		t.Error("the original OutputCheck must not be written through")
	}
}

// execFake answers the three calls an exec's planning and running make: GetInstance, and ExecInstance with a configurable live state.
type execFake struct {
	incus.InstanceServer
	missing  bool
	live     string // what the check command prints; "" with liveCode 1 means "no policy yet"
	liveCode int
	setTo    string // what the command makes live
	setCode  int
	ran      []string
}

type fakeOp struct {
	incus.Operation
	code int
}

func (o fakeOp) Wait() error { return nil }
func (o fakeOp) Get() api.Operation {
	return api.Operation{Metadata: map[string]any{"return": float64(o.code)}}
}

func (s *execFake) UseProject(string) incus.InstanceServer { return s }

func (s *execFake) GetInstance(name string) (*api.Instance, string, error) {
	if s.missing {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "Instance not found")
	}
	return &api.Instance{Name: name, Type: "container", InstancePut: api.InstancePut{Config: map[string]string{}}}, "", nil
}

func (s *execFake) ExecInstance(_ string, req api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	cmd := strings.Join(req.Command, " ")
	s.ran = append(s.ran, cmd)
	code, out := 0, ""
	switch cmd {
	case "headscale policy get":
		code, out = s.liveCode, s.live
	case "headscale policy set":
		code = s.setCode
		if code == 0 {
			s.live, s.liveCode = s.setTo, 0
		}
	}
	if args.Stdout != nil {
		_, _ = io.WriteString(args.Stdout, out)
	}
	close(args.DataDone)
	return fakeOp{code: code}, nil
}

func policyExec() Resource {
	return Resource{Kind: KindExec, Name: "set", Instance: "hs",
		Command:     []string{"headscale", "policy", "set"},
		CheckOutput: &OutputCheck{Command: []string{"headscale", "policy", "get"}, Equals: "policy", Expected: "{ \"grants\": [] }"}}
}

func TestPlanExec_CheckOutput(t *testing.T) {
	r := policyExec()
	for name, c := range map[string]struct {
		s       *execFake
		action  Action
		message string
	}{
		"the live policy is the declared one": {&execFake{live: "{ \"grants\": [] }\n"}, ActionNone, ""},
		"a different policy is live":          {&execFake{live: "{}\n"}, ActionCreate, "differs from file/policy"},
		"no policy yet (the check fails)":     {&execFake{liveCode: 1, live: "acl policy not found"}, ActionCreate, "exited 1"},
		"the instance does not exist yet":     {&execFake{missing: true}, ActionCreate, "would run"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := planExec(context.Background(), c.s, r)
			if err != nil {
				t.Fatal(err)
			}
			if p.Action != c.action {
				t.Fatalf("action = %v, want %v (changes %v)", p.Action, c.action, p.Changes)
			}
			if c.message != "" && (len(p.Changes) != 1 || !strings.Contains(p.Changes[0], c.message)) {
				t.Errorf("changes = %v, want one mentioning %q", p.Changes, c.message)
			}
			if strings.Contains(strings.Join(p.Changes, " "), "grants") {
				t.Errorf("plan must not print the content: %v", p.Changes)
			}
		})
	}
}

func TestRunExec_CheckOutput(t *testing.T) {
	r := policyExec()

	// not converged: runs the command, then the re-check passes
	s := &execFake{live: "{}\n", setTo: "{ \"grants\": [] }\n"}
	if err := runExec(context.Background(), s, r); err != nil {
		t.Fatalf("runExec: %v", err)
	}
	if got := strings.Join(s.ran, " | "); got != "headscale policy get | headscale policy set | headscale policy get" {
		t.Errorf("ran %q: want a check, the command, then a re-check", got)
	}

	// already converged: the command is not run
	s = &execFake{live: "{ \"grants\": [] }\n"}
	if err := runExec(context.Background(), s, r); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.ran, " | "); got != "headscale policy get" {
		t.Errorf("ran %q: a converged exec must not run its command", got)
	}

	// the command succeeds but does not make the declared policy live: an error, not a silent success
	s = &execFake{live: "{}\n", setTo: "{ \"other\": 1 }\n"}
	err := runExec(context.Background(), s, r)
	if err == nil || !strings.Contains(err.Error(), "did not take effect") || !strings.Contains(err.Error(), "differs from file/policy") {
		t.Errorf("err = %v, want a did-not-take-effect error", err)
	}

	// the command fails (a rejected policy): its own error, and no re-check
	s = &execFake{live: "{}\n", setCode: 1}
	err = runExec(context.Background(), s, r)
	if err == nil || !strings.Contains(err.Error(), "exit 1") {
		t.Errorf("err = %v, want the command's exit status", err)
	}
	if errors.Is(err, io.EOF) || strings.Count(strings.Join(s.ran, "|"), "headscale policy get") != 1 {
		t.Errorf("a failed command must not be re-checked: %v", s.ran)
	}

	// no policy yet (the check exits non-zero): the first set
	s = &execFake{liveCode: 1, live: "acl policy not found", setTo: "{ \"grants\": [] }\n"}
	if err := runExec(context.Background(), s, r); err != nil {
		t.Fatalf("the first set: %v", err)
	}
}

// hangOp is an exec that never finishes until it is cancelled, as a guest command that never exits.
type hangOp struct {
	incus.Operation
	cancelled chan struct{}
}

func (o hangOp) Wait() error        { <-o.cancelled; return errors.New("operation cancelled") }
func (o hangOp) Cancel() error      { close(o.cancelled); return nil }
func (o hangOp) Get() api.Operation { return api.Operation{} }

// hangFake is an instance whose every exec hangs.
type hangFake struct {
	execFake
	op hangOp
}

func (s *hangFake) ExecInstance(string, api.InstanceExecPost, *incus.InstanceExecArgs) (incus.Operation, error) {
	return s.op, nil
}

func TestPlanExec_ACheckThatHangsFailsAtTheResourcesTimeout(t *testing.T) {
	s := &hangFake{op: hangOp{cancelled: make(chan struct{})}}
	r := Resource{Kind: KindExec, Name: "hung", Instance: "vm", Check: []string{"sleep", "infinity"}, Command: []string{"true"}, Timeout: 50 * time.Millisecond}
	done := make(chan error, 1)
	go func() {
		_, err := planExec(context.Background(), s, r)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, incusapi.ErrExecTimeout) {
			t.Fatalf("want ErrExecTimeout, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("plan hung on a check that never returns")
	}
}
