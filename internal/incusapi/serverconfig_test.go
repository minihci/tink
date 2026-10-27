package incusapi

import (
	"net/http"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

type srvServer struct {
	incus.InstanceServer
	cfg       map[string]string
	etag      int
	conflicts int
	puts      int
}

func (s *srvServer) GetServer() (*api.Server, string, error) {
	cp := map[string]string{}
	for k, v := range s.cfg {
		cp[k] = v
	}
	srv := &api.Server{}
	srv.Config = cp
	return srv, string(rune('a' + s.etag)), nil
}

func (s *srvServer) UpdateServer(put api.ServerPut, _ string) error {
	s.puts++
	if s.conflicts > 0 {
		s.conflicts--
		s.etag++
		return api.StatusErrorf(http.StatusPreconditionFailed, "ETag doesn't match")
	}
	s.cfg = put.Config
	s.etag++
	return nil
}

func TestMergeServerConfigSetsAndUnsetsOnlyWhatItNamesAndKeepsTheRest(t *testing.T) {
	s := &srvServer{cfg: map[string]string{"core.https_address": "127.0.0.1:8443", "authorization.client.tls": "scriptlet", "images.compression_algorithm": "zstd"}}
	if err := MergeServerConfig(s, map[string]string{"authorization.scriptlet": "x"}, []string{"authorization.client.tls", "not.set"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"core.https_address": "127.0.0.1:8443", "images.compression_algorithm": "zstd", "authorization.scriptlet": "x"}
	if len(s.cfg) != len(want) {
		t.Fatalf("config = %v, want %v", s.cfg, want)
	}
	for k, v := range want {
		if s.cfg[k] != v {
			t.Errorf("config[%s] = %q, want %q", k, s.cfg[k], v)
		}
	}
}

func TestMergeServerConfigReadsAgainWhenSomeoneElseWroteFirst(t *testing.T) {
	s := &srvServer{cfg: map[string]string{"a": "1"}, conflicts: 1}
	if err := MergeServerConfig(s, map[string]string{"b": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	if s.puts != 2 || s.cfg["a"] != "1" || s.cfg["b"] != "2" {
		t.Errorf("puts = %d, config = %v: a conflict should read again and write once more", s.puts, s.cfg)
	}
}
