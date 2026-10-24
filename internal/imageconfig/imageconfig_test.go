package imageconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// registry is a small fake: an index with two platforms, each with its own manifest and configuration, behind a bearer-token challenge.
func registry(t *testing.T, needToken bool) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	cfg := func(entry, cmd []string) map[string]any {
		return map[string]any{"config": map[string]any{"Entrypoint": entry, "Cmd": cmd}}
	}
	docs := map[string]any{
		"/v2/team/app/manifests/1": map[string]any{"manifests": []any{
			map[string]any{"digest": "sha256:arm", "platform": map[string]any{"os": "linux", "architecture": "arm64"}},
			map[string]any{"digest": "sha256:attest", "platform": map[string]any{"os": "unknown", "architecture": "unknown"}},
			map[string]any{"digest": "sha256:amd", "platform": map[string]any{"os": "linux", "architecture": "amd64"}},
		}},
		"/v2/team/app/manifests/sha256:amd": map[string]any{"config": map[string]any{"digest": "sha256:cfgamd"}},
		"/v2/team/app/manifests/sha256:arm": map[string]any{"config": map[string]any{"digest": "sha256:cfgarm"}},
		"/v2/team/app/blobs/sha256:cfgamd":  cfg([]string{"bash", "run.sh"}, nil),
		"/v2/team/app/blobs/sha256:cfgarm":  cfg([]string{"arm-entry"}, []string{"-x"}),
		"/v2/team/plain/manifests/latest":   map[string]any{"config": map[string]any{"digest": "sha256:plain"}},
		"/v2/team/plain/blobs/sha256:plain": cfg(nil, []string{"/bin/sh"}),
	}
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			if r.URL.Query().Get("service") != "reg" || r.URL.Query().Get("scope") != "repository:team/app:pull" && r.URL.Query().Get("scope") != "repository:team/plain:pull" {
				http.Error(w, "bad request "+r.URL.RawQuery, http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"token": "t0k"})
			return
		}
		if needToken && r.Header.Get("Authorization") != "Bearer t0k" {
			w.Header().Set("Www-Authenticate", `Bearer realm="`+srv.URL+`/token",service="reg",scope="repository:`+strings.Split(r.URL.Path, "/")[2]+`/`+strings.Split(r.URL.Path, "/")[3]+`:pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		d, ok := docs[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(d)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchChoosesThePlatformAndAnswersTheTokenChallenge(t *testing.T) {
	for _, needToken := range []bool{false, true} {
		srv := registry(t, needToken)
		got, err := Fetch(context.Background(), srv.Client(), srv.URL, "team/app", "1", "amd64")
		if err != nil {
			t.Fatalf("token %v: %v", needToken, err)
		}
		if !reflect.DeepEqual(got.Entrypoint, []string{"bash", "run.sh"}) || len(got.Cmd) != 0 {
			t.Errorf("token %v: amd64 = %+v", needToken, got)
		}
		arm, err := Fetch(context.Background(), srv.Client(), srv.URL, "team/app", "1", "arm64")
		if err != nil || !reflect.DeepEqual(arm.Entrypoint, []string{"arm-entry"}) || !reflect.DeepEqual(arm.Cmd, []string{"-x"}) {
			t.Errorf("token %v: arm64 = %+v, %v", needToken, arm, err)
		}
	}
}

func TestFetchReadsAManifestThatIsNotAnIndexAndAnImageWithOnlyACmd(t *testing.T) {
	srv := registry(t, true)
	got, err := Fetch(context.Background(), srv.Client(), srv.URL, "team/plain", "latest", "amd64")
	if err != nil || len(got.Entrypoint) != 0 || !reflect.DeepEqual(got.Cmd, []string{"/bin/sh"}) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestFetchSaysWhatWentWrong(t *testing.T) {
	srv := registry(t, false)
	for name, c := range map[string]struct{ repo, ref, arch, want string }{
		"a platform the index lacks": {"team/app", "1", "riscv64", "no linux/riscv64 image"},
		"a missing repository":       {"team/none", "1", "amd64", "404"},
	} {
		if _, err := Fetch(context.Background(), srv.Client(), srv.URL, c.repo, c.ref, c.arch); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	// a registry that wants credentials: a challenge that is not a bearer one is an error that says so, not an empty entrypoint
	basic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Www-Authenticate", `Basic realm="x"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer basic.Close()
	if _, err := Fetch(context.Background(), basic.Client(), basic.URL, "a/b", "1", "amd64"); err == nil || !strings.Contains(err.Error(), "wants credentials") {
		t.Errorf("basic challenge: %v", err)
	}
	// a token endpoint that refuses
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		w.Header().Set("Www-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="s"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer deny.Close()
	if _, err := Fetch(context.Background(), deny.Client(), deny.URL, "a/b", "1", "amd64"); err == nil || !strings.Contains(err.Error(), "may need credentials") {
		t.Errorf("refused token: %v", err)
	}
}

func TestFetchRefusesABodyBeyondTheLimit(t *testing.T) {
	big := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(strings.Repeat("x", maxBody+10)))
	}))
	defer big.Close()
	if _, err := Fetch(context.Background(), big.Client(), big.URL, "a/b", "1", "amd64"); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Errorf("err = %v", err)
	}
}
