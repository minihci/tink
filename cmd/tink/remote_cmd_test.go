package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "token")
	if err := os.WriteFile(f, []byte("  abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		file, env, stdin string
		want, wantErr    string
	}{
		"from a file, trimmed":         {f, "", "", "abc123", ""},
		"from stdin":                   {"-", "", "from-stdin\n", "from-stdin", ""},
		"from the environment":         {"", "from-env", "", "from-env", ""},
		"a file beats the environment": {f, "from-env", "", "abc123", ""},
		"none at all is fine":          {"", "", "", "", ""},
		"an empty file is an error":    {empty, "", "", "", "empty"},
		"a missing file is an error":   {filepath.Join(dir, "nope"), "", "", "", "reading the token file"},
	} {
		got, err := readToken(tc.file, tc.env, strings.NewReader(tc.stdin))
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", name, got, err, tc.want)
		}
	}
}

func TestConfirmFingerprint(t *testing.T) {
	const fp = "abcdef0123"
	for in, want := range map[string]bool{"y\n": true, "YES\n": true, fp + "\n": true, "ABCDEF0123\n": true, "n\n": false, "\n": false, "maybe\n": false, "deadbeef\n": false} {
		var out bytes.Buffer
		got, err := confirmFingerprint(strings.NewReader(in), &out)(fp)
		if err != nil || got != want {
			t.Errorf("input %q: got %v, %v; want %v", in, got, err, want)
		}
		if !strings.Contains(out.String(), fp) {
			t.Errorf("the prompt must show the fingerprint: %q", out.String())
		}
	}
}

func TestRemoteCommandsOnAnEmptyConfiguration(t *testing.T) {
	t.Setenv("INCUS_CONF", filepath.Join(t.TempDir(), "incus"))
	t.Setenv("TINK_REMOTE_TOKEN", "")
	run := func(args ...string) (string, error) {
		var out, errb bytes.Buffer
		root := newRootCmd()
		root.SetArgs(args)
		root.SetOut(&out)
		root.SetErr(&errb)
		err := root.Execute()
		return out.String() + errb.String(), err
	}
	out, err := run("remote", "list")
	if err != nil || !strings.Contains(out, "local") || !strings.Contains(out, "built in, used when not configured") || !strings.Contains(out, "docker-oci") {
		t.Errorf("list: %v\n%s", err, out)
	}
	if _, err := run("remote", "remove", "nope"); err == nil || !strings.Contains(err.Error(), `no remote "nope"`) {
		t.Errorf("remove of an unknown remote: %v", err)
	}
	if _, err := run("remote", "remove", "local"); err == nil {
		t.Error("the local remote cannot be removed")
	}
	// no address and no token: nothing to connect to
	if _, err := run("remote", "add", "tron"); err == nil || !strings.Contains(err.Error(), "no server address") {
		t.Errorf("add without an address: %v", err)
	}
	// an address, nothing to verify it, and (under test) nobody to ask
	if _, err := run("remote", "add", "x:y", "10.0.0.1"); err == nil || !strings.Contains(err.Error(), "colon") {
		t.Errorf("a name with a colon: %v", err)
	}
}
