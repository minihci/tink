package incusapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

type patchServer struct {
	incus.InstanceServer
	method, path string
	body         any
	etag         string
	err          error
}

func (s *patchServer) RawQuery(method, path string, data any, etag string) (*api.Response, string, error) {
	s.method, s.path, s.body, s.etag = method, path, data, etag
	return &api.Response{Type: "sync", StatusCode: 200}, "", s.err
}

func TestPatchInstanceConfigSendsOnlyTheKeysItNames(t *testing.T) {
	s := &patchServer{}
	if err := PatchInstanceConfig(s, "tink-helper", "helper", map[string]string{"user.tink.helper.status": `{"proto":1}`}); err != nil {
		t.Fatal(err)
	}
	if s.method != "PATCH" || s.path != "/1.0/instances/helper?project=tink-helper" || s.etag != "" {
		t.Errorf("%s %s etag=%q: a PATCH of the one instance, scoped to its project, and no etag to conflict on", s.method, s.path, s.etag)
	}
	b, _ := json.Marshal(s.body)
	if string(b) != `{"config":{"user.tink.helper.status":"{\"proto\":1}"}}` {
		t.Errorf("body = %s: only the config keys, nothing else to overwrite", b)
	}
}

func TestPatchInstanceConfigEscapesNamesAndAnEmptyProjectIsOmitted(t *testing.T) {
	s := &patchServer{}
	_ = PatchInstanceConfig(s, "", "we ird/name", map[string]string{"user.a": "b"})
	if s.path != "/1.0/instances/we%20ird%2Fname" {
		t.Errorf("path = %q", s.path)
	}
}

func TestPatchInstanceConfigReturnsAFailureWithTheInstanceNamed(t *testing.T) {
	s := &patchServer{err: api.StatusErrorf(http.StatusForbidden, "not authorized")}
	err := PatchInstanceConfig(s, "p", "helper", map[string]string{"user.a": "b"})
	if err == nil || !errors.Is(err, s.err) {
		t.Fatalf("err = %v: the original error must come back", err)
	}
}
