package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func explainOf(t *testing.T, stack string) (stdout, stderr string, err error) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "tink.yaml")
	if err := os.WriteFile(file, []byte(stack), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newPlanCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"explain", "--socket", filepath.Join(t.TempDir(), "no-such.sock"), file})
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

// With no server to reach, explain still shows what the passes that need none made, and says there is no server.
func TestExplainWithNoServer(t *testing.T) {
	out, errOut, err := explainOf(t, `kind: stack
name: site
defaults:
  instance:
    config: {boot.autostart: "true"}
---
kind: instance
name: app
image: local-image
`)
	if err != nil {
		t.Fatalf("explain: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "no server:") {
		t.Errorf("stderr should say there is no server:\n%s", errOut)
	}
	for _, want := range []string{"stack site, as the passes expanded it", "defaults\n", "~ instance/app", "config.boot.autostart: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestExplainOfAStackThatNeedsNothing(t *testing.T) {
	out, _, err := explainOf(t, "kind: stack\nname: plain\n---\nkind: instance\nname: app\nimage: local-image\n")
	if err != nil || !strings.Contains(out, "nothing was derived") {
		t.Errorf("err %v, out:\n%s", err, out)
	}
}

// A pass that needs the server says so and stops, and the passes before it are still shown.
func TestExplainStopsAtAPassThatNeedsTheServer(t *testing.T) {
	out, _, err := explainOf(t, `kind: stack
name: site
network: own
defaults:
  instance:
    config: {boot.autostart: "true"}
---
kind: instance
name: app
image: local-image
`)
	if err == nil || !strings.Contains(err.Error(), "none is connected") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(out, "defaults\n") || !strings.Contains(out, "the next one stopped") {
		t.Errorf("the passes that ran should be shown:\n%s", out)
	}
}
