package bootstrap

import (
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

var (
	notFound  = api.StatusErrorf(http.StatusNotFound, "not found")
	forbidden = api.StatusErrorf(http.StatusForbidden, "not authorized")
)

// lookupFake answers the two reads these steps make, and nothing else: a write (a stop, a delete, a create) panics through the nil
// embedded interface, so a test fails loudly if a step acts when it should have stopped.
type lookupFake struct {
	incus.InstanceServer
	err error // what a read answers; nil means the object exists
}

func (f lookupFake) GetInstance(n string) (*api.Instance, string, error) {
	return &api.Instance{Name: n, Status: "Stopped"}, "", f.err
}

func (f lookupFake) GetProfile(n string) (*api.Profile, string, error) {
	return &api.Profile{Name: n}, "etag", f.err
}

func TestAStepThatCannotTellWhetherTheInstanceExistsStopsInsteadOfLaunchingOverIt(t *testing.T) {
	r := &runner{}
	err := recreateInstance(r, lookupFake{err: forbidden}, "ingress", "some-image")
	if err == nil || !strings.Contains(err.Error(), "checking whether ingress already exists") {
		t.Errorf("err = %v", err)
	}
	if len(r.actions) != 0 {
		t.Errorf("nothing may be stopped, deleted or launched: %v", r.actions)
	}
}

func TestAStepThatCannotTellWhetherTheProfileExistsStopsInsteadOfCreatingIt(t *testing.T) {
	opts := Options{RepoRoot: "../../configs"}
	r := &runner{}
	err := applyOneProfile(r, lookupFake{err: forbidden}, profileSpecs[0], opts)
	if err == nil || !strings.Contains(err.Error(), "checking whether profile incus-ui exists") {
		t.Fatalf("err = %v", err)
	}
	if len(r.actions) != 0 {
		t.Errorf("a failed read is not 'does not exist': %v", r.actions)
	}

	// and a real 404 still plans a create, a present profile does not (dry run, so nothing is written)
	r = &runner{dryRun: true}
	if err := applyOneProfile(r, lookupFake{err: notFound}, profileSpecs[0], opts); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.actions, "|"); !strings.Contains(got, "would create profile incus-ui") {
		t.Errorf("a 404 is absent: %q", got)
	}
	r = &runner{dryRun: true}
	if err := applyOneProfile(r, lookupFake{}, profileSpecs[0], opts); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.actions, "|"); strings.Contains(got, "would create") {
		t.Errorf("a profile that exists is not created: %q", got)
	}
}
