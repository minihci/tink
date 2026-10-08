package remote

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	localtls "github.com/lxc/incus/v7/shared/tls"
)

// These pin the branches of Add that remote_test.go does not reach, as it behaves now, so that taking Add apart cannot change what a
// person is told, what is sent to a server and when, or what is left on disk after a failure.

// tokenAt is a trust token whose server is reachable at addrs, which the test chooses.
func tokenAt(t *testing.T, secret, fingerprint string, addrs ...string) string {
	t.Helper()
	b, err := json.Marshal(api.CertificateAddToken{ClientName: "test", Fingerprint: fingerprint, Addresses: addrs, Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// fetchWithDead fetches certificates over the network, except from the normalised addresses given, which fail with the message they map to.
func fetchWithDead(dead map[string]string) func(string) (*x509.Certificate, error) {
	return func(addr string) (*x509.Certificate, error) {
		if msg, isDead := dead[addr]; isDead {
			return nil, errors.New(msg)
		}
		return localtls.GetRemoteCertificate(addr, "tink-test")
	}
}

func TestAddRefusesAnInvalidName(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, _ := emptyConf(t)
	for _, name := range []string{"", "a/b", "local"} {
		_, err := Add(conf, AddOptions{Name: name, Addr: f.addr(), AcceptCertificate: true})
		if want := ValidName(name); err == nil || err.Error() != want.Error() {
			t.Errorf("name %q: err = %v, want %v", name, err, want)
		}
	}
	if len(f.requestPaths) != 0 {
		t.Errorf("nothing may be sent to a server for a name that cannot be used: %v", f.requestPaths)
	}
}

// A token that is not the encoded kind is an older server's bare secret. It carries no fingerprint and no address, so it can pin
// nothing, but it is still presented to a server that was verified some other way.
func TestABareSecretCannotPinTheServerButIsStillPresented(t *testing.T) {
	f := newFake(t, false, "bare", "default")
	conf, _ := emptyConf(t)
	res, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Fingerprint: f.fingerprint(), Token: "bare"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified != "fingerprint" || !res.Trusted {
		t.Errorf("result = %+v: the server was verified by the fingerprint given, not by the token", res)
	}
	if len(f.posted) != 1 || f.posted[0] != "bare" {
		t.Errorf("the bare secret is sent exactly as given, once: %v", f.posted)
	}
}

func TestABareSecretIsNotAVerification(t *testing.T) {
	f := newFake(t, false, "bare", "default")
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Token: "bare"})
	if err == nil || !strings.Contains(err.Error(), f.fingerprint()) || !strings.Contains(err.Error(), "--fingerprint") {
		t.Fatalf("a bare secret verifies nothing, so the refusal must show the fingerprint: %v", err)
	}
	if len(f.posted) != 0 {
		t.Error("the secret must not be sent to a server that was never verified")
	}
	nothingLeft(t, dir, "tron")
}

func TestNoAddressAnywhereIsRefused(t *testing.T) {
	conf, _ := emptyConf(t)
	for name, token := range map[string]string{
		"no token":                  "",
		"a bare secret":             "bare",
		"a token with no addresses": tokenAt(t, "s", strings.Repeat("ab", 32)),
	} {
		_, err := Add(conf, AddOptions{Name: "tron", Token: token})
		if err == nil || !strings.Contains(err.Error(), "no server address") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestAnAddressThatIsNotHTTPSIsRefusedFromTheOptionAndFromTheToken(t *testing.T) {
	conf, _ := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Addr: "http://tron:8443", AcceptCertificate: true})
	if err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("from the option: %v", err)
	}
	_, err = Add(conf, AddOptions{Name: "tron", Token: tokenAt(t, "s", strings.Repeat("ab", 32), "http://tron:8443")})
	if err == nil || !strings.Contains(err.Error(), "only https") {
		t.Errorf("from the token: %v", err)
	}
}

func TestAServerThatCannotBeReachedIsRefusedNamingEveryAddressTried(t *testing.T) {
	conf, dir := emptyConf(t)
	tok := tokenAt(t, "s", strings.Repeat("ab", 32), "127.0.0.1:1", "127.0.0.1:2")
	fetch := fetchWithDead(map[string]string{"https://127.0.0.1:1": "first is down", "https://127.0.0.1:2": "second is down"})
	_, err := Add(conf, AddOptions{Name: "tron", Token: tok, fetchCert: fetch})
	if err == nil || !strings.Contains(err.Error(), "could not reach the server") || !strings.Contains(err.Error(), "first is down") || !strings.Contains(err.Error(), "second is down") {
		t.Fatalf("every address tried must be in the error: %v", err)
	}
	nothingLeft(t, dir, "tron")
}

func TestTheFirstAddressThatAnswersIsTheOneSaved(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	conf, _ := emptyConf(t)
	tok := tokenAt(t, "s3cret", f.fingerprint(), "127.0.0.1:1", f.addr())
	res, err := Add(conf, AddOptions{Name: "tron", Token: tok, fetchCert: fetchWithDead(map[string]string{"https://127.0.0.1:1": "down"})})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://" + f.addr()
	if saved := reload(t).Remotes["tron"]; res.Addr != want || len(saved.Addrs) != 1 || saved.Addrs[0] != want {
		t.Errorf("result %q, saved %v: only the address that answered is kept, want %s", res.Addr, saved.Addrs, want)
	}
}

func TestAConfirmationThatFailsIsAnError(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Confirm: func(string) (bool, error) { return false, errors.New("terminal gone") }})
	if err == nil || !strings.Contains(err.Error(), "terminal gone") {
		t.Fatalf("%v", err)
	}
	nothingLeft(t, dir, "tron")
}

func TestAServerCertificateThatCannotBeStoredLeavesNothingBehind(t *testing.T) {
	for name, tc := range map[string]struct {
		block func(t *testing.T, dir string)
		want  string
	}{
		"the directory is a file": {func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "servercerts"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "creating the server certificate directory"},
		"the certificate path is a directory": {func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "servercerts", "tron.crt"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "storing the server certificate"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, false, "s3cret", "default")
			conf, dir := emptyConf(t)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			tc.block(t, dir)
			_, err := Add(conf, AddOptions{Name: "tron", Token: token(t, f, "s3cret", f.fingerprint())})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			if len(f.posted) != 0 {
				t.Error("the secret must not be sent before the server's certificate is safely stored")
			}
			if _, ok := conf.Remotes["tron"]; ok {
				t.Error("the remote must not be left in the configuration")
			}
		})
	}
}

func TestAServerThatAcceptsTheTokenButStillDoesNotTrustUs(t *testing.T) {
	f := newFake(t, false, "s3cret", "default")
	f.stayUntrusted = true
	conf, dir := emptyConf(t)
	_, err := Add(conf, AddOptions{Name: "tron", Token: token(t, f, "s3cret", f.fingerprint())})
	if err == nil || !strings.Contains(err.Error(), "still does not trust this machine") {
		t.Fatalf("%v", err)
	}
	if len(f.posted) != 1 {
		t.Errorf("the token is presented once: %v", f.posted)
	}
	nothingLeft(t, dir, "tron")
}

// The certificate is fetched and verified, the server then stops answering: what was stored for it is removed again.
func TestAServerThatStopsAnsweringAfterItsCertificateWasFetched(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, dir := emptyConf(t)
	fetch := func(string) (*x509.Certificate, error) { return f.Certificate(), nil }
	_, err := Add(conf, AddOptions{Name: "tron", Addr: "127.0.0.1:1", Fingerprint: f.fingerprint(), fetchCert: fetch})
	if err == nil || !strings.Contains(err.Error(), "connecting to https://127.0.0.1:1") {
		t.Fatalf("%v", err)
	}
	nothingLeft(t, dir, "tron")
}

func TestAConfigThatCannotBeSavedLeavesNoCertificateAndNoRemote(t *testing.T) {
	f := newFake(t, true, "", "default")
	conf, dir := emptyConf(t)
	if err := os.MkdirAll(filepath.Join(dir, "config.yml"), 0o755); err != nil { // a directory where the file should go
		t.Fatal(err)
	}
	_, err := Add(conf, AddOptions{Name: "tron", Addr: f.addr(), Fingerprint: f.fingerprint()})
	if err == nil || !strings.Contains(err.Error(), "saving the Incus client configuration") {
		t.Fatalf("%v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "servercerts", "tron.crt")); statErr == nil {
		t.Error("the stored server certificate must be removed when saving fails")
	}
	if _, ok := conf.Remotes["tron"]; ok {
		t.Error("the remote must not stay in the configuration when saving fails")
	}
}
