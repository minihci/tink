package resolve

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// pullServer is a server with an image store, that records what it is told to pull.
type pullServer struct {
	archServer
	local     map[string]bool
	getErr    error
	createErr error
	waitErr   error
	storedAs  string // the fingerprint it says it stored
	pulled    []api.ImagesPost
}

func (s *pullServer) GetImage(fp string) (*api.Image, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	if s.local[fp] {
		return &api.Image{Fingerprint: fp}, "", nil
	}
	return nil, "", api.StatusErrorf(http.StatusNotFound, "image not found")
}

func (s *pullServer) CreateImage(post api.ImagesPost, _ *incus.ImageCreateArgs) (incus.Operation, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	s.pulled = append(s.pulled, post)
	return pullOp{fp: s.storedAs, err: s.waitErr}, nil
}

type pullOp struct {
	incus.Operation
	fp  string
	err error
}

func (o pullOp) Wait() error { return o.err }

func (o pullOp) Get() api.Operation {
	return api.Operation{Metadata: map[string]any{"fingerprint": o.fp}}
}

func newPullServer() *pullServer {
	return &pullServer{archServer: archServer{archs: []string{"x86_64"}}, local: map[string]bool{}}
}

func TestPullingTheImageOfARebuildLeavesTheDownloadToTheServer(t *testing.T) {
	host := newRegistry(t, nil)
	imgs, digest := pushIndex(t, host, "team/app:1", "amd64", "arm64")
	fp := want(t, imgs["amd64"]).Fingerprint

	t.Run("an image the server already has is not pulled again", func(t *testing.T) {
		srv := newPullServer()
		srv.local[fp] = true
		got, err := incusRebuildOps{server: srv, env: envFor(host)}.PullImage("reg:team/app:1")
		if err != nil || got != fp || len(srv.pulled) != 0 {
			t.Errorf("%q %v, pulled %v: it is local, under the fingerprint the registry gave for the server's architecture", got, err, srv.pulled)
		}
	})

	t.Run("a new image is pulled by the server, by reference and never by fingerprint", func(t *testing.T) {
		srv := newPullServer()
		srv.storedAs = fp
		pinned := "team/app:1@" + digest.String()
		got, err := incusRebuildOps{server: srv, env: envFor(host)}.PullImage("reg:" + pinned)
		if err != nil || got != fp {
			t.Fatalf("%q %v", got, err)
		}
		if len(srv.pulled) != 1 {
			t.Fatalf("the server is told once: %v", srv.pulled)
		}
		src := srv.pulled[0].Source
		if src == nil || src.Protocol != "oci" || src.Mode != "pull" || src.Type != "image" || src.Server != "http://"+host || src.Alias != pinned {
			t.Errorf("source = %+v, want an OCI pull of %s from the remote's own address, by reference", src, pinned)
		}
		if src.Fingerprint != "" {
			t.Error("a fingerprint is not something a registry can resolve: the server would try the hash as an image name")
		}
	})

	t.Run("the fingerprint returned is the one the server stored, even if the tag moved after the lookup", func(t *testing.T) {
		srv := newPullServer()
		srv.storedAs = "feedface" + fp[8:]
		got, err := incusRebuildOps{server: srv, env: envFor(host)}.PullImage("reg:team/app:1")
		if err != nil || got != srv.storedAs {
			t.Errorf("%q %v: the rebuild must use what is actually in the store", got, err)
		}
	})

	t.Run("failures are said, and none of them pulls", func(t *testing.T) {
		for name, tc := range map[string]struct {
			image string
			set   func(*pullServer)
			env   func() *imageEnv
			want  string
		}{
			"an image the registry does not have": {"reg:team/nothing:1", func(*pullServer) {}, func() *imageEnv { return envFor(host) }, ""},
			"an unknown remote":                   {"nope:team/app:1", func(*pullServer) {}, func() *imageEnv { return envFor(host) }, `"nope" is not a configured OCI remote`},
			"offline": {"reg:team/app:1", func(*pullServer) {}, func() *imageEnv {
				e := envFor(host)
				e.offline = true
				return e
			}, "--offline"},
			"no way to ask the registry": {"reg:team/app:1", func(*pullServer) {}, func() *imageEnv { return nil }, "no registry access"},
			"a store that cannot be read": {"reg:team/app:1", func(s *pullServer) { s.getErr = errors.New("db locked") }, func() *imageEnv { return envFor(host) },
				"checking whether the image is already local: db locked"},
		} {
			srv := newPullServer()
			tc.set(srv)
			_, err := incusRebuildOps{server: srv, env: tc.env()}.PullImage(tc.image)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
			}
			if len(srv.pulled) != 0 {
				t.Errorf("%s: nothing is pulled: %v", name, srv.pulled)
			}
		}
	})

	t.Run("a pull that fails, or does not say what it stored, is an error", func(t *testing.T) {
		for name, set := range map[string]func(*pullServer){
			"the server refuses":                     func(s *pullServer) { s.createErr = errors.New("project does not allow it") },
			"the download fails":                     func(s *pullServer) { s.waitErr = errors.New("registry down") },
			"the server does not say what it stored": func(s *pullServer) { s.storedAs = "" },
		} {
			srv := newPullServer()
			srv.storedAs = fp
			set(srv)
			if got, err := (incusRebuildOps{server: srv, env: envFor(host)}).PullImage("reg:team/app:1"); err == nil {
				t.Errorf("%s: %q, want an error", name, got)
			}
		}
	})
}
