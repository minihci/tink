package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/helper"
)

var helperNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

type instancesServer struct {
	incus.InstanceServer
	all     []api.Instance
	err     error
	certs   []api.Certificate
	certErr error
}

func (s instancesServer) GetCertificates() ([]api.Certificate, error) { return s.certs, s.certErr }

func (s instancesServer) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return s.all, s.err
}

func helperInstance(project, name, state string, st *helper.Status) api.Instance {
	cfg := map[string]string{helper.MarkerKey: "1"}
	if st != nil {
		text, _ := helper.Encode(*st)
		cfg[helper.StatusKey] = text
	}
	return api.Instance{Name: name, Project: project, Status: state, InstancePut: api.InstancePut{Config: cfg}}
}

func goodStatus(mut func(*helper.Status)) *helper.Status {
	s := &helper.Status{Proto: 1, Version: "v1", JobProto: 1, PolicyProto: 1, TZ: "UTC", Started: helperNow.Add(-3 * time.Hour),
		Tick: helperNow.Add(-2 * time.Minute), HeartbeatSeconds: 600}
	if mut != nil {
		mut(s)
	}
	return s
}

func statusRun(t *testing.T, srv incus.InstanceServer, instance, project string, check, asJSON bool) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runHelperStatus(&out, srv, instance, project, check, asJSON, helperNow)
	return out.String(), err
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	var ec *exitCodeError
	if err == nil {
		return 0
	}
	if !errors.As(err, &ec) {
		t.Fatalf("not an exit-code error: %v", err)
	}
	return ec.code
}

func TestStatusCheckSpeaksInExitCodesAMonitorCanUse(t *testing.T) {
	skipped := goodStatus(func(s *helper.Status) { s.Skipped = []helper.Skip{{Volume: "lib", Reason: "policy of protocol 2"}} })
	stale := goodStatus(func(s *helper.Status) { s.Tick = helperNow.Add(-40 * time.Minute) })
	for _, tc := range []struct {
		name string
		inst api.Instance
		code int
		line string
	}{
		{"healthy", helperInstance("tink-helper", "helper", "Running", goodStatus(nil)), 0, "healthy: tink-helper/helper"},
		{"degraded: a volume is skipped", helperInstance("tink-helper", "helper", "Running", skipped), 1, "degraded: tink-helper/helper: 1 volume(s) skipped: lib (policy of protocol 2)"},
		{"down: stale", helperInstance("tink-helper", "helper", "Running", stale), 2, "down: tink-helper/helper: stale: last heard from 40m ago"},
		{"down: stopped", helperInstance("tink-helper", "helper", "Stopped", goodStatus(nil)), 2, "down: tink-helper/helper: the instance is stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := statusRun(t, instancesServer{all: []api.Instance{tc.inst}}, "", "", true, false)
			if got := exitCode(t, err); got != tc.code {
				t.Errorf("exit code = %d, want %d", got, tc.code)
			}
			if strings.TrimSpace(out) == "" || !strings.HasPrefix(strings.TrimSpace(out), tc.line) {
				t.Errorf("output %q, want it to start with %q", out, tc.line)
			}
			if strings.Count(strings.TrimSpace(out), "\n") != 0 {
				t.Errorf("--check prints one line per helper: %q", out)
			}
			if err != nil {
				var ec *exitCodeError
				errors.As(err, &ec)
				if !ec.silent {
					t.Error("the check has said what it had to say: the exit-code error must not print it again")
				}
			}
		})
	}
}

func TestNoHelperIsDownUnderCheckAndAnErrorWithout(t *testing.T) {
	srv := instancesServer{all: []api.Instance{{Name: "caddy", Project: "default", Status: "Running"}}}
	out, err := statusRun(t, srv, "", "", true, false)
	if exitCode(t, err) != 2 || !strings.HasPrefix(out, "down: no helper instance found") {
		t.Errorf("%q %v", out, err)
	}
	out, err = statusRun(t, srv, "", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "no helper instance found") || out != "" {
		t.Errorf("without --check there is nothing to show, so it is an error: %q %v", out, err)
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		t.Error("and an ordinary error, printed once, not an exit-code one")
	}
}

func TestStatusFiltersAndTheWorstHelperDecidesTheCode(t *testing.T) {
	srv := instancesServer{all: []api.Instance{
		helperInstance("a", "one", "Running", goodStatus(nil)),
		helperInstance("b", "two", "Stopped", goodStatus(nil)),
	}}
	out, err := statusRun(t, srv, "", "", true, false)
	if exitCode(t, err) != 2 || strings.Count(strings.TrimSpace(out), "\n") != 1 {
		t.Errorf("two helpers, one line each, and the worst decides the code: %q %v", out, err)
	}
	if out, err := statusRun(t, srv, "one", "", true, false); err != nil || !strings.HasPrefix(out, "healthy: a/one") {
		t.Errorf("--instance narrows it: %q %v", out, err)
	}
	if out, err := statusRun(t, srv, "", "b", true, false); exitCode(t, err) != 2 || !strings.Contains(out, "b/two") || strings.Contains(out, "a/one") {
		t.Errorf("--project narrows it: %q %v", out, err)
	}
	out, err = statusRun(t, srv, "nope", "", true, false)
	if exitCode(t, err) != 2 || !strings.Contains(out, "matching the --instance and --project given") {
		t.Errorf("%q %v", out, err)
	}
}

func TestStatusWithoutCheckPrintsDetailsAndExitsZero(t *testing.T) {
	st := goodStatus(func(s *helper.Status) {
		s.Failing = []helper.Failing{{Volume: "lib", Target: "nas", Count: 3, Since: helperNow.Add(-time.Hour)}}
		s.LastJob = &helper.LastJob{ID: "20261007T110000Z-abc", State: "failed", Finished: helperNow.Add(-5 * time.Minute)}
		s.Ingress = &helper.IngressState{OK: true, At: helperNow.Add(-time.Minute)}
	})
	out, err := statusRun(t, instancesServer{all: []api.Instance{helperInstance("tink-helper", "helper", "Running", st)}}, "", "", false, false)
	if err != nil {
		t.Fatalf("a degraded helper is not an error without --check: %v", err)
	}
	for _, want := range []string{
		"helper tink-helper/helper: degraded", "1 copy(ies) failing", "version:     v1", "copy policies up to protocol 1",
		"last heard:  2m ago (it writes at least every 10m)", "skipped:     none", "lib -> nas, 3 in a row",
		"20261007T110000Z-abc failed, 5m ago", "ingress:     ok, 1m ago",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the details must contain %q:\n%s", want, out)
		}
	}
}

func TestStatusJSONCarriesTheJudgementAndTheDocument(t *testing.T) {
	st := goodStatus(func(s *helper.Status) { s.Skipped = []helper.Skip{{Volume: "lib", Reason: "x"}} })
	out, err := statusRun(t, instancesServer{all: []api.Instance{helperInstance("p", "h", "Running", st)}}, "", "", true, true)
	if exitCode(t, err) != 1 {
		t.Errorf("--json with --check still sets the exit code: %v", err)
	}
	var got []helperJSON
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 1 {
		t.Fatalf("%v\n%s", err, out)
	}
	if got[0].Health != "degraded" || got[0].Code != 1 || got[0].Project != "p" || got[0].Status == nil || got[0].Status.Skipped[0].Volume != "lib" {
		t.Errorf("%+v", got[0])
	}
}

func TestAFailedListingIsAnErrorNotNoHelper(t *testing.T) {
	_, err := statusRun(t, instancesServer{err: errors.New("connection refused")}, "", "", true, false)
	var ec *exitCodeError
	if err == nil || errors.As(err, &ec) || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("if it cannot look it must not say 'down' as though it had looked: %v", err)
	}
}

func TestExecutePrintsAnErrorOnceAndAnExitCodeErrorNotAtAll(t *testing.T) {
	run := func(err error) (stdout, stderr string, got error) {
		cmd := &cobra.Command{Use: "x", SilenceErrors: true, SilenceUsage: true, RunE: func(*cobra.Command, []string) error { return err }}
		var out, errb bytes.Buffer
		got = execute(cmd, &out, &errb)
		return out.String(), errb.String(), got
	}
	if _, stderr, _ := run(&exitCodeError{code: 2, silent: true}); stderr != "" {
		t.Errorf("a silent exit-code error prints nothing: %q", stderr)
	}
	if _, stderr, _ := run(&exitCodeError{code: 3, msg: "spoken", silent: false}); !strings.Contains(stderr, "spoken") {
		t.Errorf("a non-silent one is printed: %q", stderr)
	}
	if _, stderr, _ := run(errors.New("plain")); strings.Count(stderr, "plain") != 1 {
		t.Errorf("an ordinary error is printed once: %q", stderr)
	}
}

func TestInstallNeedsAnImageOrABinaryAndSaysSoBeforeConnectingAnywhere(t *testing.T) {
	err := runRoot(t, "helper", "install", "--socket", "/nonexistent/incus.sock")
	if err == nil || !strings.Contains(err.Error(), "--image") || !strings.Contains(err.Error(), "--binary") {
		t.Errorf("err = %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "connecting to incus") {
		t.Errorf("a missing option is reported before any connection is tried: %v", err)
	}
}

func TestTheHelperCommandsAreListed(t *testing.T) {
	var out bytes.Buffer
	root := newRootCmd()
	root.SetArgs([]string{"helper", "--help"})
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"install", "upgrade", "remove", "status"} {
		if !strings.Contains(out.String(), "  "+c+" ") {
			t.Errorf("helper %s is not listed:\n%s", c, out.String())
		}
	}
}

func TestStatusNoticesARevokedCertificateAtOnceButOnlyIfItCanReadTheTrustStore(t *testing.T) {
	inst := helperInstance("tink-helper", "helper", "Running", goodStatus(nil))
	inst.Config[helper.MarkerKey+".remote"] = "https://127.0.0.1:8443"
	trusted := api.Certificate{CertificatePut: api.CertificatePut{Name: helper.TrustName, Type: "client"}}

	out, err := statusRun(t, instancesServer{all: []api.Instance{inst}, certs: []api.Certificate{trusted}}, "", "", true, false)
	if err != nil || !strings.HasPrefix(out, "healthy:") {
		t.Errorf("trusted: %q %v", out, err)
	}
	out, err = statusRun(t, instancesServer{all: []api.Instance{inst}}, "", "", true, false)
	if exitCode(t, err) != 2 || !strings.Contains(out, "not in the host's trust store") {
		t.Errorf("revoked, though its last document is fresh: %q %v", out, err)
	}
	out, err = statusRun(t, instancesServer{all: []api.Instance{inst}, certErr: errors.New("not authorized")}, "", "", true, false)
	if err != nil || !strings.HasPrefix(out, "healthy:") {
		t.Errorf("a client that cannot read the trust store is not told, and the rest of the judgement stands: %q %v", out, err)
	}
}

func TestAReleaseBuildNamesItsOwnVersionAndADevelopmentBuildHasNoImage(t *testing.T) {
	old := injectedVersion
	t.Cleanup(func() { injectedVersion = old })

	injectedVersion = "v1.2.3"
	if got := buildVersion(); !strings.Contains(got, "tink v1.2.3 ") {
		t.Errorf("the version compiled in by the release workflow is what the binary reports: %q", got)
	}
	if got := helper.ReleaseImage(injectedVersion); got != "ghcr:minihci/tink-helper:v1.2.3" {
		t.Errorf("and names the image published for it: %q", got)
	}

	injectedVersion = ""
	err := runRoot(t, "helper", "install", "--socket", "/nonexistent/incus.sock")
	if err == nil || !strings.Contains(err.Error(), "not a release build") || !strings.Contains(err.Error(), "--image") {
		t.Errorf("a development build has no image that matches it, and says what to give instead: %v", err)
	}
}

func TestUpgradeNeedsAnImageOrABinaryOnADevelopmentBuild(t *testing.T) {
	old := injectedVersion
	t.Cleanup(func() { injectedVersion = old })
	injectedVersion = ""
	err := runRoot(t, "helper", "upgrade", "--socket", "/nonexistent/incus.sock")
	if err == nil || !strings.Contains(err.Error(), "not a release build") || strings.Contains(err.Error(), "connecting to incus") {
		t.Errorf("said before any connection is tried: %v", err)
	}
}
