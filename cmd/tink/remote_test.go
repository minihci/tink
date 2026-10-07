package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/incusapi"
)

// run executes the real root command with args and returns its error. Only commands that refuse before
// touching Incus are run this way.
func runRoot(t *testing.T, args ...string) error {
	t.Helper()
	t.Cleanup(func() { incusapi.UseRemote("") })
	var out, errb bytes.Buffer
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(&out)
	root.SetErr(&errb)
	return root.Execute()
}

func TestHostLocalCommandsRefuseUnderARemote(t *testing.T) {
	for _, args := range [][]string{
		{"--remote", "tron", "deploy"},
		{"--remote", "tron", "ingress", "reconcile"},
		{"--remote", "tron", "ingress", "status"},
		{"--remote", "tron", "daemon", "run"},
	} {
		err := runRoot(t, args...)
		if err == nil || !strings.Contains(err.Error(), `remote "tron"`) || !strings.Contains(err.Error(), "works on the host it runs on") {
			t.Errorf("%v: expected a refusal naming the remote, got %v", args, err)
		}
	}
}

func TestEnvironmentChoosesTheRemoteToo(t *testing.T) {
	t.Setenv("TINK_REMOTE", "tron")
	err := runRoot(t, "deploy")
	if err == nil || !strings.Contains(err.Error(), `remote "tron"`) {
		t.Errorf("$TINK_REMOTE must apply: %v", err)
	}
}

func TestTheFlagBeatsTheEnvironment(t *testing.T) {
	t.Setenv("TINK_REMOTE", "tron")
	err := runRoot(t, "--remote", "other", "deploy")
	if err == nil || !strings.Contains(err.Error(), `remote "other"`) {
		t.Errorf("--remote must win over $TINK_REMOTE: %v", err)
	}
}

func TestLocalIsNotARemote(t *testing.T) {
	t.Cleanup(func() { incusapi.UseRemote("") })
	incusapi.UseRemote("local")
	if err := refuseUnderRemote("tink deploy")(nil, nil); err != nil {
		t.Errorf("'local' is the local daemon, not a remote: %v", err)
	}
	incusapi.UseRemote("")
	if err := refuseUnderRemote("tink deploy")(nil, nil); err != nil {
		t.Errorf("no remote must not refuse: %v", err)
	}
}
