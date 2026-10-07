package incusapi

import (
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
		"no remote: the local daemon":                   {"", "", false, ""},
		"no remote, any socket":                         {"", "/run/x.sock", false, ""},
		"a remote, no socket":                           {"tron", "", true, ""},
		"a remote, the default socket is not a choice":  {"tron", DefaultSocket, true, ""},
		"a remote and an explicit socket contradict":    {"tron", "/tmp/other.sock", false, "contradict"},
		"the built-in 'local' remote means the daemon":  {"local", "", false, ""},
		"'local' with an explicit socket is just local": {"local", "/tmp/other.sock", false, ""},
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

func TestIsRemoteFollowsUseRemote(t *testing.T) {
	t.Cleanup(func() { UseRemote("") })
	UseRemote("tron")
	if !IsRemote() || Remote() != "tron" {
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

// docker-oci and friends are image servers, not servers to manage.
func TestConnectRemoteRefusesAnImageServer(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	_, err := ConnectRemote("docker-oci")
	if err == nil || !strings.Contains(err.Error(), "image server") {
		t.Fatalf("an image remote is not an Incus server to manage: %v", err)
	}
}
