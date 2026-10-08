package remote

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
	localtls "github.com/lxc/incus/v7/shared/tls"
)

// fakeIncus is a TLS server that speaks just enough of the Incus API for `remote add`: it reports whether it
// trusts the caller, accepts one trust-token secret to start trusting, and lists projects. The real Incus Go
// client talks to it, so what is tested is the real handshake, pinning and parsing.
type fakeIncus struct {
	*httptest.Server
	mu           sync.Mutex
	trusted      bool
	secret       string // the trust token the server will accept
	projects     []string
	authMethods  []string
	posted       []string // trust tokens presented
	public       bool
	requestPaths []string
	// stayUntrusted makes the server accept a trust token (it answers 200) without starting to trust the caller.
	stayUntrusted bool
}

func newFake(t *testing.T, trusted bool, secret string, projects ...string) *fakeIncus {
	t.Helper()
	f := &fakeIncus{trusted: trusted, secret: secret, projects: projects, authMethods: []string{"tls"}}
	mux := http.NewServeMux()
	sync200 := func(w http.ResponseWriter, md any) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"type": "sync", "status": "Success", "status_code": 200, "metadata": md})
	}
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requestPaths = append(f.requestPaths, r.Method+" "+r.URL.Path)
		auth := "untrusted"
		if f.trusted {
			auth = "trusted"
		}
		sync200(w, api.Server{ServerUntrusted: api.ServerUntrusted{APIExtensions: []string{"projects"}, APIStatus: "stable", APIVersion: "1.0", Public: f.public, Auth: auth, AuthMethods: f.authMethods}})
	})
	mux.HandleFunc("/1.0/certificates", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		var req api.CertificatesPost
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.posted = append(f.posted, req.TrustToken)
		// like the real server: the token is decoded and its inner secret checked
		presented := req.TrustToken
		if dec, err := localtls.CertificateTokenDecode(req.TrustToken); err == nil {
			presented = dec.Secret
		}
		if r.Method != http.MethodPost || f.secret == "" || presented != f.secret {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": "invalid trust token", "error_code": 403})
			return
		}
		f.trusted = !f.stayUntrusted
		sync200(w, nil)
	})
	mux.HandleFunc("/1.0/projects", func(w http.ResponseWriter, r *http.Request) {
		var urls []string
		for _, p := range f.projects {
			urls = append(urls, "/1.0/projects/"+p)
		}
		sync200(w, urls)
	})
	mux.HandleFunc("/1.0/projects/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/1.0/projects/")
		for _, p := range f.projects {
			if p == name {
				sync200(w, api.Project{Name: name})
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": "not found", "error_code": 404})
	})
	f.Server = httptest.NewTLSServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeIncus) fingerprint() string { return localtls.CertFingerprint(f.Certificate()) }
func (f *fakeIncus) addr() string        { return strings.TrimPrefix(f.URL, "https://") }

func token(t *testing.T, f *fakeIncus, secret, fingerprint string) string {
	t.Helper()
	b, err := json.Marshal(api.CertificateAddToken{ClientName: "test", Fingerprint: fingerprint, Addresses: []string{f.addr()}, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// emptyConf is a client configuration directory that does not exist yet: a machine that never had an Incus client.
func emptyConf(t *testing.T) (*cliconfig.Config, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "incus")
	t.Setenv("INCUS_CONF", dir)
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	return conf, dir
}

func reload(t *testing.T) *cliconfig.Config {
	t.Helper()
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

func nothingLeft(t *testing.T, dir, name string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "servercerts", name+".crt")); err == nil {
		t.Errorf("a failed add must not leave the server certificate behind")
	}
	if _, ok := reload(t).Remotes[name]; ok {
		t.Errorf("a failed add must not save the remote")
	}
}

func TestAddWithATokenPinsTheServerAndGetsTrusted(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, dir := emptyConf(t)
	var out bytes.Buffer

	// No address: the token carries it.
	res, err := Add(conf, AddOptions{Name: "tron", Token: token(t, f, "s3cret", f.fingerprint()), Out: &out})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if res.Verified != "token" || !res.Trusted || res.Fingerprint != f.fingerprint() {
		t.Errorf("result = %+v", res)
	}
	if got := f.posted; len(got) != 1 || got[0] == "" {
		t.Errorf("the trust token must be presented exactly once: %v", got)
	}
	saved := reload(t).Remotes["tron"]
	if saved.AuthType != "tls" || saved.Protocol != "incus" || len(saved.Addrs) != 1 || saved.Addrs[0] != "https://"+f.addr() {
		t.Errorf("saved remote = %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(dir, "servercerts", "tron.crt")); err != nil {
		t.Errorf("the server certificate must be stored so later connections are pinned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "client.crt")); err != nil {
		t.Errorf("a client certificate must have been generated: %v", err)
	}
	// and the stored remote really connects, trusted
	d, err := reload(t).GetInstanceServer("tron")
	if err != nil {
		t.Fatal(err)
	}
	if srv, _, err := d.GetServer(); err != nil || srv.Auth != "trusted" {
		t.Errorf("the saved remote must connect trusted: %v %+v", err, srv)
	}
}

func TestATokenForADifferentServerIsRefused(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, dir := emptyConf(t)
	wrong := strings.Repeat("ab", 32)
	_, err := Add(conf, AddOptions{Name: "tron", Token: token(t, f, "s3cret", wrong)})
	if err == nil || !strings.Contains(err.Error(), "refusing to trust") {
		t.Fatalf("a certificate that does not match the token must be refused: %v", err)
	}
	if len(f.posted) != 0 {
		t.Error("the secret must never be sent to a server that failed verification")
	}
	nothingLeft(t, dir, "tron")
}

func TestAddAServerThatAlreadyTrustsUs(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, _ := emptyConf(t)
	res, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Fingerprint: strings.ToUpper(f.fingerprint())})
	if err != nil || res.Verified != "fingerprint" || !res.Trusted {
		t.Fatalf("%v %+v", err, res)
	}
	if len(f.posted) != 0 {
		t.Error("no token is needed, and none may be sent, when the server already trusts the client")
	}
}

func TestNothingVerifyingTheServerIsRefusedWithTheFingerprint(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr()})
	if err == nil || !strings.Contains(err.Error(), f.fingerprint()) || !strings.Contains(err.Error(), "--fingerprint") {
		t.Fatalf("the refusal must show the fingerprint and how to proceed: %v", err)
	}
	nothingLeft(t, dir, "tron")
}

func TestConfirmationAndTrustOnFirstUse(t *testing.T) {
	for name, tc := range map[string]struct {
		opts    func(fp string) AddOptions
		want    string
		wantErr bool
	}{
		"accept-certificate": {func(string) AddOptions { return AddOptions{AcceptCertificate: true} }, "accepted", false},
		"confirmed": {func(fp string) AddOptions {
			return AddOptions{Confirm: func(got string) (bool, error) { return got == fp, nil }}
		}, "confirmed", false},
		"refused by the user": {func(string) AddOptions { return AddOptions{Confirm: func(string) (bool, error) { return false, nil }} }, "", true},
	} {
		f := newFake(t, true, "", "default")
		conf, dir := emptyConf(t)
		o := tc.opts(f.fingerprint())
		o.Name, o.Addr = "tron", f.addr()
		res, err := Add(conf, o)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected a refusal", name)
			}
			nothingLeft(t, dir, "tron")
			continue
		}
		if err != nil || res.Verified != tc.want {
			t.Errorf("%s: %v %+v", name, err, res)
		}
	}
}

func TestAServerThatDoesNotTrustUsAndNoToken(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Fingerprint: f.fingerprint()})
	if err == nil || !strings.Contains(err.Error(), "does not trust this machine") || !strings.Contains(err.Error(), "client.crt") {
		t.Fatalf("the error must explain what to ask for: %v", err)
	}
	nothingLeft(t, dir, "tron")
}

func TestAWrongTrustTokenLeavesNothingBehind(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Token: token(t, f, "not-the-secret", f.fingerprint())})
	if err == nil || !strings.Contains(err.Error(), "trust token") {
		t.Fatalf("%v", err)
	}
	nothingLeft(t, dir, "tron")
}

func TestAddRefusesWhatIsNotAManageableServer(t *testing.T) {
	f := newFake(t, true, "", "default")
	f.authMethods = []string{"oidc"} // TLS is not offered
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Fingerprint: f.fingerprint()})
	if err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("a server without TLS authentication must be refused clearly: %v", err)
	}
	nothingLeft(t, dir, "tron")

	g := newFake(t, true, "", "default")
	g.public = true
	_, err = Add(conf, AddOptions{Name: "img", Addr: g.addr(), Fingerprint: g.fingerprint()})
	if err == nil || !strings.Contains(err.Error(), "image server") {
		t.Fatalf("%v", err)
	}
}

func TestAddRefusesAnExistingName(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, _ := emptyConf(t)
	if _, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), AcceptCertificate: true}); err != nil {
		t.Fatal(err)
	}
	_, err := Add(reload(t), AddOptions{Name: "tron", Addr: f.addr(), AcceptCertificate: true})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("%v", err)
	}
}

func TestProjectChoice(t *testing.T) {
	for name, tc := range map[string]struct {
		projects []string
		asked    string
		want     string
		wantErr  string
	}{
		"only one project":                  {[]string{"tink-backup"}, "", "tink-backup", ""},
		"default among several":             {[]string{"default", "other"}, "", "", ""},
		"several, no default":               {[]string{"a", "b"}, "", "", "choose with --project"},
		"an explicit project that exists":   {[]string{"default", "other"}, "other", "other", ""},
		"an explicit project that does not": {[]string{"default"}, "nope", "", "nope"},
	} {
		f := newFake(t, true, "", tc.projects...)
		conf, _ := emptyConf(t)
		res, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), AcceptCertificate: true, Project: tc.asked})
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if err != nil || res.Project != tc.want || reload(t).Remotes["tron"].Project != tc.want {
			t.Errorf("%s: %v project=%q saved=%q", name, err, res.Project, reload(t).Remotes["tron"].Project)
		}
	}
}

func TestNormalizeAddr(t *testing.T) {
	for in, want := range map[string]string{
		"tron":                   "https://tron:8443",
		"tron:9443":              "https://tron:9443",
		"https://tron":           "https://tron:8443",
		"https://10.0.0.1:8443/": "https://10.0.0.1:8443",
		"[fd42::1]:8443":         "https://[fd42::1]:8443",
		" tron ":                 "https://tron:8443",
	} {
		got, err := NormalizeAddr(in)
		if err != nil || got != want {
			t.Errorf("NormalizeAddr(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "http://tron", "unix:///var/lib/incus/unix.socket", "https://"} {
		if _, err := NormalizeAddr(bad); err == nil {
			t.Errorf("NormalizeAddr(%q) must fail", bad)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"tron", "my-vps", "a.b"} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a:b", "a/b", "a b", "local"} {
		if err := ValidName(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
}

func TestRemoveAndList(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, dir := emptyConf(t)
	if _, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), AcceptCertificate: true}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	List(reload(t), []string{"docker-oci", "ghcr"}, &out)
	if !strings.Contains(out.String(), "tron") || !strings.Contains(out.String(), "built in, used when not configured: docker-oci, ghcr") {
		t.Errorf("list output:\n%s", out.String())
	}

	if err := Remove(reload(t), "local"); err == nil {
		t.Error("the built-in local remote cannot be removed")
	}
	if err := Remove(reload(t), "nope"); err == nil {
		t.Error("an unknown remote must be an error")
	}
	if err := Remove(reload(t), "tron"); err != nil {
		t.Fatal(err)
	}
	nothingLeft(t, dir, "tron")
}

func TestNormalizeFingerprint(t *testing.T) {
	const fp = "0f3a9c2d7b6e41805a9e3c7d2f1b8a4960d5e7c3b2a19f8e7d6c5b4a39281706"
	var colons []string
	for i := 0; i < len(fp); i += 2 {
		colons = append(colons, strings.ToUpper(fp[i:i+2]))
	}
	for name, in := range map[string]string{
		"as is":                       fp,
		"upper case with colons":      strings.Join(colons, ":"),
		"surrounding space":           "  " + fp + "\n",
		"upper case, no colons":       strings.ToUpper(fp),
		"the form `incus` prints too": strings.ToLower(strings.Join(colons, ":")),
	} {
		if got, err := NormalizeFingerprint(in); err != nil || got != fp {
			t.Errorf("%s: NormalizeFingerprint(%q) = %q, %v; want %q", name, in, got, err, fp)
		}
	}
	for name, in := range map[string]string{
		"empty":       "",
		"too short":   fp[:62],
		"too long":    fp + "00",
		"not hex":     strings.Repeat("zz", 32),
		"a SHA-1":     fp[:40],
		"words in it": "SHA256:" + fp,
	} {
		if got, err := NormalizeFingerprint(in); err == nil {
			t.Errorf("%s: NormalizeFingerprint(%q) = %q, want an error", name, in, got)
		}
	}
}
