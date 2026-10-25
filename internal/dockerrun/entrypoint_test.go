package dockerrun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func TestSplitRef(t *testing.T) {
	for _, c := range []struct {
		ref, repo, tag string
		hub            bool
	}{
		{"rhasspy/wyoming-piper", "rhasspy/wyoming-piper", "latest", true},
		{"rhasspy/wyoming-piper:1.2", "rhasspy/wyoming-piper", "1.2", true},
		{"alpine", "library/alpine", "latest", true},
		{"alpine:3", "library/alpine", "3", true},
		{"alpine", "alpine", "latest", false}, // another registry: no library/
		{"team/app@sha256:abc", "team/app", "sha256:abc", false},
		{"matter-js/matterjs-server:stable", "matter-js/matterjs-server", "stable", false},
	} {
		if repo, tag := splitRef(c.ref, c.hub); repo != c.repo || tag != c.tag {
			t.Errorf("splitRef(%q, %v) = %q, %q, want %q, %q", c.ref, c.hub, repo, tag, c.repo, c.tag)
		}
	}
}

func TestRegistryBaseAndArchitecture(t *testing.T) {
	for in, want := range map[string]string{
		"https://docker.io":                 "https://registry-1.docker.io",
		"https://ghcr.io":                   "https://ghcr.io",
		"http://127.0.0.1:5000/":            "http://127.0.0.1:5000",
		"https://registry.example.org/team": "https://registry.example.org",
	} {
		if got := registryBase(in); got != want {
			t.Errorf("registryBase(%q) = %q, want %q", in, got, want)
		}
	}
	if a, err := ociArch("x86_64"); a != "amd64" || err != nil {
		t.Errorf("x86_64 = %q, %v", a, err)
	}
	if a, err := ociArch("aarch64"); a != "arm64" || err != nil {
		t.Errorf("aarch64 = %q, %v", a, err)
	}
	if _, err := ociArch("armv7l"); err == nil {
		t.Error("an architecture with no OCI name here must be an error")
	}
}

func TestWithEntrypointPutsTheCommandAfterTheImagesOwn(t *testing.T) {
	entry := []string{"bash", "docker_run.sh"}
	if got := withEntrypoint(entry, []string{"--voice", "en_US-lessac-medium"}); got != "bash docker_run.sh --voice en_US-lessac-medium" {
		t.Errorf("got %q", got)
	}
	if len(entry) != 2 {
		t.Errorf("the image's entrypoint slice must not be changed: %v", entry)
	}
}

type archServer struct {
	incus.InstanceServer
	arch string
}

func (s archServer) GetServer() (*api.Server, string, error) {
	return &api.Server{Environment: api.ServerEnvironment{KernelArchitecture: s.arch}}, "", nil
}

// imageEntrypoint reads the image's entrypoint from the registry its REMOTE:REF names, through the client configuration's remote, for the server's architecture;
// a remote that is not an OCI one has none to read.
func TestImageEntrypointReadsTheRegistryOfTheImagesRemote(t *testing.T) {
	reg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/1":
			json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"digest": "sha256:c"}})
		case "/v2/team/app/blobs/sha256:c":
			json.NewEncoder(w).Encode(map[string]any{"config": map[string]any{"Entrypoint": []string{"bash", "run.sh"}, "Cmd": []string{"x"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer reg.Close()
	dir := t.TempDir()
	conf := "remotes:\n  mine:\n    addr: " + reg.URL + "\n    protocol: oci\n  pics:\n    addr: https://images.example.org\n    protocol: simplestreams\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INCUS_CONF", dir)

	got, err := imageEntrypoint(archServer{arch: "x86_64"}, "mine:team/app:1")
	if err != nil || !reflect.DeepEqual(got, []string{"bash", "run.sh"}) {
		t.Errorf("got %v, %v", got, err)
	}
	if _, err := imageEntrypoint(archServer{arch: "x86_64"}, "pics:alpine/3.21"); err != errNotOCI {
		t.Errorf("a simplestreams remote has no registry to read: %v", err)
	}
	if _, err := imageEntrypoint(archServer{arch: "x86_64"}, "a-local-alias"); err != errNotOCI {
		t.Errorf("a name with no remote is not an OCI image: %v", err)
	}
	if _, err := imageEntrypoint(archServer{arch: "x86_64"}, "mine:team/missing:1"); err == nil {
		t.Error("a registry that has no such image must be an error, not an empty entrypoint")
	}
}
