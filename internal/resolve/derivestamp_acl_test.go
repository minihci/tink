package resolve

import (
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

const aclStampKey = "user.tink.derive.ingress"
const aclStampValue = `{"render":"acl"}`

func derivingACL() Resource {
	return Resource{Kind: KindNetworkACL, Name: "svc-in", DeriveIngress: true}
}

func TestAnACLStampIsTheMarkerAndReadsBackStrictly(t *testing.T) {
	if got := EncodeACLDeriveStamp(); got != aclStampValue {
		t.Errorf("stamp = %s, want %s", got, aclStampValue)
	}
	d, err := DecodeACLDeriveStamp("svc-in", aclStampKey, aclStampValue)
	if err != nil || d != (DerivedACL{Name: "svc-in", Direction: "ingress", Render: "acl"}) {
		t.Errorf("decoded %+v, %v", d, err)
	}
	cases := []struct{ name, key, value, want string }{
		{"a direction ACLs do not derive", DeriveStampPrefix + "egress", aclStampValue, "not a direction"},
		{"a key outside the prefix", "user.tink.publish.x", aclStampValue, "not a derived stamp"},
		{"not an object", aclStampKey, `"acl"`, "not a JSON object"},
		{"an unknown field", aclStampKey, `{"render":"acl","path":"/a"}`, `"path" is not a field`},
		{"no render", aclStampKey, `{}`, `no "render"`},
		{"render twice", aclStampKey, `{"render":"acl","render":"acl"}`, "given twice"},
		{"text after", aclStampKey, `{"render":"acl"} x`, "more after"},
		{"render not a string", aclStampKey, `{"render":1}`, "render is not a string"},
		{"a render that is a file's", aclStampKey, `{"render":"caddy"}`, `is not "acl"`},
	}
	for _, c := range cases {
		if _, err := DecodeACLDeriveStamp("svc-in", c.key, c.value); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
}

func TestPlanStampsAnACLThatDerivesItsRulesAndNoOther(t *testing.T) {
	plain := Resource{Kind: KindNetworkACL, Name: "other-in", Config: map[string]string{"user.mine": "x"}}
	declared := derivingACL()
	declared.Config = map[string]string{"user.mine": "y"}
	levels, err := Levels([]Resource{declared, plain})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Resource{}
	for _, l := range levels {
		for _, r := range l {
			got[r.Name] = r
		}
	}
	if got["svc-in"].Config[aclStampKey] != aclStampValue || got["svc-in"].Config["user.mine"] != "y" {
		t.Errorf("the deriving ACL's config = %v", got["svc-in"].Config)
	}
	if _, ok := got["other-in"].Config[aclStampKey]; ok {
		t.Errorf("an ACL that does not derive is stamped: %v", got["other-in"].Config)
	}
	if _, ok := declared.Config[aclStampKey]; ok {
		t.Error("the caller's own config map was written to")
	}
}

func TestAnACLStampConvergesAndASecondPlanSaysNoChanges(t *testing.T) {
	r := derivingACL()
	levels, err := Levels([]Resource{r})
	if err != nil {
		t.Fatal(err)
	}
	want := levels[0][0]
	live := liveACL(nil, nil)
	s := &aclServer{acls: map[string]*api.NetworkACL{"svc-in": live}}

	p, err := planNetworkACL(s, want)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionUpdate || !strings.Contains(strings.Join(p.Changes, "\n"), "config."+aclStampKey) {
		t.Fatalf("a missing stamp is a planned change: %+v", p)
	}
	if err := updateOne(s, want, volumeEnv{}); err != nil {
		t.Fatal(err)
	}
	if s.acls["svc-in"].Config[aclStampKey] != aclStampValue {
		t.Errorf("config after apply = %v", s.acls["svc-in"].Config)
	}
	if p, err = planNetworkACL(s, want); err != nil || p.Action != ActionNone || len(p.Changes) != 0 {
		t.Errorf("a second plan: %+v, %v", p, err)
	}
}

func TestDroppingDeriveIngressRemovesTheStampAndNothingElse(t *testing.T) {
	r := Resource{Kind: KindNetworkACL, Name: "svc-in"} // no longer derives
	live := liveACL(held(ruleHA), nil)
	live.Config = api.ConfigMap{
		aclStampKey: aclStampValue, "user.tink.stack": "lab", "user.keep": "me", "user.tink.derive": "x", // the last two are not under the prefix
	}
	s := &aclServer{acls: map[string]*api.NetworkACL{"svc-in": live}}

	p, err := planNetworkACL(s, r)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Changes, "|"); got != `config.`+aclStampKey+`: "{\"render\":\"acl\"}" -> (unset)` {
		t.Errorf("changes = %s", got)
	}
	if err := updateOne(s, r, volumeEnv{}); err != nil {
		t.Fatal(err)
	}
	cfg := s.acls["svc-in"].Config
	if _, ok := cfg[aclStampKey]; ok {
		t.Errorf("the stamp is still there: %v", cfg)
	}
	if cfg["user.tink.stack"] != "lab" || cfg["user.keep"] != "me" || cfg["user.tink.derive"] != "x" {
		t.Errorf("something else was removed: %v", cfg)
	}
	if got := s.acls["svc-in"].Ingress; len(got) != 1 {
		t.Errorf("the rules are not the stamp's to touch: %+v", got)
	}
}

func TestAStackThatDeclaresAnACLStampKeyDoesNotLoad(t *testing.T) {
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, "kind: network-acl\nname: svc-in\nderive_ingress: true\nconfig:\n  user.tink.derive.ingress: '{}'\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "which tink owns") || !strings.Contains(err.Error(), "derive_ingress") {
		t.Errorf("err = %v", err)
	}
	// an ACL that does not derive anything may carry its own user.* keys, as before
	if err := writeFile(path, "kind: network-acl\nname: svc-in\nconfig:\n  user.mine: x\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Errorf("an ordinary key: %v", err)
	}
}

func TestCreatingAnACLThatDerivesStampsItFromBirth(t *testing.T) {
	levels, err := Levels([]Resource{derivingACL()})
	if err != nil {
		t.Fatal(err)
	}
	s := &aclServer{}
	if err := createNetworkACL(s, levels[0][0], "lab"); err != nil {
		t.Fatal(err)
	}
	cfg := s.created[0].Config
	if cfg[aclStampKey] != aclStampValue || cfg["user.tink.stack"] != "lab" {
		t.Errorf("created with %v", cfg)
	}
}
