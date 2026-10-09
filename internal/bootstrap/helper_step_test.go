package bootstrap

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/helper"
)

type instancesFake struct {
	incus.InstanceServer
	all []api.Instance
}

func (f *instancesFake) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return f.all, nil
}

func helperInst(ingress string, ingressOK bool) api.Instance {
	st := helper.Status{Proto: 1, JobProto: 1, PolicyProto: 1, Tick: time.Now(), HeartbeatSeconds: 600}
	if ingress != "" {
		st.Ingress = &helper.IngressState{OK: ingressOK, At: time.Now()}
	}
	text, _ := helper.Encode(st)
	cfg := map[string]string{helper.MarkerKey: "1", helper.StatusKey: text}
	if ingress != "" {
		cfg[helper.MarkerKey+".ingress"] = ingress
	}
	return api.Instance{Name: "helper", Project: "tink-helper", Status: "Running", InstancePut: api.InstancePut{Config: cfg}}
}

// rig points the step at a temporary host: unit paths in a temp dir, a fake server, and a runner that records commands.
type rig struct {
	t         *testing.T
	server    *instancesFake
	unit      string
	cmds      []string
	r         *runner
	installed []helper.InstallOptions
}

func newRig(t *testing.T, unit bool, insts ...api.Instance) *rig {
	t.Helper()
	dir := t.TempDir()
	g := &rig{t: t, server: &instancesFake{all: insts}, unit: filepath.Join(dir, "tink-daemon.service")}
	if unit {
		os.WriteFile(g.unit, []byte("[Unit]\n"), 0o644)
	}
	g.r = &runner{exec: func(name string, args ...string) ([]byte, error) {
		g.cmds = append(g.cmds, name+" "+strings.Join(args, " "))
		return nil, nil
	}}
	oldSD, oldRC, oldConn, oldInst, oldWait, oldSleep := systemdUnitPath, openrcUnitPath, connectIncus, installHelper, ingressWait, ingressSleep
	systemdUnitPath, openrcUnitPath = g.unit, filepath.Join(dir, "no-openrc")
	connectIncus = func(string) (incus.InstanceServer, error) { return g.server, nil }
	ingressWait, ingressSleep = 20*time.Millisecond, func(time.Duration) { time.Sleep(25 * time.Millisecond) }
	installHelper = func(s incus.InstanceServer, out io.Writer, o helper.InstallOptions) error {
		g.installed = append(g.installed, o)
		io.WriteString(out, "creating the helper\nstarted")
		g.server.all = append(g.server.all, helperInst("ingress", true))
		return nil
	}
	t.Cleanup(func() {
		systemdUnitPath, openrcUnitPath, connectIncus, installHelper, ingressWait, ingressSleep = oldSD, oldRC, oldConn, oldInst, oldWait, oldSleep
	})
	return g
}

func (g *rig) unitExists() bool { _, err := os.Stat(g.unit); return err == nil }

func (g *rig) notes() string { return strings.Join(g.r.actions, "\n") }

func TestDeployInstallsTheHelperWithIngressAndRetiresTheOldUnit(t *testing.T) {
	g := newRig(t, true)
	err := applyHelper(g.r, Options{Helper: HelperSource{Binary: "/usr/local/bin/tink", TZ: "America/Denver"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(g.installed) != 1 || !g.installed[0].Ingress || g.installed[0].Binary != "/usr/local/bin/tink" || g.installed[0].TZ != "America/Denver" {
		t.Errorf("%+v", g.installed)
	}
	if g.unitExists() {
		t.Error("the old unit must be removed once the helper reconciles ingress")
	}
	if got := strings.Join(g.cmds, "\n"); !strings.Contains(got, "systemctl disable --now tink-daemon") || !strings.Contains(got, "systemctl daemon-reload") {
		t.Errorf("%s", got)
	}
	if !strings.Contains(g.notes(), "helper: creating the helper") || !strings.Contains(g.notes(), "helper: started") {
		t.Errorf("what the installer says belongs in the deploy log:\n%s", g.notes())
	}
}

func TestAHostWithNoOldUnitJustGetsTheHelper(t *testing.T) {
	g := newRig(t, false)
	if err := applyHelper(g.r, Options{Helper: HelperSource{Image: "ghcr.io/minihci/tink-helper:v1.0.0"}}); err != nil {
		t.Fatal(err)
	}
	if len(g.installed) != 1 || g.installed[0].Image != "ghcr.io/minihci/tink-helper:v1.0.0" || len(g.cmds) != 0 {
		t.Errorf("%+v %v", g.installed, g.cmds)
	}
}

func TestAnExistingHelperThatRunsIngressIsNotInstalledAgain(t *testing.T) {
	g := newRig(t, true, helperInst("ingress", true))
	if err := applyHelper(g.r, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(g.installed) != 0 || g.unitExists() || !strings.Contains(g.notes(), "runs the ingress reconcile") {
		t.Errorf("%+v unit=%v\n%s", g.installed, g.unitExists(), g.notes())
	}
}

func TestTheOldUnitStaysUntilTheHelperHasReconciledIngress(t *testing.T) {
	g := newRig(t, true, helperInst("ingress", false))
	if err := applyHelper(g.r, Options{}); err != nil {
		t.Fatal(err)
	}
	if !g.unitExists() || len(g.cmds) != 0 {
		t.Errorf("a host must never be left without a reconciler: unit=%v cmds=%v", g.unitExists(), g.cmds)
	}
	if !strings.Contains(g.notes(), "WARN") || !strings.Contains(g.notes(), "systemctl disable --now tink-daemon") {
		t.Errorf("say what to do:\n%s", g.notes())
	}
}

func TestAHelperWithoutIngressIsNotReplacedAndTheUnitStays(t *testing.T) {
	g := newRig(t, true, helperInst("", false))
	if err := applyHelper(g.r, Options{}); err != nil {
		t.Fatal(err)
	}
	if len(g.installed) != 0 || !g.unitExists() || len(g.cmds) != 0 {
		t.Errorf("%+v unit=%v %v", g.installed, g.unitExists(), g.cmds)
	}
	if !strings.Contains(g.notes(), "tink helper remove") {
		t.Errorf("%s", g.notes())
	}
}

func TestAFailedInstallFailsTheDeployAndKeepsTheUnit(t *testing.T) {
	g := newRig(t, true)
	installHelper = func(incus.InstanceServer, io.Writer, helper.InstallOptions) error { return errors.New("no network") }
	err := applyHelper(g.r, Options{})
	if err == nil || !strings.Contains(err.Error(), "no network") || !g.unitExists() || len(g.cmds) != 0 {
		t.Errorf("%v unit=%v %v", err, g.unitExists(), g.cmds)
	}
}

func TestADryRunLooksAndSaysWhatItWouldReallyDo(t *testing.T) {
	// no helper, an old unit: it would install, then retire
	g := newRig(t, true)
	g.r.dryRun = true
	if err := applyHelper(g.r, Options{Helper: HelperSource{Binary: "/usr/local/bin/tink"}}); err != nil {
		t.Fatal(err)
	}
	if len(g.installed) != 0 || !g.unitExists() || len(g.cmds) != 0 {
		t.Errorf("a dry run changes nothing: %+v unit=%v %v", g.installed, g.unitExists(), g.cmds)
	}
	for _, want := range []string{"would install the helper", "/usr/local/bin/tink", "would then retire the host's tink-daemon unit"} {
		if !strings.Contains(g.notes(), want) {
			t.Errorf("missing %q:\n%s", want, g.notes())
		}
	}

	// a helper with ingress that has reconciled: it would retire the unit
	g = newRig(t, true, helperInst("ingress", true))
	g.r.dryRun = true
	applyHelper(g.r, Options{})
	if !strings.Contains(g.notes(), "would retire the host's tink-daemon unit") || !g.unitExists() || len(g.cmds) != 0 {
		t.Errorf("%s", g.notes())
	}

	// one that has not: it would leave the unit
	g = newRig(t, true, helperInst("ingress", false))
	g.r.dryRun = true
	applyHelper(g.r, Options{})
	if !strings.Contains(g.notes(), "would leave the host's tink-daemon unit running") {
		t.Errorf("%s", g.notes())
	}

	// a helper without ingress, as on a hand-built host: the same warning as a real run
	g = newRig(t, false, helperInst("", false))
	g.r.dryRun = true
	applyHelper(g.r, Options{})
	if !strings.Contains(g.notes(), "does not run the ingress reconcile") || len(g.installed) != 0 {
		t.Errorf("%s", g.notes())
	}

	// a server that cannot be reached still gets a plan, not an error
	g = newRig(t, false)
	g.r.dryRun = true
	connectIncus = func(string) (incus.InstanceServer, error) { return nil, errors.New("no socket") }
	if err := applyHelper(g.r, Options{}); err != nil || !strings.Contains(g.notes(), "could not look: no socket") {
		t.Errorf("%v\n%s", err, g.notes())
	}
}
