package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/volbackup"
)

const verifyStack = `kind: storage-volume
name: lib
backup:
  snapshots: {schedule: "0 3 * * *", retain: 14d}
  verify:
    every: weekly
    check:
      image: docker-oci:library/alpine:3
      command: [sh, -c, "test -s /data/db"]
`

// seeVerify replaces what `backup verify` does against a server and records what it was asked to do.
func seeVerify(t *testing.T, res volbackup.VerifyResult, err error) (*volbackup.Volume, *volbackup.VerifyOptions) {
	t.Helper()
	var gotV volbackup.Volume
	var gotOpts volbackup.VerifyOptions
	called := false
	old := verifyOn
	verifyOn = func(_ string, v volbackup.Volume, opts volbackup.VerifyOptions) (volbackup.VerifyResult, error) {
		called, gotV, gotOpts = true, v, opts
		return res, err
	}
	t.Cleanup(func() { verifyOn = old })
	t.Cleanup(func() {
		if !called {
			t.Errorf("the command never reached the verification")
		}
	})
	return &gotV, &gotOpts
}

func TestVerifyCheckProjectReachesTheVerification(t *testing.T) {
	stack := filepath.Join(t.TempDir(), "tink.yaml")
	if err := os.WriteFile(stack, []byte(verifyStack), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want string // the check project the verification is told about
	}{
		{"the flag", []string{"--check-project", "tink-verify"}, "tink-verify"},
		{"the flag with an equals sign", []string{"--check-project=tink-verify"}, "tink-verify"},
		{"no flag means the volume's own project", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, opts := seeVerify(t, volbackup.VerifyResult{Snapshot: "snap0", With: "check", Recorded: true}, nil)
			args := append([]string{"backup", "verify", "lib", "-f", stack, "--project", "tink", "--pool", "fast"}, tc.args...)

			out, err := runCLI(t, args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if opts.CheckProject != tc.want {
				t.Errorf("check project = %q, want %q", opts.CheckProject, tc.want)
			}
			// the flag changes where the check runs and nothing else: the volume is still the one the stack and --project/--pool name,
			// and the check is still the one the stack declares
			if v.Project != "tink" || v.Pool != "fast" || v.Name != "lib" {
				t.Errorf("the volume = %+v: --check-project must not move it", *v)
			}
			if opts.Check == nil || opts.Check.Image != "docker-oci:library/alpine:3" {
				t.Errorf("the declared check = %+v", opts.Check)
			}
			if !strings.Contains(out, "verified lib@snap0") {
				t.Errorf("output = %q", out)
			}
		})
	}
}

func TestVerifyReportsAMissingCheckProjectAsItIs(t *testing.T) {
	seeVerify(t, volbackup.VerifyResult{}, errors.New("project tink-verify does not exist: create it, see docs/volume-backup.md"))
	out, err := runCLI(t, "backup", "verify", "lib", "--check-project", "tink-verify")
	if err == nil || !strings.Contains(err.Error(), "project tink-verify does not exist: create it, see docs/volume-backup.md") {
		t.Fatalf("err = %v\n%s", err, out)
	}
}

// Restoring is unchanged: it makes its volume in the volume's own project, so it has nothing to take a check project for.
func TestOnlyVerifyTakesACheckProject(t *testing.T) {
	_, err := runCLI(t, "backup", "restore", "lib", "--check-project", "tink-verify")
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --check-project") {
		t.Errorf("restore must not accept --check-project: %v", err)
	}
	help, err := runCLI(t, "backup", "verify", "--help")
	if err != nil || !strings.Contains(help, "--check-project") || !strings.Contains(help, "docs/volume-backup.md") {
		t.Errorf("verify's help must describe --check-project and say where the recipe is: %v\n%s", err, help)
	}
	help, err = runCLI(t, "backup", "restore", "--help")
	if err != nil || strings.Contains(help, "check-project") {
		t.Errorf("restore's help must not mention it: %v\n%s", err, help)
	}
}
