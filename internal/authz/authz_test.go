package authz

import (
	"strings"
	"testing"

	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

const (
	daemonFP = "3588cc5d7a1e4f2b9c0d6e8f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
	otherFP  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// decide runs the generated scriptlet the way Incus does: authorize(details, object, entitlement) with a flat `details`.
func decide(t *testing.T, script, fingerprint, protocol, object, entitlement string) bool {
	t.Helper()
	thread := &starlark.Thread{Name: "authorize"}
	globals, err := starlark.ExecFileOptions(&syntax.FileOptions{}, thread, "authz.star", script, nil)
	if err != nil {
		t.Fatalf("the generated script does not run: %v\n%s", err, script)
	}
	fn, ok := globals["authorize"]
	if !ok {
		t.Fatal("the generated script defines no authorize")
	}
	details := starlarkstruct.FromStringDict(starlarkstruct.Default, starlark.StringDict{
		"Username": starlark.String(fingerprint),
		"Protocol": starlark.String(protocol),
	})
	v, err := starlark.Call(thread, fn, starlark.Tuple{details, starlark.String(object), starlark.String(entitlement)}, nil)
	if err != nil {
		t.Fatalf("authorize(%q, %q): %v", object, entitlement, err)
	}
	b, ok := v.(starlark.Bool)
	if !ok {
		t.Fatalf("authorize returned %v, not a bool", v)
	}
	return bool(b)
}

func scope() Scope {
	return Scope{
		Status:    &Volume{"lab-authz", "default", "lrstatus"},
		Copies:    []Copy{{Source: Volume{"lab-authz", "default", "lvol"}, TargetPools: []string{"vm"}}},
		Reconcile: true,
		DNS:       []Instance{{"lab-recon-p", "dns"}},
	}
}

func daemonScript(t *testing.T) string {
	t.Helper()
	s, err := Generate([]Identity{{Name: "the daemon", Fingerprint: daemonFP, Rules: DaemonRules(scope())}}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTheDaemonIsAllowedExactlyWhatItWasMeasuredToNeed(t *testing.T) {
	s := daemonScript(t)
	for _, c := range [][2]string{
		{"server:incus", "can_view"},
		{"project:lab-authz", "can_view_events"},
		{"project:lab-recon-c", "can_view_events"},
		{"project:lab-authz", "can_view_operations"},
		{"project:lab-authz", "can_create_storage_volumes"},
		{"instance:lab-recon-c/whoami", "can_view"},
		{"profile:default/lr-base", "can_view"},
		{"network:default/lrtink0", "can_view"},
		{"network_acl:default/lrtink0-in", "can_view"},
		{"image:default/f17d529e", "can_view"},
		{"storage_pool:vm", "can_view"},
		{"storage_volume:lab-authz/vm/custom/lvol-bk-20261026-093102", "can_view"},
		{"storage_volume:lab-authz/default/custom/lrstatus", "can_edit"},
		{"storage_volume:lab-authz/default/custom/lvol", "can_edit"},
		{"storage_volume:lab-authz/default/custom/lvol", "can_manage_snapshots"},
		{"storage_volume:lab-authz/vm/custom/lvol-bk-20261026-093206", "can_edit"},
		{"instance:lab-recon-p/dns", "can_access_files"},
	} {
		if !decide(t, s, daemonFP, "tls", c[0], c[1]) {
			t.Errorf("the daemon is refused %s on %s, which it was measured to need", c[1], c[0])
		}
	}
}

func TestTheDaemonHasNoWayToTheHostAndNoRightOverAnythingItDoesNotTouch(t *testing.T) {
	s := daemonScript(t)
	for _, c := range [][2]string{
		// an instance: its devices are a way into the host, and exec runs code in it
		{"instance:lab-recon-p/dns", "can_edit"},
		{"instance:lab-recon-p/dns", "can_exec"},
		{"instance:lab-recon-p/dns", "can_update_state"},
		{"instance:lab-recon-p/dns", "can_delete"},
		{"project:lab-recon-p", "can_create_instances"},
		// files in an instance that is not a dns helper
		{"instance:lab-recon-c/whoami", "can_access_files"},
		{"instance:lab-recon-p/ingress", "can_access_files"},
		// the network, projects, the server and trust
		{"network:default/lrtink0", "can_edit"},
		{"network_acl:default/lrtink0-in", "can_edit"},
		{"project:lab-recon-p", "can_edit"},
		{"server:incus", "can_edit"},
		{"server:incus", "can_view_privileged_events"},
		{"server:incus", "can_create_certificates"},
		{"certificate:" + otherFP, "can_edit"},
		// volumes it does not copy, and a lookalike of a restore point
		{"storage_volume:lab-authz/default/custom/other", "can_edit"},
		{"storage_volume:lab-authz/default/custom/other", "can_manage_snapshots"},
		{"storage_volume:lab-authz/default/custom/lvol-bk-2", "can_edit"},
		{"storage_volume:lab-authz/vm/custom/other-bk-20261026", "can_edit"},
		{"storage_volume:lab-authz/default/custom/lvol", "can_delete"},
	} {
		if decide(t, s, daemonFP, "tls", c[0], c[1]) {
			t.Errorf("the daemon is allowed %s on %s", c[1], c[0])
		}
	}
}

func TestEveryOtherClientIsJudgedAsIncusJudgesItWithoutTheScript(t *testing.T) {
	s := daemonScript(t)
	for _, c := range []struct {
		protocol string
		want     bool
	}{{"tls", true}, {"oidc", true}, {"unix", false}, {"", false}} {
		if got := decide(t, s, otherFP, c.protocol, "instance:p/x", "can_edit"); got != c.want {
			t.Errorf("another %q client: allowed = %v, want %v", c.protocol, got, c.want)
		}
	}
}

func TestTheDaemonIsHeldToItsRulesWhateverTheProtocolSaysAndNeverFallsThrough(t *testing.T) {
	s := daemonScript(t)
	if decide(t, s, daemonFP, "oidc", "instance:lab-recon-p/dns", "can_edit") {
		t.Error("the daemon's fingerprint fell through to the generic rule")
	}
}

func TestWithoutTheReconcilerTheDaemonHasNoRightToReadInstancesOrWatchEvents(t *testing.T) {
	sc := scope()
	sc.Reconcile, sc.DNS = false, nil
	s, err := Generate([]Identity{{Name: "d", Fingerprint: daemonFP, Rules: DaemonRules(sc)}}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"instance:lab-recon-p/dns", "can_view"}, {"project:lab-authz", "can_view_events"}, {"instance:lab-recon-p/dns", "can_access_files"}} {
		if decide(t, s, daemonFP, "tls", c[0], c[1]) {
			t.Errorf("allowed %s on %s with the reconciler off", c[1], c[0])
		}
	}
	if !decide(t, s, daemonFP, "tls", "storage_volume:lab-authz/default/custom/lvol", "can_manage_snapshots") {
		t.Error("the backup rules went with the reconciler")
	}
}

func TestTheSameTableAlwaysGivesTheSameBytes(t *testing.T) {
	a := Identity{Name: "a", Fingerprint: daemonFP, Rules: DaemonRules(scope())}
	b := Identity{Name: "b", Fingerprint: otherFP, Rules: []Rule{{Object: "server:incus", Entitlement: "can_view"}}}
	rev := append([]Rule(nil), a.Rules...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	one, err := Generate([]Identity{a, b}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Generate([]Identity{b, {Name: "a", Fingerprint: daemonFP, Rules: append(rev, rev...)}}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	// the names differ only in the comment, so compare with the comment lines removed
	strip := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(l), "# a") && !strings.HasPrefix(strings.TrimSpace(l), "# b") {
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	if strip(one) != strip(two) {
		t.Errorf("the order or repeats of the rules changed the script:\n%s\n---\n%s", one, two)
	}
}

func TestAMinimalTableGeneratesExactlyThisText(t *testing.T) {
	got, err := Generate([]Identity{{Name: "the daemon", Fingerprint: otherFP, Rules: []Rule{
		{Object: "storage_volume:p/default/custom/db-bk-", Entitlement: "can_edit", Prefix: true},
		{Object: "server:incus", Entitlement: "can_view"},
	}}}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	want := Marker + `
# shape: incus-7.5
#
# Route it with: authorization.client.tls: scriptlet. It is run for unrestricted TLS clients only (the local Unix socket never reaches it).
# A fingerprint listed below is held to its rules and to nothing else. Every other client is judged as Incus's own routing judges it without
# this script: an unrestricted TLS client and an OIDC client are allowed, anything else is not.
IDENTITIES = {
    # the daemon
    "` + otherFP + `": {
        "exact": [
            ("server:incus", "can_view"),
        ],
        "prefix": [
            ("storage_volume:p/default/custom/db-bk-", "can_edit"),
        ],
    },
}

def authorize(details, object, entitlement):
    rules = IDENTITIES.get(details.Username)
    if rules == None:
        return details.Protocol == "tls" or details.Protocol == "oidc"
    for r in rules["exact"]:
        if object == r[0] and entitlement == r[1]:
            return True
    for r in rules["prefix"]:
        if object.startswith(r[0]) and entitlement == r[1]:
            return True
    return False
`
	if got != want {
		t.Errorf("--- got\n%s--- want\n%s", got, want)
	}
}

func TestGenerateRefusesATableThatCouldNotMeanWhatItSays(t *testing.T) {
	ok := []Rule{{Object: "server:incus", Entitlement: "can_view"}}
	for name, ids := range map[string][]Identity{
		"a short fingerprint":      {{Name: "x", Fingerprint: "abc", Rules: ok}},
		"an upper case one":        {{Name: "x", Fingerprint: strings.ToUpper(daemonFP), Rules: ok}},
		"one fingerprint twice":    {{Name: "x", Fingerprint: daemonFP, Rules: ok}, {Name: "y", Fingerprint: daemonFP, Rules: ok}},
		"no rules":                 {{Name: "x", Fingerprint: daemonFP}},
		"a quote in an object":     {{Name: "x", Fingerprint: daemonFP, Rules: []Rule{{Object: `instance:p/a"b`, Entitlement: "can_view"}}}},
		"a backslash in an object": {{Name: "x", Fingerprint: daemonFP, Rules: []Rule{{Object: `instance:p/a\b`, Entitlement: "can_view"}}}},
		"a newline in an object":   {{Name: "x", Fingerprint: daemonFP, Rules: []Rule{{Object: "instance:p/a\nb", Entitlement: "can_view"}}}},
		"a space in an object":     {{Name: "x", Fingerprint: daemonFP, Rules: []Rule{{Object: "instance:p/a b", Entitlement: "can_view"}}}},
		"an object with no type":   {{Name: "x", Fingerprint: daemonFP, Rules: []Rule{{Object: "p/a", Entitlement: "can_view"}}}},
		"an odd entitlement":       {{Name: "x", Fingerprint: daemonFP, Rules: []Rule{{Object: "server:incus", Entitlement: "view"}}}},
		"a name with a line break": {{Name: "x\ny", Fingerprint: daemonFP, Rules: ok}},
	} {
		if _, err := Generate(ids, Incus75); err == nil {
			t.Errorf("%s: Generate accepted it", name)
		}
	}
	if _, err := Generate([]Identity{{Name: "x", Fingerprint: daemonFP, Rules: ok}}, Shape{Name: "empty"}); err == nil {
		t.Error("a shape that says nowhere to find the fingerprint was accepted")
	}
}

func TestOnlyAScriptletWithTheMarkerIsOurs(t *testing.T) {
	s := daemonScript(t)
	if !IsOurs(s) {
		t.Error("a generated script is not recognised as ours")
	}
	for _, other := range []string{"", "def authorize(details, object, entitlement):\n    return True\n", " " + Marker} {
		if IsOurs(other) {
			t.Errorf("%q was taken for ours", other)
		}
	}
}

// derivedScript is the daemon that keeps the stamped derived files: no dns reconciler, files on the instances that carry a stamp, and a restart of the ones
// whose stamp says so.
func derivedScript(t *testing.T) string {
	t.Helper()
	sc := scope()
	sc.Reconcile, sc.DNS = false, nil
	sc.Derived = true
	sc.Files = []Instance{{"site", "dns"}, {"site", "ingress"}, {"site", "sso"}}
	sc.Restart = []Instance{{"site", "ingress"}, {"site", "sso"}}
	s, err := Generate([]Identity{{Name: "the daemon", Fingerprint: daemonFP, Rules: DaemonRules(sc)}}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTheDaemonThatKeepsDerivedFilesWritesThemOnStampedInstancesAndRestartsOnlyThoseThatSayso(t *testing.T) {
	s := derivedScript(t)
	for _, c := range [][2]string{
		{"instance:site/dns", "can_access_files"},
		{"instance:site/ingress", "can_access_files"},
		{"instance:site/sso", "can_access_files"},
		{"instance:site/ingress", "can_update_state"},
		{"instance:site/sso", "can_update_state"},
		{"instance:site/dns", "can_view"}, // it reads every instance to render the join
		{"project:site", "can_view_events"},
	} {
		if !decide(t, s, daemonFP, "tls", c[0], c[1]) {
			t.Errorf("the daemon is refused %s on %s", c[1], c[0])
		}
	}
	for _, c := range [][2]string{
		{"instance:site/dns", "can_update_state"},   // its stamp does not say restart
		{"instance:site/other", "can_access_files"}, // no stamp
		{"instance:site/other", "can_update_state"},
		{"instance:site/ingress", "can_edit"}, // an edit reaches the devices, which reach the host
		{"instance:site/ingress", "can_exec"},
		{"instance:site/ingress", "can_delete"},
		{"instance:site/sso", "can_connect_sftp"},
		{"project:site", "can_create_instances"},
		{"network_acl:default/site-in", "can_edit"},
	} {
		if decide(t, s, daemonFP, "tls", c[0], c[1]) {
			t.Errorf("the daemon is allowed %s on %s", c[1], c[0])
		}
	}
}

func TestTheDerivedRowsAreAskedForByTheirOwnFlagAndNotByTheDnsReconciler(t *testing.T) {
	sc := scope() // --reconcile-dns only
	sc.Files = []Instance{{"site", "ingress"}}
	sc.Restart = []Instance{{"site", "ingress"}}
	s, err := Generate([]Identity{{Name: "d", Fingerprint: daemonFP, Rules: DaemonRules(sc)}}, Incus75)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"instance:site/ingress", "can_access_files"}, {"instance:site/ingress", "can_update_state"}} {
		if decide(t, s, daemonFP, "tls", c[0], c[1]) {
			t.Errorf("allowed %s on %s without --reconcile-derived", c[1], c[0])
		}
	}
	// and the other way round: the derived daemon has no right on the dns helper that only --reconcile-dns grants
	d := derivedScript(t)
	if decide(t, d, daemonFP, "tls", "instance:lab-recon-p/dns", "can_access_files") {
		t.Error("--reconcile-derived grants the dns helper that --reconcile-dns names")
	}
}
