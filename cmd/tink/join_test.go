package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/join"
	"github.com/minihci/tink/internal/resolve"
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

// ---- --from-server: the same join, built from what the server holds ----

// joinServer is a server holding the platform and canary stacks as `tink plan apply` leaves them: each stack planned, so its publishes are stamped onto its
// instances. It implements only the reads the join makes; any write panics through the nil embedded interface.
type joinServer struct {
	incus.InstanceServer
	instances []api.Instance
	profiles  []api.Profile
	networks  []api.Network
}

func (s *joinServer) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return s.instances, nil
}
func (s *joinServer) GetProfilesAllProjects() ([]api.Profile, error) { return s.profiles, nil }
func (s *joinServer) GetNetworks() ([]api.Network, error)            { return s.networks, nil }

func newJoinServer(t *testing.T) *joinServer {
	t.Helper()
	stacks, err := loadJoinStacks([]string{joinPlatform, joinCanary})
	if err != nil {
		t.Fatal(err)
	}
	s := &joinServer{}
	for _, st := range stacks {
		levels, err := resolve.Levels(st.Resources)
		if err != nil {
			t.Fatal(err)
		}
		for _, level := range levels {
			for _, r := range level {
				project := r.Project
				if project == "" {
					project = "default"
				}
				switch r.Kind {
				case resolve.KindInstance:
					s.instances = append(s.instances, api.Instance{Name: r.Name, Project: project, InstancePut: api.InstancePut{Config: r.Config, Devices: r.Devices, Profiles: r.Profiles}})
				case resolve.KindProfile:
					s.profiles = append(s.profiles, api.Profile{Name: r.Name, Project: project, ProfilePut: api.ProfilePut{Config: r.Config, Devices: r.Devices}})
				case resolve.KindNetwork:
					s.networks = append(s.networks, api.Network{Name: r.Name, Managed: true, NetworkPut: api.NetworkPut{Config: r.Config}})
				}
			}
		}
	}
	return s
}

func runFromServer(t *testing.T, s incus.InstanceServer, only string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errw bytes.Buffer
	err = runJoinFromServer(&out, &errw, s, only)
	return out.String(), errw.String(), err
}

func TestJoinFromServer_PrintsTheSameSectionsAsTheFiles(t *testing.T) {
	s := newJoinServer(t)
	fromServer, errw, err := runFromServer(t, s, "")
	if err != nil || errw != "" {
		t.Fatalf("err %v, stderr %q", err, errw)
	}
	fromFiles, _, err := runJoinCmd(t, []string{joinPlatform, joinCanary}, "")
	if err != nil {
		t.Fatal(err)
	}
	if fromServer != fromFiles {
		t.Errorf("from the server:\n%s\nfrom the files:\n%s", fromServer, fromFiles)
	}
	for _, name := range []string{"dns", "mesh", "caddy", "sso", "acl"} {
		one, _, err := runFromServer(t, s, name)
		if err != nil || one != readJoinFixture(t, joinGolden+name+".txt") {
			t.Errorf("--only %s: %v\n%s", name, err, one)
		}
	}
	if _, _, err := runFromServer(t, s, "routes"); err == nil || !strings.Contains(err.Error(), "the sections are dns, mesh, caddy, sso, acl") {
		t.Errorf("--only needs a known section: %v", err)
	}
}

func TestJoinFromServer_AStampThatCannotBeReadPrintsNothingAndNamesTheInstance(t *testing.T) {
	s := newJoinServer(t)
	for i := range s.instances {
		if s.instances[i].Name == "whoami" {
			cfg := map[string]string{"user.tink.publish.whoami": `{"allow":["family"]`}
			for k, v := range s.instances[i].Config {
				if k != "user.tink.publish.whoami" {
					cfg[k] = v
				}
			}
			s.instances[i].Config = cfg
		}
	}
	out, errw, err := runFromServer(t, s, "")
	if err == nil || !strings.Contains(err.Error(), "1 error(s): nothing was rendered") {
		t.Fatalf("err = %v", err)
	}
	if out != "" {
		t.Errorf("stdout = %q: a failed join must print no rendering", out)
	}
	if !strings.Contains(errw, "error: instance/whoami: (project canary) user.tink.publish.whoami:") {
		t.Errorf("stderr = %q", errw)
	}
}

func TestJoinApplyFromServer_WritesTheSameFileAsTheFilesDo(t *testing.T) {
	s := newJoinServer(t)
	f := newFakeJoinFiles()
	var out, errw bytes.Buffer
	if err := runJoinApplyFromServer(&out, &errw, s, f, "dns", join.ApplyOptions{}); err != nil {
		t.Fatalf("%v\n%s", err, errw.String())
	}
	if !strings.Contains(out.String(), "created, and read back") {
		t.Errorf("output:\n%s", out.String())
	}
	fromFiles := newFakeJoinFiles()
	if _, _, err := runApply(t, fromFiles, "dns", join.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if f.files["tink/dns/hosts"] != fromFiles.files["tink/dns/hosts"] || f.files["tink/dns/hosts"] == "" {
		t.Errorf("from the server %q, from the files %q", f.files["tink/dns/hosts"], fromFiles.files["tink/dns/hosts"])
	}
	// a second run changes nothing
	out.Reset()
	if err := runJoinApplyFromServer(&out, &errw, s, f, "dns", join.ApplyOptions{}); err != nil || !strings.Contains(out.String(), "no changes") {
		t.Errorf("second run: %v\n%s", err, out.String())
	}
}

func TestJoinApplyFromServer_ABadStampOrSectionWritesNothing(t *testing.T) {
	s := newJoinServer(t)
	f := newFakeJoinFiles()
	var out, errw bytes.Buffer
	if err := runJoinApplyFromServer(&out, &errw, s, f, "caddy", join.ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "only dns is built") {
		t.Errorf("caddy: %v", err)
	}
	s.instances[0].Config = map[string]string{"user.tink.publish.x": "nope"}
	if err := runJoinApplyFromServer(&out, &errw, s, f, "dns", join.ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "nothing was rendered") {
		t.Errorf("bad stamp: %v", err)
	}
	if len(f.writes) != 0 {
		t.Errorf("wrote %v", f.writes)
	}
}

// The two sources are alternatives, and the guards fire before any connection is made.
func TestJoinFlags_FilesAndFromServerAreAlternatives(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"join", "--from-server", joinCanary}, "takes no FILE"},
		{[]string{"join"}, "at least one FILE, or --from-server"},
		{[]string{"join", "--socket", "/x.sock", joinCanary}, "--socket is for --from-server"},
		{[]string{"join", "apply", "dns", "--from-server", joinCanary}, "takes no FILE"},
		{[]string{"join", "apply", "dns"}, "at least one FILE, or --from-server"},
		{[]string{"join", "apply", "routes", "--from-server"}, "is not a section"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			root := newRootCmd()
			var out, errw bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errw)
			root.SetArgs(c.args)
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want it to say %q", err, c.want)
			}
		})
	}
}
