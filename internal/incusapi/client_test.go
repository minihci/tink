package incusapi

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestWantsRemote(t *testing.T) {
	t.Cleanup(func() { UseRemote("") })
	for name, tc := range map[string]struct {
		remote, socket string
		want           bool
		wantErr        string
	}{
		"no remote: the local daemon":                               {"", "", false, ""},
		"no remote, any socket":                                     {"", "/run/x.sock", false, ""},
		"a remote, no socket":                                       {"homelab", "", true, ""},
		"a remote and an explicit socket contradict":                {"homelab", "/tmp/other.sock", false, "contradict"},
		"the old default path is an explicit socket like any other": {"lab", "/var/lib/incus/unix.socket", false, "contradict"},
		"the built-in 'local' remote means the daemon":              {"local", "", false, ""},
		"'local' with an explicit socket is just local":             {"local", "/tmp/other.sock", false, ""},
	} {
		UseRemote(tc.remote)
		got, err := wantsRemote(tc.socket)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", name, got, err, tc.want)
		}
	}
}

// Without --socket the path is Incus's to resolve, $INCUS_SOCKET first. A default filled in here would hide it, and with it the
// /run/incus/unix.socket that a distribution package such as Fedora's uses: tink then reported "no local Incus daemon" on a machine that
// had one.
func TestConnectWithoutASocketFollowsINCUS_SOCKET(t *testing.T) {
	t.Cleanup(func() { UseRemote("") })
	UseRemote("")
	sock := filepath.Join(t.TempDir(), "elsewhere.sock")
	t.Setenv("INCUS_SOCKET", sock)
	_, err := Connect("")
	if err == nil || !strings.Contains(err.Error(), sock) {
		t.Fatalf("Connect(\"\") must dial $INCUS_SOCKET (%s): %v", sock, err)
	}
	if !strings.Contains(err.Error(), "--socket PATH or $INCUS_SOCKET") {
		t.Errorf("a missing local daemon should say how to point tink at a socket: %v", err)
	}
}

func TestIsRemoteFollowsUseRemote(t *testing.T) {
	t.Cleanup(func() { UseRemote("") })
	UseRemote("homelab")
	if !IsRemote() || Remote() != "homelab" {
		t.Errorf("IsRemote=%v Remote=%q", IsRemote(), Remote())
	}
	UseRemote("local")
	if IsRemote() || Remote() != "" {
		t.Errorf("'local' must mean no remote: IsRemote=%v Remote=%q", IsRemote(), Remote())
	}
}

func TestConnectRemoteWithAnUnknownRemote(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	_, err := ConnectRemote("nope")
	if err == nil || !strings.Contains(err.Error(), `no Incus remote "nope"`) {
		t.Fatalf("an unknown remote must be named: %v", err)
	}
}

func TestAMissingRemoteInTheHelperIsFixedWithTheHelpersOwnCommand(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	t.Cleanup(func() { SetRemoteAdvice("") })
	_, err := ConnectRemote("nas2")
	if err == nil || !strings.Contains(err.Error(), "incus remote add") {
		t.Fatalf("by default the advice is incus's own: %v", err)
	}
	SetRemoteAdvice("run `tink helper remote add %s ADDRESS`")
	_, err = ConnectRemote("nas2")
	if err == nil || !strings.Contains(err.Error(), "tink helper remote add nas2 ADDRESS") || strings.Contains(err.Error(), "incus remote add") {
		t.Errorf("the helper has no incus CLI, so it must not be told to use one: %v", err)
	}
}

// docker-oci and friends are image servers, not servers to manage.
func TestConnectRemoteRefusesAnImageServer(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	_, err := ConnectRemote("docker-oci")
	if err == nil || !strings.Contains(err.Error(), "image server") {
		t.Fatalf("an image remote is not an Incus server to manage: %v", err)
	}
}

// A caller that knows more about a remote than its name (a stack that declares its address) adds to the advice, so it must be able to tell this
// error from any other without reading the message.
func TestAMissingRemoteIsATypedError(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	_, err := ConnectRemote("nope")
	var missing *RemoteNotConfiguredError
	if !errors.As(err, &missing) || missing.Name != "nope" || missing.Advice == "" {
		t.Fatalf("want a *RemoteNotConfiguredError naming the remote, with advice: %#v", err)
	}
	wrapped := fmt.Errorf("target %q: %w", "nas", err)
	if !errors.As(wrapped, &missing) {
		t.Error("it must still be found once a caller has wrapped it")
	}
	// an image server is a different problem, with different advice
	if _, err := ConnectRemote("docker-oci"); errors.As(err, &missing) {
		t.Errorf("a remote that exists but is an image server is not a missing remote: %v", err)
	}
}
