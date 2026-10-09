package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "app.env")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEnvFileIsReadLikeDockerReadsIt(t *testing.T) {
	t.Setenv("FROM_HOST", "inherited")
	t.Setenv("UNSET_ELSEWHERE", "")
	os.Unsetenv("UNSET_ELSEWHERE")
	p := writeEnvFile(t, "# a comment\n\nTZ=America/Denver\r\nDB_URL=postgres://u:p@db/x?a=b\nQUOTED=\"kept as written\"\nEMPTY=\nFROM_HOST\nUNSET_ELSEWHERE\n  # indented comment\n")
	spec, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{p}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"environment.TZ":        "America/Denver",          // CRLF handled
		"environment.DB_URL":    "postgres://u:p@db/x?a=b", // only the first = splits
		"environment.QUOTED":    `"kept as written"`,       // Docker does not strip quotes
		"environment.EMPTY":     "",                        // set, and empty
		"environment.FROM_HOST": "inherited",               // a bare name is taken from this process
	}
	got := withoutStamp(spec.Config)
	for k, v := range want {
		if gv, ok := got[k]; !ok || gv != v {
			t.Errorf("%s = %q (present %v), want %q", k, gv, ok, v)
		}
	}
	if _, there := got["environment.UNSET_ELSEWHERE"]; there {
		t.Error("a bare name that is not set here is skipped, not set empty")
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}
}

func TestDashEWinsOverTheEnvFileAndLaterFilesOverEarlier(t *testing.T) {
	a := writeEnvFile(t, "A=file1\nB=file1\n")
	b := writeEnvFile(t, "B=file2\nC=file2\n")
	spec, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{a, b}, Env: []string{"C=flag"}})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"environment.A": "file1", "environment.B": "file2", "environment.C": "flag"} {
		if spec.Config[k] != want {
			t.Errorf("%s = %q, want %q", k, spec.Config[k], want)
		}
	}
}

func TestEnvFileErrorsNameTheFileAndLine(t *testing.T) {
	if _, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{filepath.Join(t.TempDir(), "missing.env")}}); err == nil || !strings.Contains(err.Error(), "--env-file") {
		t.Errorf("missing file: %v", err)
	}
	p := writeEnvFile(t, "OK=1\nhas space=2\n")
	_, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{p}})
	if err == nil || !strings.Contains(err.Error(), p+":2") || !strings.Contains(err.Error(), "not a variable name") {
		t.Errorf("bad name: %v", err)
	}
	p = writeEnvFile(t, "=oops\n")
	if _, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{p}}); err == nil {
		t.Error("an empty name must be refused")
	}
}

func TestTheEnvFilesValuesAreNeverInTheRecordedCommand(t *testing.T) {
	p := writeEnvFile(t, "DB_PASSWORD=hunter2\n")
	spec, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{p}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := spec.Config[KeyCommand]
	if !strings.Contains(cmd, "--env-file "+p) || strings.Contains(cmd, "hunter2") {
		t.Errorf("the path is recorded, never the values: %s", cmd)
	}
	if spec.Config["environment.DB_PASSWORD"] != "hunter2" {
		t.Errorf("but the value is set: %v", spec.Config)
	}
}

func TestIncusConfigCannotOverrideAnEnvFileVariable(t *testing.T) {
	p := writeEnvFile(t, "A=1\n")
	_, err := Build(Options{Name: "n", Image: "i", EnvFile: []string{p}, IncusConfig: []string{"environment.A=2"}})
	if err == nil || !strings.Contains(err.Error(), "-e or --env-file") {
		t.Errorf("err = %v", err)
	}
}

func TestPrivilegedSetsTheKeyAndSaysWhatItIsNot(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", Privileged: true})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Config["security.privileged"] != "true" {
		t.Errorf("config = %v", spec.Config)
	}
	joined := strings.Join(spec.Notes, "\n")
	for _, want := range []string{"user-namespace", "host's root", "--device"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the note should say %q: %s", want, joined)
		}
	}
	if !strings.Contains(spec.Config[KeyCommand], "--privileged") {
		t.Errorf("recorded: %s", spec.Config[KeyCommand])
	}
	if spec, _ := Build(Options{Name: "n", Image: "i"}); spec.Config["security.privileged"] != "" || len(spec.Notes) != 0 {
		t.Errorf("not asked for, not said: %v %v", spec.Config, spec.Notes)
	}
	if _, err := Build(Options{Name: "n", Image: "i", Privileged: true, IncusConfig: []string{"security.privileged=false"}}); err == nil || !strings.Contains(err.Error(), "--privileged") {
		t.Errorf("err = %v", err)
	}
}
