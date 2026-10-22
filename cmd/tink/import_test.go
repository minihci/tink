package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runImport(t *testing.T, compose string) (stdout, stderr string, err error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compose.yaml")
	if err := os.WriteFile(path, []byte(compose), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newImportCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SilenceUsage = true // as the root command sets it
	cmd.SetArgs([]string{"--stack", "demo", path})
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestImportWritesTheStackToStdoutAndTheRestToStderr(t *testing.T) {
	out, errOut, err := runImport(t, "services:\n  web:\n    image: nginx\n    ports: ['8080:80']\n    healthcheck: {test: [CMD, true]}\n")
	if err != nil {
		t.Fatalf("import: %v\n%s", err, errOut)
	}
	if !strings.Contains(out, "kind: stack\nname: demo\n") || !strings.Contains(out, "kind: instance\nname: web\n") {
		t.Errorf("stdout is not the stack:\n%s", out)
	}
	if !strings.Contains(errOut, "warning: service web: healthcheck was dropped") {
		t.Errorf("a dropped key must be a warning on stderr:\n%s", errOut)
	}
	if strings.Contains(out, "warning") || strings.Contains(out, "note:") {
		t.Errorf("stdout must hold the stack only:\n%s", out)
	}
}

func TestImportRefusesAndWritesNothing(t *testing.T) {
	out, errOut, err := runImport(t, "services:\n  web:\n    image: nginx\n    build: .\n    network_mode: host\n")
	if err == nil {
		t.Fatal("a refused file must exit non-zero")
	}
	if out != "" {
		t.Errorf("a refused file must write nothing, got:\n%s", out)
	}
	if !strings.Contains(errOut, "refused: service web: build:") || !strings.Contains(errOut, "refused: service web: network_mode:") {
		t.Errorf("every refusal must be printed:\n%s", errOut)
	}
}
