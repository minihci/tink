package main

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/prepare"
)

var sharedStackFlags = []string{"identity", "offline", "secrets", "socket"}

func findCmd(t *testing.T, root *cobra.Command, path ...string) *cobra.Command {
	t.Helper()
	c, _, err := root.Find(path)
	if err != nil || c == nil || c.Name() != path[len(path)-1] {
		t.Fatalf("no command %v: %v", path, err)
	}
	return c
}

// The flags `plan` and `plan apply` share are declared once, on `plan`, as persistent flags. `plan apply` has none of its own with those
// names, so it cannot drift from `plan` in a default or a help text, and a command added under `plan` inherits them.
func TestTheSharedStackFlagsAreDeclaredOnceOnPlan(t *testing.T) {
	root := newRootCmd()
	plan := findCmd(t, root, "plan")
	apply := findCmd(t, root, "plan", "apply")
	for _, name := range sharedStackFlags {
		if plan.PersistentFlags().Lookup(name) == nil {
			t.Errorf("plan --%s is not a persistent flag", name)
		}
		if apply.LocalNonPersistentFlags().Lookup(name) != nil {
			t.Errorf("plan apply declares its own --%s", name)
		}
		if apply.InheritedFlags().Lookup(name) != plan.PersistentFlags().Lookup(name) {
			t.Errorf("plan apply does not inherit plan's --%s", name)
		}
	}
}

// Both commands turn the same command line into the same request for the preparation, and neither does any preparing of its own: the
// request is all they hand over. A divergence in how either reads a flag or a file shows up as two different requests.
func TestPlanAndApplyAskForTheSamePreparation(t *testing.T) {
	var asked []prepare.Request
	stop := errors.New("stop here: preparation is faked")
	prepareStack = func(req prepare.Request) (*prepare.Prepared, error) {
		req.Redactor, req.Notice = nil, nil // a func cannot be compared; the others are the request
		asked = append(asked, req)
		return nil, stop
	}
	t.Cleanup(func() { prepareStack = prepare.Prepare })

	args := []string{"--socket", "/run/example.sock", "--offline", "--secrets", "store.yaml", "--identity", "id.txt", "a.yaml", "b.yaml"}
	for _, command := range [][]string{{"plan"}, {"plan", "apply"}} {
		var out, errOut bytes.Buffer
		root := newRootCmd()
		root.SetOut(&out)
		root.SetErr(&errOut)
		root.SetArgs(append(append([]string{}, command...), args...))
		if err := root.Execute(); !errors.Is(err, stop) {
			t.Fatalf("tink %s: err = %v, want the faked preparation's", strings.Join(command, " "), err)
		}
	}
	want := prepare.Request{
		Files:       []string{"a.yaml", "b.yaml"},
		Socket:      "/run/example.sock",
		SecretStore: "store.yaml",
		Identity:    "id.txt",
	}
	if len(asked) != 2 || !reflect.DeepEqual(asked[0], want) || !reflect.DeepEqual(asked[1], want) {
		t.Errorf("plan and plan apply asked for\n  %+v\n  %+v\nwant both to ask for\n  %+v", asked[0], asked[len(asked)-1], want)
	}
}
