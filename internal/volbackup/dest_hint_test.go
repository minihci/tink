package volbackup

import (
	"errors"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/resolve"
)

const hintFingerprint = "0f3a9c2d7b6e41805a9e3c7d2f1b8a4960d5e7c3b2a19f8e7d6c5b4a39281706"

// connectFailsWith makes every remote connection fail with err for the length of the test.
func connectFailsWith(t *testing.T, err error) {
	t.Helper()
	old := connectRemote
	connectRemote = func(string) (incus.InstanceServer, error) { return nil, err }
	t.Cleanup(func() { connectRemote = old })
}

func TestAMissingRemoteIsExplainedFromWhatTheStackDeclared(t *testing.T) {
	connectFailsWith(t, &incusapi.RemoteNotConfiguredError{Name: "vps", Advice: "add it with `incus remote add`"})
	stack := resolve.Resource{Kind: resolve.KindBackupTarget, Name: "offsite", Remote: "vps", Address: "vps.example.com", Fingerprint: strings.ToUpper(hintFingerprint)}
	tgt := TargetFrom(stack)
	if tgt.Address != "https://vps.example.com:8443" || tgt.Fingerprint != hintFingerprint {
		t.Fatalf("TargetFrom must carry the declared remote, normalised: %+v", tgt)
	}
	_, err := tgt.dest(newFake("tron", "default"), Volume{Name: "lib"})
	if err == nil {
		t.Fatal("the remote is missing: want an error")
	}
	for _, want := range []string{
		`remote "vps"`, `no Incus remote "vps" is configured`,
		"tink remote add vps https://vps.example.com:8443 --fingerprint " + hintFingerprint, "trust token",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must say %q: %v", want, err)
		}
	}
	if !errors.As(err, new(*incusapi.RemoteNotConfiguredError)) {
		t.Error("the cause must still be there for a caller to find")
	}
}

func TestAMissingRemoteWithNothingDeclaredGetsTheUsualMessageOnly(t *testing.T) {
	cause := &incusapi.RemoteNotConfiguredError{Name: "vps", Advice: "add it with `incus remote add`"}
	connectFailsWith(t, cause)
	_, err := Target{Name: "offsite", Remote: "vps"}.dest(newFake("tron", "default"), Volume{Name: "lib"})
	want := `target "offsite": remote "vps": ` + cause.Error()
	if err == nil || err.Error() != want {
		t.Errorf("a target that opted in to nothing must read exactly as before:\n  got:  %v\n  want: %s", err, want)
	}
}

// A remote that is configured but does not answer is not fixed by adding it again, so it must not be told to.
func TestAnUnreachableRemoteIsNotToldToBeAddedAgain(t *testing.T) {
	connectFailsWith(t, errors.New("dial tcp 10.0.0.7:8443: i/o timeout"))
	_, err := Target{Name: "offsite", Remote: "vps", Address: "https://10.0.0.7:8443", Fingerprint: hintFingerprint}.dest(newFake("tron", "default"), Volume{Name: "lib"})
	if err == nil || strings.Contains(err.Error(), "tink remote add") {
		t.Errorf("only a missing remote gets the add command: %v", err)
	}
}
