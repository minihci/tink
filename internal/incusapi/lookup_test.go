package incusapi

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// answerServer answers every Get with err, or with an object that exists when err is nil. Any other method panics (nil embedded
// interface), so a lookup that wanders off to do something else fails loudly.
type answerServer struct {
	incus.InstanceServer
	err error
}

func (s answerServer) GetInstance(n string) (*api.Instance, string, error) {
	return &api.Instance{Name: n}, "etag-i", s.err
}
func (s answerServer) GetProfile(n string) (*api.Profile, string, error) {
	return &api.Profile{Name: n}, "etag-p", s.err
}
func (s answerServer) GetProject(n string) (*api.Project, string, error) {
	return &api.Project{Name: n}, "etag-j", s.err
}
func (s answerServer) GetStoragePoolVolume(_, _, n string) (*api.StorageVolume, string, error) {
	return &api.StorageVolume{Name: n}, "etag-v", s.err
}
func (s answerServer) GetImageAlias(n string) (*api.ImageAliasesEntry, string, error) {
	return &api.ImageAliasesEntry{Name: n}, "", s.err
}
func (s answerServer) GetImage(f string) (*api.Image, string, error) {
	return &api.Image{Fingerprint: f}, "", s.err
}
func (s answerServer) GetInstanceFile(string, string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return io.NopCloser(strings.NewReader("x")), &incus.InstanceFileResponse{}, nil
}

// result is what a lookup said, reduced to the three things a caller acts on.
type result struct {
	found bool
	err   error
}

func lookups(s answerServer) map[string]result {
	r := map[string]result{}
	_, _, f, err := LookupInstance(s, "x")
	r["instance"] = result{f, err}
	_, _, f, err = LookupProfile(s, "x")
	r["profile"] = result{f, err}
	_, _, f, err = LookupProject(s, "x")
	r["project"] = result{f, err}
	_, _, f, err = LookupVolume(s, "p", "custom", "x")
	r["volume"] = result{f, err}
	_, _, f, err = LookupImageAlias(s, "x")
	r["image alias"] = result{f, err}
	_, _, f, err = LookupImage(s, "x")
	r["image"] = result{f, err}
	rc, _, f, err := LookupInstanceFile(s, "i", "/p")
	if rc != nil {
		rc.Close()
	}
	r["instance file"] = result{f, err}
	return r
}

func TestEveryLookupDistinguishesAbsentFromFailed(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantFound bool
		wantErr   bool
	}{
		{"it exists", nil, true, false},
		{"a 404 is absent, and is not an error", api.StatusErrorf(http.StatusNotFound, "not found"), false, false},
		{"a 403 (a revoked or restricted client) is a failure", api.StatusErrorf(http.StatusForbidden, "not authorized"), false, true},
		{"a 500 is a failure", api.StatusErrorf(http.StatusInternalServerError, "boom"), false, true},
		{"a 503 is a failure", api.StatusErrorf(http.StatusServiceUnavailable, "busy"), false, true},
		{"a dropped connection is a failure", errors.New("read tcp: connection reset by peer"), false, true},
		{"a wrapped 404 is still absent", fmt.Errorf("asking: %w", api.StatusErrorf(http.StatusNotFound, "gone")), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for kind, got := range lookups(answerServer{err: tc.err}) {
				if got.found != tc.wantFound || (got.err != nil) != tc.wantErr {
					t.Errorf("%s: found=%v err=%v, want found=%v err=%v", kind, got.found, got.err, tc.wantFound, tc.wantErr)
				}
				if tc.wantErr && !errors.Is(got.err, tc.err) {
					t.Errorf("%s: the original error must come back, got %v", kind, got.err)
				}
			}
		})
	}
}

func TestAFoundLookupReturnsTheObjectAndItsEtag(t *testing.T) {
	inst, etag, found, err := LookupInstance(answerServer{}, "web")
	if err != nil || !found || inst.Name != "web" || etag != "etag-i" {
		t.Errorf("%+v %q %v %v", inst, etag, found, err)
	}
	vol, etag, found, err := LookupVolume(answerServer{}, "p", "custom", "lib")
	if err != nil || !found || vol.Name != "lib" || etag != "etag-v" {
		t.Errorf("%+v %q %v %v", vol, etag, found, err)
	}
	// an absent object carries no object and no etag, so nothing can be used by mistake
	inst, etag, found, err = LookupInstance(answerServer{err: api.StatusErrorf(http.StatusNotFound, "x")}, "web")
	if inst != nil || etag != "" || found || err != nil {
		t.Errorf("%+v %q %v %v", inst, etag, found, err)
	}
}

func TestIsNotFound(t *testing.T) {
	notFound := api.StatusErrorf(http.StatusNotFound, "not found")
	forbidden := api.StatusErrorf(http.StatusForbidden, "not authorized")
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"nil":                       {nil, false},
		"404":                       {notFound, true},
		"a wrapped 404":             {fmt.Errorf("reading the live profile: %w", notFound), true},
		"a 404 joined with another": {errors.Join(errors.New("x"), notFound), true},
		"403":                       {forbidden, false},
		"500":                       {api.StatusErrorf(http.StatusInternalServerError, "boom"), false},
		"a wrapped non-404 status":  {fmt.Errorf("x: %w", forbidden), false},
		"a dropped connection":      {errors.New("read tcp: connection reset by peer"), false},
		"text that says not found":  {errors.New("Image not found"), false},
		"an unparsable 404 reply":   {errors.New("Failed to fetch https://tron:8443/1.0/profiles/web: 404 Not Found"), false},
	} {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Errorf("%s: IsNotFound(%v) = %v, want %v", name, tc.err, got, tc.want)
		}
	}
}
