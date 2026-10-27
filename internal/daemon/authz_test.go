package daemon

import (
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/authz"
	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/volbackup"
)

const fp = "3588cc5d7a1e4f2b9c0d6e8f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"

// authzServer is the part of a server the narrowing reads and writes: the trust store and the server's own config.
type authzServer struct {
	incus.InstanceServer
	certs []api.Certificate
	cfg   map[string]string
	puts  int
}

func (s *authzServer) GetCertificates() ([]api.Certificate, error) { return s.certs, nil }
func (s *authzServer) GetServer() (*api.Server, string, error) {
	cp := map[string]string{}
	for k, v := range s.cfg {
		cp[k] = v
	}
	srv := &api.Server{}
	srv.Config = cp
	return srv, "etag", nil
}
func (s *authzServer) UpdateServer(put api.ServerPut, _ string) error {
	s.puts++
	s.cfg = put.Config
	return nil
}

func daemonCert() api.Certificate {
	c := api.Certificate{Fingerprint: fp}
	c.Name = helper.TrustName
	return c
}

func opts(s *authzServer) AuthzOptions {
	return AuthzOptions{
		Server:    s,
		Reconcile: true,
		scope: func(incus.InstanceServer, AuthzOptions) (authz.Scope, []string, error) {
			return authz.Scope{
				Status:    &authz.Volume{Project: "tink-helper", Pool: "default", Name: "helper-status"},
				Copies:    []authz.Copy{{Source: authz.Volume{Project: "default", Pool: "default", Name: "lib"}, TargetPools: []string{"nas"}}},
				Reconcile: true,
				DNS:       []authz.Instance{{Project: "tink", Name: "dns"}},
			}, []string{"a note"}, nil
		},
	}
}

func TestThePlanNamesTheCertificateByItsNameAndBuildsTheScriptFromWhatTheServerHas(t *testing.T) {
	s := &authzServer{certs: []api.Certificate{daemonCert()}, cfg: map[string]string{}}
	p, err := PlanAuthz(opts(s))
	if err != nil {
		t.Fatal(err)
	}
	if p.Fingerprint != fp || p.Identity != helper.TrustName || p.Refusal != "" || p.UpToDate || len(p.Rules) == 0 {
		t.Errorf("plan = %+v", p)
	}
	if !strings.Contains(p.Script, fp) || !strings.HasPrefix(p.Script, authz.Marker) || !strings.Contains(p.Script, "storage_volume:tink-helper/default/custom/helper-status") {
		t.Errorf("the script does not say what the table says:\n%s", p.Script)
	}
	if s.puts != 0 {
		t.Error("a plan wrote to the server")
	}
}

func TestACertificateThatIsNotThereOrIsRestrictedIsAnErrorThatSaysWhat(t *testing.T) {
	if _, err := PlanAuthz(opts(&authzServer{cfg: map[string]string{}})); err == nil || !strings.Contains(err.Error(), "no certificate named") {
		t.Errorf("a missing certificate: %v", err)
	}
	r := daemonCert()
	r.Restricted = true
	if _, err := PlanAuthz(opts(&authzServer{certs: []api.Certificate{r}, cfg: map[string]string{}})); err == nil || !strings.Contains(err.Error(), "restricted") {
		t.Errorf("a restricted certificate: %v", err)
	}
}

func TestAScriptletThatIsNotTinksIsNeverReplaced(t *testing.T) {
	s := &authzServer{certs: []api.Certificate{daemonCert()}, cfg: map[string]string{AuthzScriptletKey: "def authorize(d, o, e):\n    return True\n"}}
	p, err := PlanAuthz(opts(s))
	if err != nil || p.Refusal == "" {
		t.Fatalf("plan = %+v, err %v: want a refusal", p, err)
	}
	if _, err := ApplyAuthz(opts(s)); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("apply: %v", err)
	}
	if s.puts != 0 {
		t.Error("a foreign scriptlet was overwritten")
	}
}

func TestARouteSomeoneElseChoseIsNeverChanged(t *testing.T) {
	s := &authzServer{certs: []api.Certificate{daemonCert()}, cfg: map[string]string{AuthzRouteKey: "openfga"}}
	if _, err := ApplyAuthz(opts(s)); err == nil || s.puts != 0 {
		t.Errorf("apply over a route to openfga: err %v, writes %d", err, s.puts)
	}
}

func TestApplyWritesTheScriptAndTheRouteInOneWriteAndASecondApplyWritesNothing(t *testing.T) {
	s := &authzServer{certs: []api.Certificate{daemonCert()}, cfg: map[string]string{"core.https_address": ":8443"}}
	p, err := ApplyAuthz(opts(s))
	if err != nil {
		t.Fatal(err)
	}
	if s.puts != 1 || s.cfg[AuthzScriptletKey] != p.Script || s.cfg[AuthzRouteKey] != "scriptlet" || s.cfg["core.https_address"] != ":8443" {
		t.Errorf("writes %d, config %v", s.puts, s.cfg)
	}
	again, err := ApplyAuthz(opts(s))
	if err != nil || !again.UpToDate || s.puts != 1 {
		t.Errorf("second apply: %+v, err %v, writes %d", again, err, s.puts)
	}
}

func TestRemoveTakesAwayTheScriptAndTheRouteTinkPutThereAndNothingElse(t *testing.T) {
	s := &authzServer{certs: []api.Certificate{daemonCert()}, cfg: map[string]string{"core.https_address": ":8443"}}
	if _, err := ApplyAuthz(opts(s)); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveAuthz(s)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if _, ok := s.cfg[AuthzScriptletKey]; ok {
		t.Error("the script is still there")
	}
	if _, ok := s.cfg[AuthzRouteKey]; ok {
		t.Error("the route is still there")
	}
	if s.cfg["core.https_address"] != ":8443" {
		t.Errorf("another key was touched: %v", s.cfg)
	}
	if removed, err := RemoveAuthz(s); err != nil || removed {
		t.Errorf("removing nothing = %v, %v", removed, err)
	}
}

func TestRemoveLeavesAScriptletThatIsNotTinks(t *testing.T) {
	s := &authzServer{cfg: map[string]string{AuthzScriptletKey: "def authorize(d, o, e):\n    return True\n", AuthzRouteKey: "scriptlet"}}
	removed, err := RemoveAuthz(s)
	if err == nil || removed || s.puts != 0 {
		t.Errorf("remove = %v, %v, writes %d: a foreign scriptlet must be left", removed, err, s.puts)
	}
}

func TestCopiesAreWhatTheServerIsAskedForAndRemoteTargetsCreateNothingHere(t *testing.T) {
	items := []backuprun.Item{
		{Volume: volbackup.Volume{Name: "lib"}, Copies: []backuprun.Copy{
			{Target: volbackup.Target{Name: "nas", Pool: "nas"}},
			{Target: volbackup.Target{Name: "nas2", Pool: "nas"}},
			{Target: volbackup.Target{Name: "far", Remote: "vps"}},
			{Target: volbackup.Target{Name: "both", Pool: "fast", Remote: "vps"}},
		}},
		{Volume: volbackup.Volume{Project: "photos", Pool: "ssd", Name: "db"}, Copies: []backuprun.Copy{{Target: volbackup.Target{Name: "t", Pool: "vm"}}}},
	}
	got := copiesOf(items)
	if len(got) != 2 {
		t.Fatalf("copies = %+v", got)
	}
	if got[0].Source != (authz.Volume{Project: "default", Pool: "default", Name: "lib"}) || len(got[0].TargetPools) != 1 || got[0].TargetPools[0] != "nas" {
		t.Errorf("the default project and pool, one local target pool, none for the remote ones: %+v", got[0])
	}
	if got[1].Source != (authz.Volume{Project: "photos", Pool: "ssd", Name: "db"}) || got[1].TargetPools[0] != "vm" {
		t.Errorf("explicit project and pool: %+v", got[1])
	}
}

func TestAHelperDeclaredInAStackIsNarrowedForWhatItIsOnTheServer(t *testing.T) {
	found := helper.Found{Project: "tink-helper", Name: "helper", StatusVolume: "fast/helper-status",
		Config: map[string]string{"oci.entrypoint": "/usr/bin/tink daemon run --remote host --reconcile-dns"}}
	o, err := (HelperAuthz{}).optionsFor(&authzServer{}, found)
	if err != nil {
		t.Fatal(err)
	}
	if !o.Reconcile || o.Status == nil || *o.Status != (authz.Volume{Project: "tink-helper", Pool: "fast", Name: "helper-status"}) {
		t.Errorf("options = %+v, status %+v", o, o.Status)
	}
	// a daemon that does not reconcile, and one that has no status volume: the rules are the narrower for it
	found.Config["oci.entrypoint"] = "/usr/bin/tink daemon run --remote host"
	found.StatusVolume = ""
	if o, _ = (HelperAuthz{}).optionsFor(&authzServer{}, found); o.Reconcile || o.Status != nil {
		t.Errorf("options = %+v", o)
	}
	// a flag that only looks like it is not the flag
	found.Config["oci.entrypoint"] = "/usr/bin/tink daemon run --reconcile-dns-extra"
	if o, _ = (HelperAuthz{}).optionsFor(&authzServer{}, found); o.Reconcile {
		t.Error("--reconcile-dns-extra is not --reconcile-dns")
	}
	found.StatusVolume = "no-slash"
	if _, err := (HelperAuthz{}).optionsFor(&authzServer{}, found); err == nil {
		t.Error("a status volume that is not POOL/VOLUME must be an error, not a guess")
	}
}

func TestNarrowedMeansTinksScriptIsRoutedWhetherOrNotItIsCurrent(t *testing.T) {
	s := &authzServer{certs: []api.Certificate{daemonCert()}, cfg: map[string]string{}}
	p, _ := PlanAuthz(opts(s))
	if p.Narrowed() {
		t.Error("an empty server is not narrowed")
	}
	if _, err := ApplyAuthz(opts(s)); err != nil {
		t.Fatal(err)
	}
	if p, _ = PlanAuthz(opts(s)); !p.Narrowed() || !p.UpToDate {
		t.Errorf("after apply: %+v", p)
	}
	s.cfg[AuthzScriptletKey] = authz.Marker + "\n# an older script\n"
	if p, _ = PlanAuthz(opts(s)); !p.Narrowed() || p.UpToDate {
		t.Errorf("an older script of tink's is narrowed and out of date: narrowed=%v up to date=%v", p.Narrowed(), p.UpToDate)
	}
	delete(s.cfg, AuthzRouteKey)
	if p, _ = PlanAuthz(opts(s)); p.Narrowed() {
		t.Error("a script nothing routes to narrows nobody")
	}
}
