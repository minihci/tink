package resolve

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// lookupServer answers every Get a planner makes with err or, when err is nil, with an object that
// exists. Methods a planner has no business calling panic (nil embedded interface), and the one write
// the file tests care about is counted, so a test can prove that a failed read never became a write.
type lookupServer struct {
	incus.InstanceServer
	err         error
	fileContent string
	aliasTarget string
	pushed      int
}

func (s *lookupServer) UseProject(string) incus.InstanceServer { return s }

func (s *lookupServer) GetProject(name string) (*api.Project, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &api.Project{Name: name}, "", nil
}

func (s *lookupServer) GetProfile(name string) (*api.Profile, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &api.Profile{Name: name}, "", nil
}

func (s *lookupServer) GetStoragePoolVolume(_, _, name string) (*api.StorageVolume, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &api.StorageVolume{Name: name}, "", nil
}

func (s *lookupServer) GetInstance(name string) (*api.Instance, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &api.Instance{Name: name, Type: "container"}, "", nil
}

func (s *lookupServer) GetInstanceFile(string, string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return io.NopCloser(strings.NewReader(s.fileContent)), &incus.InstanceFileResponse{}, nil
}

func (s *lookupServer) GetImageAlias(name string) (*api.ImageAliasesEntry, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &api.ImageAliasesEntry{Name: name, ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: s.aliasTarget}}, "", nil
}

func (s *lookupServer) CreateInstanceFile(string, string, incus.InstanceFileArgs) error {
	s.pushed++
	return nil
}

var (
	errNotFound  = api.StatusErrorf(http.StatusNotFound, "not found")
	errForbidden = api.StatusErrorf(http.StatusForbidden, "not authorized")
	errServer    = api.StatusErrorf(http.StatusInternalServerError, "database is locked")
	errDropped   = fmt.Errorf("Get \"https://tron:8443/1.0\": %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")})
)

// notAbsent are the failures that say nothing about whether the object exists.
var notAbsent = []struct {
	name string
	err  error
}{
	{"403 (revoked or restricted client)", errForbidden},
	{"500", errServer},
	{"dropped connection", errDropped},
}

// lookupResources is one resource of every kind whose planner reads the live object first.
var lookupResources = []Resource{
	{Kind: KindProject, Name: "web"},
	{Kind: KindProfile, Name: "web"},
	{Kind: KindStorageVolume, Name: "data", Pool: "default"},
	{Kind: KindInstance, Name: "app"},
	{Kind: KindFile, Name: "conf", Instance: "app", Path: "/etc/app.conf", Content: "x", Restart: true},
	{Kind: KindImage, Name: "base", Alias: "base"},
	{Kind: KindExec, Name: "init", Instance: "app", Command: []string{"true"}},
}

func TestIsNotFound(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                       {nil, false},
		"404":                       {errNotFound, true},
		"a wrapped 404":             {fmt.Errorf("reading the live profile: %w", errNotFound), true},
		"403":                       {errForbidden, false},
		"500":                       {errServer, false},
		"a dropped connection":      {errDropped, false},
		"text that says not found":  {errors.New("Image not found"), false},
		"an unparsable 404 reply":   {errors.New("Failed to fetch https://tron:8443/1.0/profiles/web: 404 Not Found"), false},
		"a wrapped non-404 status":  {fmt.Errorf("x: %w", errServer), false},
		"a 404 joined with another": {errors.Join(errors.New("x"), errNotFound), true},
	} {
		if got := isNotFound(tc.err); got != tc.want {
			t.Errorf("%s: isNotFound(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}

// A real not-found keeps planning a create, for every kind.
func TestPlanNotFoundPlansACreate(t *testing.T) {
	for _, r := range lookupResources {
		t.Run(string(r.Kind), func(t *testing.T) {
			p, err := planOne(&lookupServer{err: errNotFound}, r, PlanOptions{})
			if err != nil {
				t.Fatalf("a 404 is the object being absent, not an error: %v", err)
			}
			if p.Action != ActionCreate {
				t.Errorf("action = %v, want ActionCreate", p.Action)
			}
		})
	}
}

// Anything else is returned, not read as absence.
func TestPlanOtherLookupErrorsStopThePlan(t *testing.T) {
	for _, r := range lookupResources {
		for _, tc := range notAbsent {
			t.Run(string(r.Kind)+"/"+tc.name, func(t *testing.T) {
				p, err := planOne(&lookupServer{err: tc.err}, r, PlanOptions{})
				if err == nil {
					t.Fatalf("planned %v instead of returning the error", p.Action)
				}
				if !errors.Is(err, tc.err) {
					t.Errorf("error %q does not wrap the server's %q", err, tc.err)
				}
				if p.Action == ActionCreate {
					t.Errorf("planned a create for an object that was never shown to be absent")
				}
			})
		}
	}
}

// What exists is still planned as before: the lookup helper must not turn "found" into anything else.
func TestPlanExistingResourcesAreUntouched(t *testing.T) {
	for _, r := range lookupResources {
		if r.Kind == KindExec {
			continue // converging an exec runs a command in the guest
		}
		t.Run(string(r.Kind), func(t *testing.T) {
			p, err := planOne(&lookupServer{fileContent: r.Content, aliasTarget: "abc"}, r, NewPlanOptions(true))
			if err != nil {
				t.Fatal(err)
			}
			if p.Action != ActionNone {
				t.Errorf("action = %v (changes %v), want ActionNone", p.Action, p.Changes)
			}
		})
	}
}

// The path `tink plan` and `tink plan apply` take: one failing lookup fails the whole plan, naming the resource.
func TestPlanWithOptionsReturnsTheLookupError(t *testing.T) {
	for _, tc := range notAbsent {
		t.Run(tc.name, func(t *testing.T) {
			plans, err := Plan(&lookupServer{err: tc.err}, lookupResources)
			if err == nil {
				t.Fatalf("Plan returned %d plans and no error", len(plans))
			}
			if plans != nil {
				t.Errorf("Plan returned plans alongside the error: %v", plans)
			}
			if !errors.Is(err, tc.err) || !strings.HasPrefix(err.Error(), "project/web: ") {
				t.Errorf("error = %q, want it to name project/web and wrap %q", err, tc.err)
			}
		})
	}
}

// The case that made this matter: a file read that merely failed must not be pushed over (and its
// instance restarted, restart: true being set on the resource).
func TestApplyDoesNotPushAFileItCouldNotRead(t *testing.T) {
	file := Resource{Kind: KindFile, Name: "conf", Instance: "app", Path: "/etc/app.conf", Content: "x", Restart: true}
	note := func(string, ...any) {}

	for _, tc := range notAbsent {
		t.Run(tc.name, func(t *testing.T) {
			s := &lookupServer{err: tc.err}
			if _, err := applyOne(s, file, PlanOptions{}, note); err == nil {
				t.Fatal("applyOne succeeded on a file it could not read")
			}
			if s.pushed != 0 {
				t.Errorf("pushed %d time(s), want 0", s.pushed)
			}
		})
	}

	t.Run("404 still creates", func(t *testing.T) {
		s := &lookupServer{err: errNotFound}
		file := file
		file.Restart = false // restarting needs a live instance; the push is what this proves
		if _, err := applyOne(s, file, PlanOptions{}, note); err != nil {
			t.Fatal(err)
		}
		if s.pushed != 1 {
			t.Errorf("pushed %d time(s), want 1", s.pushed)
		}
	})
}

// execConverged is also what runExec trusts before running Command: a lookup that failed must not read as
// "not converged", or a command its Check/Triggers guard exists to keep from running twice runs again.
func TestRunExecDoesNotRunWhenTheInstanceLookupFails(t *testing.T) {
	r := Resource{Kind: KindExec, Name: "init", Instance: "app", Command: []string{"true"}, Check: []string{"test", "-f", "/done"}}
	for _, tc := range notAbsent {
		t.Run(tc.name, func(t *testing.T) {
			// Reaching ExecInstance would panic on the nil embedded interface.
			if err := runExec(&lookupServer{err: tc.err}, r); !errors.Is(err, tc.err) {
				t.Errorf("runExec error = %v, want it to wrap %q", err, tc.err)
			}
		})
	}
}

// A local alias lookup that failed is unverified, not "no drift"; a missing alias keeps having no opinion.
func TestCheckInstanceAliasLookup(t *testing.T) {
	cur := &api.Instance{Name: "app", InstancePut: api.InstancePut{Config: map[string]string{"volatile.base_image": "aaa111"}}}
	r := Resource{Kind: KindInstance, Name: "app", Image: "mine"}
	env := &imageEnv{offline: true}

	for name, tc := range map[string]struct {
		s          *lookupServer
		drift, unv bool
	}{
		"alias missing":    {&lookupServer{err: errNotFound}, false, false},
		"alias unchanged":  {&lookupServer{aliasTarget: "aaa111"}, false, false},
		"alias repointed":  {&lookupServer{aliasTarget: "bbb222"}, true, false},
		"403":              {&lookupServer{err: errForbidden}, false, true},
		"500":              {&lookupServer{err: errServer}, false, true},
		"dropped":          {&lookupServer{err: errDropped}, false, true},
		"wrapped not-find": {&lookupServer{err: fmt.Errorf("x: %w", errNotFound)}, false, false},
	} {
		got := env.checkInstance(tc.s, cur, r)
		if (len(got.Drift) > 0) != tc.drift || (len(got.Unverified) > 0) != tc.unv {
			t.Errorf("%s: drift=%v unverified=%v, want drift=%v unverified=%v", name, got.Drift, got.Unverified, tc.drift, tc.unv)
		}
	}
}
