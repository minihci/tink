package helper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

func runningHelper(h *fakeHost, config map[string]string) {
	if config == nil {
		config = map[string]string{}
	}
	config[MarkerKey] = "1"
	h.instances["helper"] = &api.Instance{Name: "helper", Project: "tink-helper", Status: "Running", InstancePut: api.InstancePut{Config: config}}
}

func statusText(t *testing.T, mut func(*Status)) string {
	t.Helper()
	s := Status{Proto: StatusProto, Version: "v1", JobProto: 1, PolicyProto: 1, Tick: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), HeartbeatSeconds: 600}
	if mut != nil {
		mut(&s)
	}
	text, err := Encode(s)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestAddRemoteRunsRemoteAddInsideTheHelperWithTheTokenOnStandardInput(t *testing.T) {
	h := newHost()
	runningHelper(h, nil)
	in, out := installer(h, nil)

	err := in.AddRemote(RemoteAddOptions{Remote: "nas2", Addr: "https://nas2.lan:8443", Token: "SECRET-TOKEN", RemoteProject: "backups"})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.execs) != 1 {
		t.Fatalf("one exec: %v", h.execs)
	}
	e := h.execs[0]
	if got := strings.Join(e.command, " "); got != "/usr/local/bin/tink remote add nas2 https://nas2.lan:8443 --token-file - --project backups" {
		t.Errorf("command = %s", got)
	}
	if e.stdin != "SECRET-TOKEN" || strings.Contains(strings.Join(e.command, " "), "SECRET") {
		t.Errorf("the token goes on standard input and never on the command line: stdin=%q", e.stdin)
	}
	if e.env["HOME"] != "/root" {
		t.Errorf("the helper's own client configuration is under /root: %v", e.env)
	}
	if !strings.Contains(out.String(), "tink helper remote list") {
		t.Errorf("%s", out.String())
	}
}

func TestAddRemoteWithoutATokenSendsNothingOnStandardInput(t *testing.T) {
	h := newHost()
	runningHelper(h, nil)
	in, _ := installer(h, nil)
	// a server that already trusts the helper's certificate, pinned by fingerprint
	if err := in.AddRemote(RemoteAddOptions{Remote: "nas2", Addr: "nas2.lan", Fingerprint: "abc123", AcceptCertificate: true}); err != nil {
		t.Fatal(err)
	}
	e := h.execs[0]
	if got := strings.Join(e.command, " "); got != "/usr/local/bin/tink remote add nas2 nas2.lan --fingerprint abc123 --accept-certificate" {
		t.Errorf("command = %s", got)
	}
	if e.stdin != "" {
		t.Errorf("stdin = %q", e.stdin)
	}
}

func TestAddRemoteRefusesWhatCannotWork(t *testing.T) {
	for name, c := range map[string]struct {
		opts  RemoteAddOptions
		setup func(*fakeHost)
		want  string
	}{
		"the helper's own host":     {RemoteAddOptions{Remote: "host", Addr: "x"}, nil, "the helper's own host"},
		"a name incus cannot use":   {RemoteAddOptions{Remote: "a:b", Addr: "x"}, nil, "colon"},
		"no name":                   {RemoteAddOptions{Addr: "x"}, nil, "needs a name"},
		"neither address nor token": {RemoteAddOptions{Remote: "nas2"}, nil, "address, or a trust token"},
		"no helper":                 {RemoteAddOptions{Remote: "nas2", Addr: "x"}, func(h *fakeHost) { delete(h.instances, "helper") }, "no helper found"},
		"a stopped helper":          {RemoteAddOptions{Remote: "nas2", Addr: "x"}, func(h *fakeHost) { h.instances["helper"].Status = "Stopped" }, "start it first"},
	} {
		h := newHost()
		runningHelper(h, nil)
		if c.setup != nil {
			c.setup(h)
		}
		in, _ := installer(h, nil)
		err := in.AddRemote(c.opts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
		if len(h.execs) != 0 {
			t.Errorf("%s: nothing is run in the helper: %v", name, h.execs)
		}
	}
}

func TestAddRemoteThatFailsSaysWhy(t *testing.T) {
	h := newHost()
	runningHelper(h, nil)
	h.execCode, h.execOut = 1, "Error: the token was already used\n"
	in, _ := installer(h, nil)
	err := in.AddRemote(RemoteAddOptions{Remote: "nas2", Token: "T"})
	if err == nil || !strings.Contains(err.Error(), "exit 1") || !strings.Contains(err.Error(), "already used") {
		t.Errorf("err = %v", err)
	}
}

func TestAddRemoteDropsTheAdviceThatIsForTheMachineItRanOn(t *testing.T) {
	h := newHost()
	runningHelper(h, nil)
	h.execOut = "added remote nas2: https://nas2.lan:8443 (server certificate ab12, verified by token; this machine is trusted)\nuse it with: tink --remote nas2 plan FILE\n"
	in, out := installer(h, nil)
	if err := in.AddRemote(RemoteAddOptions{Remote: "nas2", Addr: "x", AcceptCertificate: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "added remote nas2") || strings.Contains(out.String(), "use it with") {
		t.Errorf("%s", out.String())
	}
}

func TestRemoveRemote(t *testing.T) {
	h := newHost()
	runningHelper(h, nil)
	in, _ := installer(h, nil)
	if err := in.RemoveRemote("", "", "nas2"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.execs[0].command, " "); got != "/usr/local/bin/tink remote remove nas2" {
		t.Errorf("command = %s", got)
	}
	if err := in.RemoveRemote("", "", "host"); err == nil || !strings.Contains(err.Error(), "the helper's own host") {
		t.Errorf("removing its own host would cut it off from the server it works for: %v", err)
	}
	if len(h.execs) != 1 {
		t.Errorf("the refused one ran nothing: %v", h.execs)
	}
}

func TestRemotesAreReadFromTheStatusDocument(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 4, 0, 0, time.UTC)
	h := newHost()
	runningHelper(h, map[string]string{StatusKey: statusText(t, func(s *Status) {
		s.Remotes = []Remote{{Name: "host", Addr: "https://127.0.0.1:8443"}, {Name: "nas2", Addr: "https://nas2.lan:8443", Project: "backups"}}
	})})
	in, _ := installer(h, nil)
	list, age, label, err := in.Remotes("", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[1].Name != "nas2" || list[1].Project != "backups" || age != 4*time.Minute || label != "tink-helper/helper" {
		t.Errorf("%+v %v %q", list, age, label)
	}
	if len(h.execs) != 0 {
		t.Error("listing is a read of instance config, not an exec")
	}
}

func TestRemotesSaysWhyItCannotTell(t *testing.T) {
	for name, c := range map[string]struct {
		config map[string]string
		want   string
	}{
		"it has not reported":       {nil, "has not reported yet"},
		"an older helper":           {map[string]string{StatusKey: statusText(t, nil)}, "does not report its remotes"},
		"a document it cannot read": {map[string]string{StatusKey: "nonsense"}, "not a status document"},
	} {
		h := newHost()
		runningHelper(h, c.config)
		in, _ := installer(h, nil)
		if _, _, _, err := in.Remotes("", "", time.Now()); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestAStatusDocumentSaysWhetherItReportedRemotes(t *testing.T) {
	none, err := Parse(statusText(t, func(s *Status) { s.Remotes = []Remote{} }))
	if err != nil {
		t.Fatal(err)
	}
	if none.Remotes == nil || len(none.Remotes) != 0 {
		t.Errorf("an empty list survives a round trip as a statement that there are none: %#v", none.Remotes)
	}
	unsaid, err := Parse(statusText(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if unsaid.Remotes != nil {
		t.Errorf("a helper that did not say is not one that has none: %#v", unsaid.Remotes)
	}
	// a document an older helper wrote has no such key at all
	old, err := Parse(`{"proto":1,"tick":"2026-10-07T12:00:00Z"}`)
	if err != nil || old.Remotes != nil {
		t.Errorf("%#v %v", old.Remotes, err)
	}
}

func TestConfiguredRemotesAreTheOnesAConnectionCouldOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("INCUS_CONF", dir)
	conf := `remotes:
  host:
    addr: https://127.0.0.1:8443
    protocol: incus
    auth_type: tls
  nas2:
    addr: https://nas2.lan:8443
    protocol: incus
    auth_type: tls
    project: backups
  images:
    addr: https://images.example
    protocol: simplestreams
    public: true
`
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ConfiguredRemotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "host" || got[1].Name != "nas2" || got[1].Addr != "https://nas2.lan:8443" || got[1].Project != "backups" {
		t.Errorf("an image registry and the local socket are not somewhere a copy can go, and the list is sorted: %+v", got)
	}

	// no configuration at all is an empty list, not an error and not nil
	empty := t.TempDir()
	t.Setenv("INCUS_CONF", empty)
	got, err = ConfiguredRemotes()
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("%#v %v", got, err)
	}
}
