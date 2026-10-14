package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/join"
)

const (
	joinPlatform = "../../internal/join/testdata/platform.yaml"
	joinCanary   = "../../internal/join/testdata/canary.yaml"
	joinGolden   = "../../internal/join/testdata/golden/"
)

func runJoinCmd(t *testing.T, args []string, only string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errw bytes.Buffer
	err = runJoin(&out, &errw, args, only)
	return out.String(), errw.String(), err
}

func readJoinFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJoin_PrintsEverySectionAndNothingToStderr(t *testing.T) {
	out, errw, err := runJoinCmd(t, []string{joinPlatform, joinCanary}, "")
	if err != nil {
		t.Fatal(err)
	}
	if errw != "" {
		t.Errorf("stderr = %q, want nothing", errw)
	}
	for _, name := range []string{"dns", "mesh", "caddy", "sso", "acl"} {
		if !strings.Contains(out, "== "+name+": ") {
			t.Errorf("no %s section in:\n%s", name, out)
		}
		if body := readJoinFixture(t, joinGolden+name+".txt"); !strings.Contains(out, body) {
			t.Errorf("the %s body is not the golden one:\n%s", name, out)
		}
	}
}

func TestJoin_OnlyPrintsOneBodyAloneForDiffing(t *testing.T) {
	out, _, err := runJoinCmd(t, []string{joinPlatform, joinCanary}, "dns")
	if err != nil {
		t.Fatal(err)
	}
	if want := readJoinFixture(t, joinGolden+"dns.txt"); out != want {
		t.Errorf("--only dns = %q, want exactly %q", out, want)
	}
}

func TestJoin_OnlyNeedsAKnownSection(t *testing.T) {
	if _, _, err := runJoinCmd(t, []string{joinPlatform, joinCanary}, "routes"); err == nil || !strings.Contains(err.Error(), "the sections are dns, mesh, caddy, sso, acl") {
		t.Errorf("err = %v", err)
	}
}

func TestJoin_AnErrorPrintsNothingToStdoutAndSaysWhy(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "platform.yaml")
	if err := os.WriteFile(broken, []byte(strings.Replace(readJoinFixture(t, joinPlatform), "user.tink.role: dns", "user.tink.role: nothing", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errw, err := runJoinCmd(t, []string{broken, joinCanary}, "")
	if err == nil || !strings.Contains(err.Error(), "nothing was rendered") {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Errorf("stdout = %q: a failed join must print no rendering", out)
	}
	if !strings.Contains(errw, "error: dns helper: no instance has user.tink.role: dns") {
		t.Errorf("stderr = %q", errw)
	}
}

func TestJoin_AWarningIsPrintedBesideACompleteRendering(t *testing.T) {
	dir := t.TempDir()
	noMesh := filepath.Join(dir, "platform.yaml")
	if err := os.WriteFile(noMesh, []byte(strings.Replace(readJoinFixture(t, joinPlatform), "user.tink.role: mesh", "user.tink.role: nothing", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errw, err := runJoinCmd(t, []string{noMesh, joinCanary}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errw, "warning: mesh helper") || strings.Contains(out, "== mesh:") || !strings.Contains(out, "== acl:") {
		t.Errorf("stdout:\n%s\nstderr: %q", out, errw)
	}
	if _, _, err := runJoinCmd(t, []string{noMesh, joinCanary}, "mesh"); err == nil || !strings.Contains(err.Error(), "nothing to print for --only mesh") {
		t.Errorf("--only for a section that does not apply: %v", err)
	}
}

func TestJoin_AStackSplitAcrossFilesIsGivenWithCommas(t *testing.T) {
	dir := t.TempDir()
	whole := readJoinFixture(t, joinCanary)
	i := strings.Index(whole, "kind: publish\nname: whoami\n")
	a, b := filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yaml")
	// the publishes are in a second file that names no stack and no project: they only get the stack's project, and so only meet their instance,
	// if the two files are loaded together
	if err := os.WriteFile(a, []byte(whole[:i]), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte(whole[i:]), 0o600); err != nil {
		t.Fatal(err)
	}
	out, errw, err := runJoinCmd(t, []string{joinPlatform, a + "," + b}, "caddy")
	if err != nil {
		t.Fatalf("%v\n%s", err, errw)
	}
	if want := readJoinFixture(t, joinGolden+"caddy.txt"); out != want {
		t.Errorf("caddy =\n%s\nwant\n%s", out, want)
	}
	// given as two arguments instead, the second is a stack of its own, so its publishes are in the default project and cannot find the instance
	_, errw, err = runJoinCmd(t, []string{joinPlatform, a, b}, "")
	if err == nil || !strings.Contains(errw, `names instance "whoami" (project default)`) {
		t.Errorf("two files of one stack given separately should not join: err %v, stderr %q", err, errw)
	}
}

func TestJoin_AFileThatDoesNotLoadIsAnError(t *testing.T) {
	if _, _, err := runJoinCmd(t, []string{"no-such-file.yaml"}, ""); err == nil {
		t.Error("a missing file must be an error")
	}
}

// fakeJoinFiles is the instance file API in memory, for the apply command.
type fakeJoinFiles struct {
	files  map[string]string
	writes []string
}

func (f *fakeJoinFiles) Read(p, i, path string) (string, bool, error) {
	c, ok := f.files[p+"/"+i+path]
	return c, ok, nil
}

func (f *fakeJoinFiles) Write(p, i, path, content string) error {
	f.writes = append(f.writes, path)
	f.files[p+"/"+i+path] = content
	return nil
}

func newFakeJoinFiles() *fakeJoinFiles {
	return &fakeJoinFiles{files: map[string]string{
		"tink/dns/db.home.xlii.co": "$ORIGIN home.xlii.co.\n@ IN SOA ns.home.xlii.co. h.home.xlii.co. ( 1 3600 600 86400 30 )\nns IN A 10.0.142.230\nauth IN A 10.0.142.231\n",
	}}
}

func runApply(t *testing.T, f *fakeJoinFiles, section string, o join.ApplyOptions) (stdout, stderr string, err error) {
	t.Helper()
	var out, errw bytes.Buffer
	err = runJoinApply(&out, &errw, f, section, []string{joinPlatform, joinCanary}, o)
	return out.String(), errw.String(), err
}

func TestJoinApply_DryRunShowsTheChangeAndWritesNothing(t *testing.T) {
	f := newFakeJoinFiles()
	out, _, err := runApply(t, f, "dns", join.ApplyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"dns: /hosts on dns (project tink)", "  + 10.0.142.231 whoami.home.xlii.co", "  + 10.0.142.231 whoami-admin.home.xlii.co", "would create the file (dry run: nothing was written)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(f.writes) != 0 {
		t.Errorf("a dry run wrote %v", f.writes)
	}
}

func TestJoinApply_WritesTheFileAndASecondRunChangesNothing(t *testing.T) {
	f := newFakeJoinFiles()
	out, _, err := runApply(t, f, "dns", join.ApplyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "created, and read back") || !strings.HasPrefix(f.files["tink/dns/hosts"], "# Generated by `tink join apply dns`") {
		t.Errorf("output:\n%s\nfile: %q", out, f.files["tink/dns/hosts"])
	}
	out, _, err = runApply(t, f, "dns", join.ApplyOptions{})
	if err != nil || !strings.Contains(out, "no changes") || len(f.writes) != 1 {
		t.Errorf("second run: err %v, writes %d, output:\n%s", err, len(f.writes), out)
	}
}

func TestJoinApply_OnlyDNSCanBeApplied(t *testing.T) {
	f := newFakeJoinFiles()
	if _, _, err := runApply(t, f, "caddy", join.ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "only dns is built") {
		t.Errorf("caddy: %v", err)
	}
	if _, _, err := runApply(t, f, "routes", join.ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "is not a section") {
		t.Errorf("routes: %v", err)
	}
	if len(f.writes) != 0 {
		t.Error("nothing may be written")
	}
}

func TestJoinApply_AJoinErrorIsPrintedAndNothingIsWritten(t *testing.T) {
	dir := t.TempDir()
	broken := filepath.Join(dir, "platform.yaml")
	if err := os.WriteFile(broken, []byte(strings.Replace(readJoinFixture(t, joinPlatform), "user.tink.role: sso", "user.tink.role: nothing", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFakeJoinFiles()
	var out, errw bytes.Buffer
	err := runJoinApply(&out, &errw, f, "dns", []string{broken, joinCanary}, join.ApplyOptions{})
	if err == nil || !strings.Contains(err.Error(), "nothing was applied") || !strings.Contains(errw.String(), "error: sso helper") || len(f.writes) != 0 {
		t.Errorf("err %v, stderr %q, writes %v", err, errw.String(), f.writes)
	}
}
