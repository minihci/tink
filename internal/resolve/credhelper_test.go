package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

// stubHelper writes a credentials helper that records how it was run (its argument and what it was given on standard input, one line per
// run, in the returned calls file) and then does body. It is a real program run by the real code, as Incus's helper is.
func stubHelper(t *testing.T, body string) (helper, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	helper = filepath.Join(dir, "docker-credential-stub")
	script := "#!/bin/sh\necho \"$1 $(cat)\" >> '" + calls + "'\n" + body + "\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return helper, calls
}

func helperAnswering(t *testing.T, user, secret string) (helper, calls string) {
	return stubHelper(t, `printf '{"ServerURL":"x","Username":"`+user+`","Secret":"`+secret+`"}'`)
}

func callsTo(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// withHelper is the remote at host, with a credentials helper.
func withHelper(host, helper string) cliconfig.Remote {
	r := remoteAt(host)
	r.CredHelper = helper
	return r
}

func TestAHelpersCredentialsAreWrittenIntoTheAddressAsIncusWritesThem(t *testing.T) {
	// A password with the characters that an address treats specially: it must survive the trip into the address and out again.
	const secret = "p@ss/w:rd#1?"
	helper, calls := helperAnswering(t, "pull er", secret)
	remote := cliconfig.Remote{Addrs: []string{"https://user:old@registry.example:5000/", "https://other"}, Protocol: "oci", CredHelper: helper}

	got, err := withHelperCredentials(remote)
	if err != nil {
		t.Fatal(err)
	}
	if user, password, ok := registryCredentials(got); !ok || user != "pull er" || password != secret {
		t.Errorf("credentials = %q %q %v, want the helper's, replacing the ones already in the address", user, password, ok)
	}
	if remoteHost(got) != "registry.example:5000" {
		t.Errorf("host = %q: the credentials must not change where the registry is", remoteHost(got))
	}
	if len(got.Addrs) != 2 || got.Addrs[1] != "https://other" {
		t.Errorf("addrs = %v: the others are kept", got.Addrs)
	}
	if remote.Addrs[0] != "https://user:old@registry.example:5000/" {
		t.Errorf("the loaded configuration was changed: %v", remote.Addrs)
	}
	// What Incus does: `HELPER get`, with the registry's host (and port) on standard input.
	if c := callsTo(t, calls); len(c) != 1 || c[0] != "get registry.example:5000" {
		t.Errorf("the helper was run as %q, want once, as `get` with the host on standard input", c)
	}
}

func TestARemoteWithoutAHelperIsLeftAlone(t *testing.T) {
	remote := cliconfig.Remote{Addrs: []string{"https://user:pw@registry.example"}, Protocol: "oci"}
	got, err := withHelperCredentials(remote)
	if err != nil || got.Addrs[0] != remote.Addrs[0] {
		t.Errorf("%v %v: its own credentials are used as they are", got.Addrs, err)
	}
}

func TestAHelperThatFailsIsSaidSoWithItsOwnWords(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "docker-credential-nothing")
	notFound, _ := stubHelper(t, `echo "credentials not found in native keychain" >&2; exit 1`)
	garbage, _ := stubHelper(t, `echo "not json"`)
	empty, _ := stubHelper(t, `printf '{}'`)
	for name, tc := range map[string]struct{ helper, want string }{
		"it is not installed":            {missing, "failed"},
		"it has no credentials":          {notFound, "credentials not found in native keychain"},
		"it answers with something else": {garbage, "did not answer with"},
		"it answers with nothing":        {empty, "no username and no secret"},
	} {
		_, err := withHelperCredentials(withHelper("registry.example", tc.helper))
		if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.helper) {
			t.Errorf("%s: err = %v, want it to name the helper and say %q", name, err, tc.want)
		}
	}
}

// A private registry reached through a helper: the lookup of an image, which the server cannot do for tink, is authenticated by it.
func TestTheLookupOfAnImageUsesTheHelpersCredentials(t *testing.T) {
	var enforce atomic.Bool
	host := newRegistry(t, requireBasic(&enforce, "puller", "s3cret"))
	imgs, _ := pushIndex(t, host, "team/private:1", "amd64")
	pushIndex(t, host, "team/private:2", "amd64")
	enforce.Store(true)

	t.Run("without the helper the registry refuses", func(t *testing.T) {
		if _, err := envFor(host).registryImage(newPullServer(), "reg", "team/private:1"); err == nil {
			t.Fatal("the registry wants a login: the lookup must fail without one")
		}
	})

	t.Run("with it the image is found, and the runtime config too", func(t *testing.T) {
		helper, _ := helperAnswering(t, "puller", "s3cret")
		env := envFor(host)
		env.conf.Remotes["reg"] = withHelper(host, helper)
		img, err := env.registryImage(newPullServer(), "reg", "team/private:1")
		if err != nil || img.Fingerprint != want(t, imgs["amd64"]).Fingerprint {
			t.Fatalf("%+v %v", img, err)
		}
		if _, err := env.runtimeConfig(newPullServer(), env.conf.Remotes["reg"], "team/private:1"); err != nil {
			t.Errorf("the runtime config is read with the same credentials: %v", err)
		}
	})

	t.Run("a helper with the wrong credentials is a refusal, not a silent anonymous lookup", func(t *testing.T) {
		helper, _ := helperAnswering(t, "puller", "wrong")
		env := envFor(host)
		env.conf.Remotes["reg"] = withHelper(host, helper)
		if _, err := env.registryImage(newPullServer(), "reg", "team/private:1"); err == nil {
			t.Error("the helper's login is the one used, and it is wrong")
		}
	})

	t.Run("a failing helper is reported against the remote, and the registry is not asked anonymously", func(t *testing.T) {
		helper, _ := stubHelper(t, `echo "keychain is locked" >&2; exit 1`)
		env := envFor(host)
		env.conf.Remotes["reg"] = withHelper(host, helper)
		_, err := env.registryImage(newPullServer(), "reg", "team/private:1")
		if err == nil || !strings.Contains(err.Error(), `remote "reg"`) || !strings.Contains(err.Error(), "keychain is locked") {
			t.Errorf("err = %v, want it to say which remote and why", err)
		}
		_, err = env.runtimeConfig(newPullServer(), env.conf.Remotes["reg"], "team/private:1")
		if err == nil || !strings.Contains(err.Error(), "keychain is locked") {
			t.Errorf("runtime config err = %v", err)
		}
	})

	t.Run("instances are checked at once, and the helper is asked once", func(t *testing.T) {
		helper, calls := helperAnswering(t, "puller", "s3cret")
		env := envFor(host)
		env.conf.Remotes["reg"] = withHelper(host, helper)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ref := "team/private:" + string(rune('1'+i%2))
				if _, err := env.registryImage(newPullServer(), "reg", ref); err != nil {
					t.Errorf("%s: %v", ref, err)
				}
			}()
		}
		wg.Wait()
		if c := callsTo(t, calls); len(c) != 1 {
			t.Errorf("the helper was run %d times: %q; a helper that asks for a keychain password should ask once", len(c), c)
		}
	})
}

// The pull is the server's, and the only way to give it a login is the address: it must be sent with the helper's credentials in it,
// and nothing that comes back may repeat them.
func TestTheServersPullIsGivenTheHelpersCredentials(t *testing.T) {
	var enforce atomic.Bool
	host := newRegistry(t, requireBasic(&enforce, "puller", "s3cret"))
	imgs, _ := pushIndex(t, host, "team/private:1", "amd64")
	fp := want(t, imgs["amd64"]).Fingerprint
	enforce.Store(true)
	helper, calls := helperAnswering(t, "puller", "s3cret")
	envWithHelper := func() *imageEnv {
		env := envFor(host)
		env.conf.Remotes["reg"] = withHelper(host, helper)
		return env
	}

	t.Run("the address sent carries them", func(t *testing.T) {
		srv := newPullServer()
		srv.storedAs = fp
		if got, err := (incusRebuildOps{server: srv, env: envWithHelper()}).PullImage("reg:team/private:1"); err != nil || got != fp {
			t.Fatalf("%q %v", got, err)
		}
		if len(srv.pulled) != 1 || srv.pulled[0].Source.Server != "http://puller:s3cret@"+host {
			t.Errorf("pulled = %+v, want the pull sent to http://puller:s3cret@%s, as `incus launch` sends it", srv.pulled, host)
		}
	})

	t.Run("an image the server already has does not need the helper again", func(t *testing.T) {
		before := len(callsTo(t, calls))
		srv := newPullServer()
		srv.local[fp] = true
		if _, err := (incusRebuildOps{server: srv, env: envWithHelper()}).PullImage("reg:team/private:1"); err != nil {
			t.Fatal(err)
		}
		if after := len(callsTo(t, calls)); after != before+1 {
			t.Errorf("helper runs went from %d to %d: the lookup asks (once per run) and the pull, not needed, does not ask again", before, after)
		}
	})

	t.Run("a server that repeats the address in its error does not repeat the password", func(t *testing.T) {
		for name, set := range map[string]func(*pullServer){
			"on the request": func(s *pullServer) { s.createErr = errors.New("skopeo: cannot reach http://puller:s3cret@" + host) },
			"in the download": func(s *pullServer) {
				s.waitErr = errors.New("copying from docker://puller:s3cret@" + host + ": 401, password s3cretXX rejected")
			},
		} {
			srv := newPullServer()
			srv.storedAs = fp
			set(srv)
			_, err := incusRebuildOps{server: srv, env: envWithHelper()}.PullImage("reg:team/private:1")
			if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "***") {
				t.Errorf("%s: err = %v, want the failure with the password replaced", name, err)
			}
		}
	})

	t.Run("a helper that fails stops the pull before the server is asked", func(t *testing.T) {
		failing, _ := stubHelper(t, `echo "keychain is locked" >&2; exit 1`)
		env := envFor(host)
		env.conf.Remotes["reg"] = withHelper(host, failing)
		env.images["reg:team/private:1"] = registryResult{img: registryImage{Fingerprint: fp}} // as if the lookup had been made
		srv := newPullServer()
		_, err := incusRebuildOps{server: srv, env: env}.PullImage("reg:team/private:1")
		if err == nil || !strings.Contains(err.Error(), `remote "reg"`) || !strings.Contains(err.Error(), "keychain is locked") {
			t.Errorf("err = %v", err)
		}
		if len(srv.pulled) != 0 {
			t.Errorf("nothing is pulled without credentials: %v", srv.pulled)
		}
	})
}

func TestOnlyThePasswordInAnAddressIsScrubbedFromAnError(t *testing.T) {
	const addr = "https://puller:s3cret99@registry.example"
	for name, tc := range map[string]struct{ err, want string }{
		"the address":    {"cannot reach " + addr, "cannot reach ***"},
		"the password":   {"401 for password s3cret99", "401 for password ***"},
		"neither":        {"registry down", "registry down"},
		"the user alone": {"user puller is unknown", "user puller is unknown"},
	} {
		if got := scrubCredentials(errors.New(tc.err), addr).Error(); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	if scrubCredentials(nil, addr) != nil {
		t.Error("no error stays no error")
	}
	// An address with no credentials, or a short password, is not something to scrub from text.
	plain := errors.New("registry down")
	if scrubCredentials(plain, "https://registry.example") != plain {
		t.Error("an error is returned as it is when the address has no credentials")
	}
	if got := scrubCredentials(errors.New("bad id 12 at https://u:12@h"), "https://u:12@h").Error(); got != "bad id 12 at ***" {
		t.Errorf("a short password stays in the text, but the address does not: %q", got)
	}
}
