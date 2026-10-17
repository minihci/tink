package main

import (
	"io"
	"strings"
	"testing"
)

func TestHelperInstallTakesAnAPIViaAndRefusesAnythingElseBeforeConnecting(t *testing.T) {
	cmd := newHelperInstallCmd()
	f := cmd.Flags().Lookup("api-via")
	if f == nil || f.DefValue != "auto" {
		t.Fatalf("--api-via defaults to auto: %+v", f)
	}
	for _, want := range []string{"auto", "network", "proxy"} {
		if !strings.Contains(f.Usage, want) {
			t.Errorf("the usage names %q: %s", want, f.Usage)
		}
	}
	if !strings.Contains(cmd.Long, "--api-via") || !strings.Contains(cmd.Long, "egress ACL") {
		t.Errorf("the long help describes the two ways and what the network way exposes:\n%s", cmd.Long)
	}

	// a value that is not a way is refused before any connection is made: this test has no server, so a connection attempt would fail differently
	cmd = newHelperInstallCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--image", "x", "--api-via", "tunnel"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `--api-via "tunnel": expected auto, network or proxy`) {
		t.Errorf("err = %v", err)
	}
}
