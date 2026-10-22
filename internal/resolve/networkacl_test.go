package resolve

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// aclServer is a fake Incus server for network ACLs, with the network calls of netServer beside it. It REMEMBERS: a create or update changes what the
// next read returns, so a test can run plan, apply, plan again, the way two runs against a real server go. Like the real server it refuses a rule
// with no state or an unknown action, which is what proves tink sends a valid rule. Methods an ACL has no business calling panic (nil embedded
// interface), and every write is recorded.
type aclServer struct {
	netServer
	acls    map[string]*api.NetworkACL // live ACLs; a name not here answers 404
	err     error                      // answers every ACL read
	reads   []string
	created []api.NetworkACLsPost
	updated []api.NetworkACLPut
	etags   []string
}

func (s *aclServer) GetNetworkACL(name string) (*api.NetworkACL, string, error) {
	s.reads = append(s.reads, name)
	if s.err != nil {
		return nil, "", s.err
	}
	live, ok := s.acls[name]
	if !ok {
		return nil, "", errNotFound
	}
	cp := *live // a read hands back a copy, as a round trip through JSON would
	return &cp, "etag-acl", nil
}

func validRulesOrError(put api.NetworkACLPut) error {
	for _, dir := range []struct {
		name  string
		rules []api.NetworkACLRule
	}{{"ingress", put.Ingress}, {"egress", put.Egress}} {
		for i, r := range dir.rules {
			if !slices.Contains(aclActions, r.Action) {
				return fmt.Errorf("Invalid %s rule %d: Action must be one of: %v", dir.name, i, aclActions)
			}
			if !slices.Contains(aclStates, r.State) {
				return fmt.Errorf("Invalid %s rule %d: State must be one of: %v", dir.name, i, aclStates)
			}
		}
	}
	return nil
}

func (s *aclServer) CreateNetworkACL(p api.NetworkACLsPost) error {
	if err := validRulesOrError(p.NetworkACLPut); err != nil {
		return err
	}
	s.created = append(s.created, p)
	if s.acls == nil {
		s.acls = map[string]*api.NetworkACL{}
	}
	s.acls[p.Name] = &api.NetworkACL{NetworkACLPost: p.NetworkACLPost, NetworkACLPut: p.NetworkACLPut}
	return nil
}

func (s *aclServer) UpdateNetworkACL(name string, put api.NetworkACLPut, etag string) error {
	if err := validRulesOrError(put); err != nil {
		return err
	}
	s.updated = append(s.updated, put)
	s.etags = append(s.etags, etag)
	s.acls[name].NetworkACLPut = put
	return nil
}

// Rules of the lab recipe and a few more, written the way a stack would write them (no state), and the way Incus holds them (state enabled).
var (
	ruleHA    = api.NetworkACLRule{Action: "allow", Source: "198.51.100.221/32", Protocol: "tcp", DestinationPort: "8123"}
	ruleMQTT  = api.NetworkACLRule{Action: "allow", Source: "198.51.100.5/32", Protocol: "tcp", DestinationPort: "1883"}
	ruleDNS   = api.NetworkACLRule{Action: "allow", Protocol: "udp", DestinationPort: "53", Description: "name lookups"}
	ruleBlock = api.NetworkACLRule{Action: "reject", Source: "10.0.0.0/8"}
)

// held is how Incus holds a rule declared without a state.
func held(rules ...api.NetworkACLRule) []api.NetworkACLRule {
	out := make([]api.NetworkACLRule, len(rules))
	for i, r := range rules {
		r.State = "enabled"
		out[i] = r
	}
	return out
}

func liveACL(ingress, egress []api.NetworkACLRule) *api.NetworkACL {
	return &api.NetworkACL{
		NetworkACLPost: api.NetworkACLPost{Name: "svc-in"},
		NetworkACLPut:  api.NetworkACLPut{Description: "", Ingress: ingress, Egress: egress},
	}
}

func aclResource(ingress []api.NetworkACLRule) Resource {
	return Resource{Kind: KindNetworkACL, Name: "svc-in", Ingress: ingress}
}

func TestLoadFile_NetworkACL(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, `
kind: network-acl
name: svclab0-in
description: only the ingress helper may reach the apps
ingress:
  - action: allow
    source: 198.51.100.221/32
    protocol: tcp
    destination_port: 8123
    description: the ingress helper to Home Assistant
    state: enabled
  - action: reject
egress:
  - action: allow
    protocol: icmp4
    icmp_type: 8
config:
  user.owner: smarthome
`))
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if len(rs) != 1 {
		t.Fatalf("got %d resources, want 1", len(rs))
	}
	r := rs[0]
	if r.Kind != KindNetworkACL || r.Name != "svclab0-in" || r.Description != "only the ingress helper may reach the apps" || r.Config["user.owner"] != "smarthome" {
		t.Errorf("resource = %+v", r)
	}
	if len(r.Ingress) != 2 || len(r.Egress) != 1 {
		t.Fatalf("rules = %d ingress, %d egress; want 2 and 1", len(r.Ingress), len(r.Egress))
	}
	want := api.NetworkACLRule{Action: "allow", Source: "198.51.100.221/32", Protocol: "tcp", DestinationPort: "8123", Description: "the ingress helper to Home Assistant", State: "enabled"}
	if r.Ingress[0] != want {
		t.Errorf("ingress[0] = %+v, want %+v (a port written as a bare number must load as the string Incus takes)", r.Ingress[0], want)
	}
	if r.Egress[0].ICMPType != "8" {
		t.Errorf("icmp_type = %q, want \"8\"", r.Egress[0].ICMPType)
	}
}

func TestLoadFile_NetworkACLKeepsTheOrderItWasWritten(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, "kind: network-acl\nname: a\ningress:\n  - {action: reject}\n  - {action: allow, protocol: tcp}\n  - {action: drop, protocol: udp}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs[0].Ingress {
		got = append(got, r.Action)
	}
	if want := []string{"reject", "allow", "drop"}; !reflect.DeepEqual(got, want) {
		t.Errorf("actions = %v, want %v", got, want)
	}
}

func TestLoadFile_NetworkACLRejectedForms(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"project", "kind: network-acl\nname: a\nproject: ha-eval\n", "server-global"},
		{"name with an underscore", "kind: network-acl\nname: svc_in\n", "letters, digits"},
		{"name too long", "kind: network-acl\nname: " + strings.Repeat("a", 64) + "\n", "1 to 63"},
		{"name starts with a hyphen", "kind: network-acl\nname: -a\n", "start or end"},
		{"name is a number", "kind: network-acl\nname: \"123\"\n", "cannot be a number"},
		{"name starts with an at sign", "kind: network-acl\nname: \"@internal\"\n", "letters, digits"},
		{"action not Incus's", "kind: network-acl\nname: a\ningress:\n  - {action: permit}\n", `ingress rule 0: action "permit" is not one of: allow, allow-stateless, drop, reject`},
		{"action missing, second rule, named by index", "kind: network-acl\nname: a\ningress:\n  - {action: allow}\n  - {protocol: tcp}\n", `ingress rule 1: action ""`},
		{"action wrong in egress", "kind: network-acl\nname: a\negress:\n  - {action: Allow}\n", `egress rule 0: action "Allow"`},
		{"state not Incus's", "kind: network-acl\nname: a\ningress:\n  - {action: allow, state: on}\n", `ingress rule 0: state "on" is not one of: enabled, disabled, logged`},
		{"protocol icmp is not Incus's", "kind: network-acl\nname: a\ningress:\n  - {action: allow, protocol: icmp}\n", `protocol "icmp" is not one of: icmp4, icmp6, tcp, udp, or left out for any (icmp4 for IPv4, icmp6 for IPv6)`},
		{"protocol in capitals", "kind: network-acl\nname: a\ningress:\n  - {action: allow, protocol: TCP}\n", `protocol "TCP"`},
		{"a port with no protocol", "kind: network-acl\nname: a\ningress:\n  - {action: allow, destination_port: 80}\n", "need protocol tcp or udp"},
		{"a port with icmp", "kind: network-acl\nname: a\ningress:\n  - {action: allow, protocol: icmp4, source_port: 80}\n", "need protocol tcp or udp"},
		{"icmp_type with tcp", "kind: network-acl\nname: a\ningress:\n  - {action: allow, protocol: tcp, icmp_type: 8}\n", "need protocol icmp4 or icmp6"},
		{"the same rule twice", "kind: network-acl\nname: a\ningress:\n  - {action: allow, protocol: tcp}\n  - {action: allow, protocol: tcp}\n", "ingress rule 1: repeats rule 0"},
		{"the same rule twice, one spelling the state out", "kind: network-acl\nname: a\ningress:\n  - {action: allow}\n  - {action: allow, state: enabled}\n", "repeats rule 0"},
		{"unknown rule field", "kind: network-acl\nname: a\ningress:\n  - {action: allow, port: 80}\n", "not found in type"},
		{"unknown resource field", "kind: network-acl\nname: a\nrules: []\n", "not found in type"},
		{"derive_ingress with rules written", "kind: network-acl\nname: a\nderive_ingress: true\ningress: []\n", "exclusive"},
		{"derive_ingress on a network", "kind: network\nname: a\nderive_ingress: true\n", "derive_ingress"},
		{"type is not a field", "kind: network-acl\nname: a\ntype: bridge\n", "not found in type"},
		{"allow_disruption is the network's", "kind: network-acl\nname: a\nallow_disruption: true\n", `does not use field "AllowDisruption"`},
		{"pool is not an ACL field", "kind: network-acl\nname: a\npool: default\n", `does not use field "Pool"`},
		{"ingress on another kind", "kind: network\nname: svc\ningress:\n  - {action: allow}\n", `does not use field "Ingress"`},
		{"egress on a profile", "kind: profile\nname: p\negress:\n  - {action: allow}\n", `does not use field "Egress"`},
		{"a secret in a rule", "kind: network-acl\nname: a\ningress:\n  - {action: allow, description: \"${secret:x}\"}\n", "secret reference is not allowed"},
		{"a secret in config", "kind: network-acl\nname: a\nconfig:\n  user.k: ${secret:x}\n", "secret reference is not allowed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := LoadFile(writeTemp(t, c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestLoadFile_NetworkACLErrorsNameTheResourceAndTheRule(t *testing.T) {
	_, err := LoadFile(writeTemp(t, "kind: network-acl\nname: svclab0-in\ningress:\n  - {action: allow}\n  - {action: allow}\n  - {action: nope}\n"))
	if err == nil || !strings.Contains(err.Error(), `resource "svclab0-in": ingress rule 1: repeats rule 0`) {
		// rule 1 is the first fault the walk meets, not rule 2: the message must name both the resource and the index
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFile_NetworkACLAcceptsWhatIncusAccepts(t *testing.T) {
	for _, yaml := range []string{
		"kind: network-acl\nname: a\n", // no rules at all
		"kind: network-acl\nname: a\ningress: []\n",
		"kind: network-acl\nname: " + strings.Repeat("a", 63) + "\n",
		"kind: network-acl\nname: a-1\ningress:\n  - {action: allow-stateless, state: logged}\n  - {action: drop, state: disabled, source: \"10.0.0.1/32, 10.0.0.2/32\"}\n",
		"kind: network-acl\nname: a\negress:\n  - {action: reject, protocol: icmp6, icmp_type: 128, icmp_code: 0}\n  - {action: allow, protocol: udp, source_port: \"1000-2000\", destination_port: \"53,5353\"}\n",
		"kind: network-acl\nname: a\ningress:\n  - {action: \" allow \", protocol: \" tcp\"}\n", // Incus trims these
	} {
		if _, err := LoadFile(writeTemp(t, yaml)); err != nil {
			t.Errorf("LoadFile(%q) error = %v", yaml, err)
		}
	}
}

func TestLevels_ACLWaitsNotForAnythingButIsWaitedFor(t *testing.T) {
	cases := []struct {
		name      string
		resources []Resource
		wantLevel map[string]int
	}{
		{
			"a network that attaches the ACL",
			[]Resource{
				{Kind: KindNetwork, Name: "svclab0", Config: map[string]string{"security.acls": "svc-in"}},
				{Kind: KindNetworkACL, Name: "svc-in"},
			},
			map[string]int{"svc-in": 0, "svclab0": 1},
		},
		{
			"a network that attaches two, one of them declared, with spaces in the list",
			[]Resource{
				{Kind: KindNetwork, Name: "svclab0", Config: map[string]string{"security.acls": "svc-in , already-on-the-server"}},
				{Kind: KindNetworkACL, Name: "svc-in"},
			},
			map[string]int{"svc-in": 0, "svclab0": 1},
		},
		{
			"an instance NIC that attaches the ACL",
			[]Resource{
				{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0", "security.acls": "svc-in"}}},
				{Kind: KindNetworkACL, Name: "svc-in"},
			},
			map[string]int{"svc-in": 0, "ha": 1},
		},
		{
			"a profile NIC that attaches the ACL, and the instance on that profile",
			[]Resource{
				{Kind: KindInstance, Name: "ha", Profiles: []string{"base"}},
				{Kind: KindProfile, Name: "base", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0", "security.acls": "svc-in"}}},
				{Kind: KindNetworkACL, Name: "svc-in"},
			},
			map[string]int{"svc-in": 0, "base": 1, "ha": 2},
		},
		{
			"the whole recipe: ACL, then the bridge that attaches it, then an instance on the bridge",
			[]Resource{
				{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "svclab0"}}},
				{Kind: KindNetwork, Name: "svclab0", Config: map[string]string{"security.acls": "svc-in"}},
				{Kind: KindNetworkACL, Name: "svc-in"},
			},
			map[string]int{"svc-in": 0, "svclab0": 1, "ha": 2},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			levels, err := Levels(c.resources)
			if err != nil {
				t.Fatalf("Levels() error = %v", err)
			}
			for name, level := range c.wantLevel {
				if !inLevel(levels, level, name) {
					t.Errorf("want %s in level %d, got %v", name, level, levels)
				}
			}
		})
	}
}

func TestLevels_ACLTheStackDoesNotDeclareStaysLenient(t *testing.T) {
	levels, err := Levels([]Resource{
		{Kind: KindNetwork, Name: "svclab0", Config: map[string]string{"security.acls": "made-by-hand"}},
		{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0", "security.acls": "made-by-hand"}}},
	})
	if err != nil {
		t.Fatalf("an ACL that exists on the server already is normal, got error %v", err)
	}
	if !inLevel(levels, 0, "svclab0") || !inLevel(levels, 0, "ha") {
		t.Errorf("want both in level 0, got %v", levels)
	}
}

func TestLevels_ACLNameOfAnotherKindIsNoEdge(t *testing.T) {
	// "base" is a profile here, not an ACL: naming it in security.acls must not make the network wait for the profile.
	levels, err := Levels([]Resource{
		{Kind: KindNetwork, Name: "svclab0", Config: map[string]string{"security.acls": "base"}},
		{Kind: KindProfile, Name: "base"},
	})
	if err != nil {
		t.Fatalf("Levels() error = %v", err)
	}
	if !inLevel(levels, 0, "svclab0") || !inLevel(levels, 0, "base") {
		t.Errorf("want both in level 0, got %v", levels)
	}
}

func TestLevels_ACLOnlyNICDevicesAttach(t *testing.T) {
	// security.acls is a NIC key; a disk device with the same word in it is not an attachment.
	levels, err := Levels([]Resource{
		{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"data": {"type": "disk", "security.acls": "svc-in"}}},
		{Kind: KindNetworkACL, Name: "svc-in"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !inLevel(levels, 0, "ha") {
		t.Errorf("want the instance in level 0, got %v", levels)
	}
}

func TestStackProject_NetworkACLTakesNoDefaultProject(t *testing.T) {
	resources := []Resource{
		{Kind: KindStack, Name: "smarthome", Project: "smarthome"},
		{Kind: KindNetworkACL, Name: "svc-in"},
		{Kind: KindNetwork, Name: "svclab0"},
		{Kind: KindProfile, Name: "base"},
	}
	applyStackProject(resources)
	for _, r := range resources {
		want := ""
		if r.Kind == KindProfile {
			want = "smarthome"
		}
		if r.Kind != KindStack && r.Project != want {
			t.Errorf("%s/%s project = %q, want %q: an ACL, like a network, is server-global and takes no default project", r.Kind, r.Name, r.Project, want)
		}
	}
	if notes := mixedProjectNotes(resources); notes["svc-in"] != "" {
		t.Errorf("an ACL names no project on purpose and must not be warned about it: %q", notes["svc-in"])
	}
}

func TestScopedServer_NetworkACLIsNeverScoped(t *testing.T) {
	// The embedded interface is nil, so UseProject would panic: an ACL goes to the default project whatever Project says.
	s := &aclServer{}
	if got := scopedServer(s, Resource{Kind: KindNetworkACL, Name: "a", Project: "p"}); got != incus.InstanceServer(s) {
		t.Errorf("scopedServer scoped a network ACL to a project")
	}
}

func TestValidate_NetworkACLNeedsNoRules(t *testing.T) {
	if err := Validate(Resource{Kind: KindNetworkACL, Name: "a"}); err != nil {
		t.Errorf("an ACL with no rules is legal (Incus then applies only its defaults): %v", err)
	}
}

func TestPlanNetworkACL(t *testing.T) {
	cases := []struct {
		name       string
		live       *api.NetworkACL // nil: absent
		declared   Resource
		wantAction Action
		wantChange []string
	}{
		{"absent creates", nil, aclResource([]api.NetworkACLRule{ruleHA}), ActionCreate, nil},
		{
			"the same rules read no changes, though the stack left the state out and Incus filled it in",
			liveACL(held(ruleHA, ruleMQTT), nil), aclResource([]api.NetworkACLRule{ruleHA, ruleMQTT}), ActionNone, nil,
		},
		{
			"a state spelled out is the same as the default",
			liveACL(held(ruleHA), nil), aclResource(held(ruleHA)), ActionNone, nil,
		},
		{
			"spaces Incus trims are not a difference",
			liveACL(held(api.NetworkACLRule{Action: "allow", Source: "10.0.0.1/32,10.0.0.2/32"}), nil),
			aclResource([]api.NetworkACLRule{{Action: " allow", Source: "10.0.0.1/32, 10.0.0.2/32"}}), ActionNone, nil,
		},
		{
			"a rule added at the end",
			liveACL(held(ruleHA), nil), aclResource([]api.NetworkACLRule{ruleHA, ruleMQTT}), ActionUpdate,
			[]string{"ingress rule 1: add (action=allow source=198.51.100.5/32 protocol=tcp destination_port=1883 state=enabled)"},
		},
		{
			"a rule added in the middle is one add, not a change to every rule after it",
			liveACL(held(ruleHA, ruleBlock), nil), aclResource([]api.NetworkACLRule{ruleHA, ruleMQTT, ruleBlock}), ActionUpdate,
			[]string{"ingress rule 1: add (action=allow source=198.51.100.5/32 protocol=tcp destination_port=1883 state=enabled)"},
		},
		{
			"a rule removed",
			liveACL(held(ruleHA, ruleMQTT), nil), aclResource([]api.NetworkACLRule{ruleHA}), ActionUpdate,
			[]string{"ingress rule 1 (in the live list): remove (action=allow source=198.51.100.5/32 protocol=tcp destination_port=1883 state=enabled)"},
		},
		{
			"a rule changed names the field",
			liveACL(held(ruleHA), nil),
			aclResource([]api.NetworkACLRule{{Action: "allow", Source: "198.51.100.221/32", Protocol: "tcp", DestinationPort: "8080"}}), ActionUpdate,
			[]string{`ingress rule 0: destination_port: "8123" -> "8080"`},
		},
		{
			"a rule changed in several fields names each",
			liveACL(held(ruleHA), nil),
			aclResource([]api.NetworkACLRule{{Action: "reject", Source: "198.51.100.221/32", Protocol: "tcp", DestinationPort: "8123", State: "logged"}}), ActionUpdate,
			[]string{`ingress rule 0: action: "allow" -> "reject"`, `ingress rule 0: state: "enabled" -> "logged"`},
		},
		{
			"the second of three rules changed leaves the others out of the diff",
			liveACL(held(ruleHA, ruleMQTT, ruleBlock), nil),
			aclResource([]api.NetworkACLRule{ruleHA, {Action: "allow", Source: "198.51.100.6/32", Protocol: "tcp", DestinationPort: "1883"}, ruleBlock}), ActionUpdate,
			[]string{`ingress rule 1: source: "198.51.100.5/32" -> "198.51.100.6/32"`},
		},
		{
			"the same rules reordered is an update, reported as a reorder",
			liveACL(held(ruleHA, ruleBlock), nil), aclResource([]api.NetworkACLRule{ruleBlock, ruleHA}), ActionUpdate,
			[]string{"ingress: rules reordered (the same 2; Incus applies drop, then reject, then allow whatever the order, so filtering is unchanged): " +
				"[action=allow source=198.51.100.221/32 protocol=tcp destination_port=8123 state=enabled] [action=reject source=10.0.0.0/8 state=enabled] -> " +
				"[action=reject source=10.0.0.0/8 state=enabled] [action=allow source=198.51.100.221/32 protocol=tcp destination_port=8123 state=enabled]"},
		},
		{
			"egress is its own list",
			liveACL(held(ruleHA), held(ruleDNS)),
			Resource{Kind: KindNetworkACL, Name: "svc-in", Ingress: []api.NetworkACLRule{ruleHA}, Egress: []api.NetworkACLRule{ruleDNS, ruleBlock}}, ActionUpdate,
			[]string{"egress rule 1: add (action=reject source=10.0.0.0/8 state=enabled)"},
		},
		{
			"a direction the stack leaves out is not managed",
			liveACL(held(ruleHA), held(ruleDNS)),
			Resource{Kind: KindNetworkACL, Name: "svc-in", Ingress: []api.NetworkACLRule{ruleHA}}, ActionNone, nil,
		},
		{
			"ingress left out is not managed either",
			liveACL(held(ruleHA), held(ruleDNS)),
			Resource{Kind: KindNetworkACL, Name: "svc-in", Egress: []api.NetworkACLRule{ruleDNS}}, ActionNone, nil,
		},
		{
			"a direction declared empty is managed, and the live rules in it are removed",
			liveACL(held(ruleHA), held(ruleDNS)),
			Resource{Kind: KindNetworkACL, Name: "svc-in", Ingress: []api.NetworkACLRule{ruleHA}, Egress: []api.NetworkACLRule{}}, ActionUpdate,
			[]string{"egress rule 0 (in the live list): remove (action=allow protocol=udp destination_port=53 description=\"name lookups\" state=enabled)"},
		},
		{
			"a description changed",
			&api.NetworkACL{NetworkACLPut: api.NetworkACLPut{Description: "old", Ingress: held(ruleHA)}},
			Resource{Kind: KindNetworkACL, Name: "svc-in", Description: "only the ingress helper", Ingress: []api.NetworkACLRule{ruleHA}}, ActionUpdate,
			[]string{`description: "old" -> "only the ingress helper"`},
		},
		{
			"a description the stack does not state is left alone",
			&api.NetworkACL{NetworkACLPut: api.NetworkACLPut{Description: "made by hand", Ingress: held(ruleHA)}},
			aclResource([]api.NetworkACLRule{ruleHA}), ActionNone, nil,
		},
		{
			"a config key differs",
			&api.NetworkACL{NetworkACLPut: api.NetworkACLPut{Ingress: held(ruleHA), Config: api.ConfigMap{"user.owner": "someone"}}},
			Resource{Kind: KindNetworkACL, Name: "svc-in", Ingress: []api.NetworkACLRule{ruleHA}, Config: map[string]string{"user.owner": "smarthome"}}, ActionUpdate,
			[]string{`config.user.owner: "someone" -> "smarthome"`},
		},
		{
			"a config key the stack does not declare is not drift",
			&api.NetworkACL{NetworkACLPut: api.NetworkACLPut{Ingress: held(ruleHA), Config: api.ConfigMap{"user.owner": "someone"}}},
			aclResource([]api.NetworkACLRule{ruleHA}), ActionNone, nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &aclServer{}
			if c.live != nil {
				s.acls = map[string]*api.NetworkACL{"svc-in": c.live}
			}
			p, err := planNetworkACL(s, c.declared)
			if err != nil {
				t.Fatalf("planNetworkACL() error = %v", err)
			}
			if p.Action != c.wantAction {
				t.Fatalf("action = %v (%v), want %v", p.Action, p.Changes, c.wantAction)
			}
			if !reflect.DeepEqual(p.Changes, c.wantChange) {
				t.Errorf("changes =\n  %q\nwant\n  %q", p.Changes, c.wantChange)
			}
			if len(p.Blocked) != 0 || len(p.Warnings) != 0 {
				t.Errorf("an ACL plan has nothing to block or warn about: blocked %v warnings %v", p.Blocked, p.Warnings)
			}
		})
	}
}

func TestPlanNetworkACL_ForbiddenIsNotAbsent(t *testing.T) {
	p, err := planNetworkACL(&aclServer{err: errForbidden}, aclResource(nil))
	if err == nil {
		t.Fatalf("a 403 must be an error, never read as 'the ACL is absent' (got action %v)", p.Action)
	}
}

func TestPlanNetworkACL_TwoRunsAgree(t *testing.T) {
	// The same plan twice against the same server says the same thing: reading an ACL changes nothing.
	s := &aclServer{acls: map[string]*api.NetworkACL{"svc-in": liveACL(held(ruleHA), nil)}}
	r := aclResource([]api.NetworkACLRule{ruleHA, ruleMQTT})
	first, _ := planNetworkACL(s, r)
	second, _ := planNetworkACL(s, r)
	if !reflect.DeepEqual(first, second) {
		t.Errorf("plans differ between runs:\n%+v\n%+v", first, second)
	}
}

func TestCreateOne_NetworkACLCreatesWithTheRulesInOrderAndTheStateFilledIn(t *testing.T) {
	s := &aclServer{}
	r := aclResource([]api.NetworkACLRule{ruleBlock, ruleHA})
	r.Description = "only the ingress helper"
	r.Egress = []api.NetworkACLRule{ruleDNS}
	r.Config = map[string]string{"user.owner": "smarthome"}
	if err := createOne(s, r, volumeEnv{}); err != nil {
		t.Fatalf("createOne() error = %v (the fake refuses a rule with no state, as Incus does)", err)
	}
	if len(s.created) != 1 {
		t.Fatalf("CreateNetworkACL called %d times, want 1", len(s.created))
	}
	got := s.created[0]
	if got.Name != "svc-in" || got.Description != "only the ingress helper" || got.Config["user.owner"] != "smarthome" {
		t.Errorf("CreateNetworkACL got %+v", got)
	}
	if want := held(ruleBlock, ruleHA); !reflect.DeepEqual(got.Ingress, want) {
		t.Errorf("ingress = %+v, want %+v (declared order, state enabled)", got.Ingress, want)
	}
	if want := held(ruleDNS); !reflect.DeepEqual(got.Egress, want) {
		t.Errorf("egress = %+v, want %+v", got.Egress, want)
	}
}

func TestUpdateOne_NetworkACLReplacesDeclaredListsAndKeepsTheRest(t *testing.T) {
	live := liveACL(held(ruleHA, ruleMQTT), held(ruleDNS))
	live.Description = "made by hand"
	live.Config = api.ConfigMap{"user.keep": "me"}
	s := &aclServer{acls: map[string]*api.NetworkACL{"svc-in": live}}

	r := aclResource([]api.NetworkACLRule{ruleHA}) // egress not declared
	r.Config = map[string]string{"user.owner": "smarthome"}
	if err := updateOne(s, r, volumeEnv{}); err != nil {
		t.Fatalf("updateOne() error = %v", err)
	}
	if len(s.updated) != 1 {
		t.Fatalf("UpdateNetworkACL called %d times, want 1", len(s.updated))
	}
	put := s.updated[0]
	if want := held(ruleHA); !reflect.DeepEqual(put.Ingress, want) {
		t.Errorf("ingress = %+v, want exactly the declared list %+v: a rule the stack no longer lists is removed", put.Ingress, want)
	}
	if want := held(ruleDNS); !reflect.DeepEqual(put.Egress, want) {
		t.Errorf("egress = %+v, want the live list back unchanged %+v", put.Egress, want)
	}
	if put.Description != "made by hand" {
		t.Errorf("description = %q: the stack states none, so the live one stays", put.Description)
	}
	if put.Config["user.keep"] != "me" || put.Config["user.owner"] != "smarthome" {
		t.Errorf("config = %v, want the live key kept and the declared one merged in", put.Config)
	}
	if s.etags[0] != "etag-acl" {
		t.Errorf("etag = %q, want the one read with the ACL", s.etags[0])
	}
}

func TestApplyOne_NetworkACLIsIdempotent(t *testing.T) {
	s := &aclServer{}
	var notes []string
	note := func(f string, a ...any) { notes = append(notes, strings.TrimSpace(fmt.Sprintf(f, a...))) }
	r := aclResource([]api.NetworkACLRule{ruleHA})
	r.Description = "only the ingress helper"

	apply := func(r Resource) outcome {
		t.Helper()
		out, err := applyOne(s, r, PlanOptions{}, note)
		if err != nil {
			t.Fatalf("applyOne() error = %v", err)
		}
		return out
	}

	// 1. absent: created. 2. again: nothing to do, nothing written.
	if out := apply(r); out != outChanged || len(s.created) != 1 {
		t.Fatalf("first apply = %v with %d creates; want outChanged and 1", out, len(s.created))
	}
	if p, _ := planNetworkACL(s, r); p.Action != ActionNone {
		t.Fatalf("plan after create = %v %v, want no changes", p.Action, p.Changes)
	}
	if out := apply(r); out != outConverged || len(s.created) != 1 || len(s.updated) != 0 {
		t.Fatalf("second apply = %v with %d creates, %d updates; want outConverged and no new writes", out, len(s.created), len(s.updated))
	}

	// 3. a rule is added: updated once, and then it is stable again.
	r2 := r
	r2.Ingress = []api.NetworkACLRule{ruleHA, ruleMQTT}
	if out := apply(r2); out != outChanged || len(s.updated) != 1 {
		t.Fatalf("apply with a rule added = %v with %d updates; want outChanged and 1", out, len(s.updated))
	}
	if want := held(ruleHA, ruleMQTT); !reflect.DeepEqual(s.acls["svc-in"].Ingress, want) {
		t.Errorf("stored ingress = %+v, want %+v", s.acls["svc-in"].Ingress, want)
	}
	if out := apply(r2); out != outConverged || len(s.updated) != 1 {
		t.Fatalf("apply again = %v with %d updates; want outConverged and still 1", out, len(s.updated))
	}

	// 4. the rules are reordered: one update, the stored order follows the stack, then stable.
	r3 := r
	r3.Ingress = []api.NetworkACLRule{ruleMQTT, ruleHA}
	if out := apply(r3); out != outChanged || len(s.updated) != 2 {
		t.Fatalf("apply with the rules reordered = %v with %d updates; want outChanged and 2", out, len(s.updated))
	}
	if want := held(ruleMQTT, ruleHA); !reflect.DeepEqual(s.acls["svc-in"].Ingress, want) {
		t.Errorf("stored ingress = %+v, want the reordered %+v", s.acls["svc-in"].Ingress, want)
	}
	if out := apply(r3); out != outConverged || len(s.updated) != 2 {
		t.Fatalf("apply again = %v with %d updates; want outConverged and still 2", out, len(s.updated))
	}

	// 5. the stack says the lists are empty: both go, then stable.
	r4 := r
	r4.Ingress, r4.Egress = []api.NetworkACLRule{}, []api.NetworkACLRule{}
	if out := apply(r4); out != outChanged || len(s.acls["svc-in"].Ingress) != 0 {
		t.Fatalf("apply with empty lists = %v, stored ingress %+v", out, s.acls["svc-in"].Ingress)
	}
	if out := apply(r4); out != outConverged {
		t.Fatalf("apply again = %v, want outConverged", out)
	}
	if len(s.created) != 1 {
		t.Errorf("the ACL was created %d times, want once", len(s.created))
	}
}

func TestApplyOne_NetworkACLNeverDeletes(t *testing.T) {
	// The fake has no DeleteNetworkACL, so a call would panic; an ACL missing from the stack is simply not looked at.
	s := &aclServer{acls: map[string]*api.NetworkACL{"someone-elses": liveACL(held(ruleHA), nil)}}
	if _, err := applyOne(s, aclResource([]api.NetworkACLRule{ruleHA}), PlanOptions{}, func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.acls["someone-elses"]; !ok {
		t.Error("an ACL the stack does not declare was touched")
	}
}

func TestIsACLKey(t *testing.T) {
	for k, want := range map[string]bool{
		"security.acls":                        true,
		"security.acls.default.ingress.action": true,
		"security.acls.default.egress.action":  true,
		"security.acls.default.ingress.logged": true,
		"security.acls.default.egress.logged":  true,
		"security.aclsx":                       false,
		"security.acls2":                       false,
		"user.security.acls":                   false,
		"ipv4.nat":                             false,
		"ipv4.address":                         false,
		"dns.mode":                             false,
		"raw.dnsmasq":                          false,
		"security.nat":                         false,
	} {
		if got := isACLKey(k); got != want {
			t.Errorf("isACLKey(%q) = %v, want %v", k, got, want)
		}
	}
}

func TestPlanNetwork_AttachingAnACLToABridgeInUseIsNotDisruption(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.address": "203.0.113.1/24", "ipv4.nat": "true"}, "/1.0/instances/mosquitto", "/1.0/instances/ha")
	r := netResource(map[string]string{
		"ipv4.address":                        "203.0.113.1/24",
		"security.acls":                       "svc-in",
		"security.acls.default.egress.action": "allow",
	})
	p, err := planNetwork(&netServer{live: live}, r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionUpdate {
		t.Fatalf("action = %v (%v), want ActionUpdate: attaching an ACL needs no allow_disruption", p.Action, p.Blocked)
	}
	if len(p.Warnings) != 0 || len(p.Blocked) != 0 {
		t.Errorf("warnings %v blocked %v, want neither", p.Warnings, p.Blocked)
	}
	want := []string{`config.security.acls.default.egress.action: "" -> "allow"`, `config.security.acls: "" -> "svc-in"`} // diffConfig sorts the printed lines
	if !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("changes = %q, want %q (the plan still shows the attachment)", p.Changes, want)
	}
}

func TestPlanNetwork_ChangingAnAttachedACLListOnABridgeInUseIsNotDisruption(t *testing.T) {
	live := liveBridge(map[string]string{"security.acls": "a", "security.acls.default.ingress.action": "reject"}, "/1.0/instances/ha")
	r := netResource(map[string]string{"security.acls": "a,b", "security.acls.default.ingress.action": "drop", "security.acls.default.ingress.logged": "true"})
	p, _ := planNetwork(&netServer{live: live}, r)
	if p.Action != ActionUpdate || len(p.Warnings) != 0 {
		t.Fatalf("action %v warnings %v (%v), want a plain update", p.Action, p.Warnings, p.Blocked)
	}
}

func TestPlanNetwork_AnACLKeyDoesNotLetADisruptiveKeyThrough(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"}, "/1.0/instances/mosquitto")
	r := netResource(map[string]string{"ipv4.nat": "false", "security.acls": "svc-in"})
	p, _ := planNetwork(&netServer{live: live}, r)
	if p.Action != ActionBlocked {
		t.Fatalf("action = %v, want ActionBlocked: the NAT change is still disruption, whatever else is in the same edit", p.Action)
	}
	if len(p.Changes) != 2 {
		t.Errorf("a blocked plan shows everything it would change, got %v", p.Changes)
	}
	// With the consent the resource gives, the warning is about the instances, and the update goes through.
	r.AllowDisruption = true
	if p, _ = planNetwork(&netServer{live: live}, r); p.Action != ActionUpdate || len(p.Warnings) != 1 {
		t.Errorf("with allow_disruption: action %v warnings %v", p.Action, p.Warnings)
	}
}

func TestUpdateOne_NetworkWritesTheACLKeysToo(t *testing.T) {
	live := liveBridge(map[string]string{"ipv4.nat": "true"}, "/1.0/instances/ha")
	s := &netServer{live: live}
	r := netResource(map[string]string{"security.acls": "svc-in", "security.acls.default.egress.action": "allow"})
	if err := updateOne(s, r, volumeEnv{}); err != nil {
		t.Fatal(err)
	}
	cfg := s.updated[0].Config
	if cfg["security.acls"] != "svc-in" || cfg["security.acls.default.egress.action"] != "allow" || cfg["ipv4.nat"] != "true" {
		t.Errorf("config = %v", cfg)
	}
}

func TestPlanOne_NetworkNamingAnACLThatExistsNowhereIsFlagged(t *testing.T) {
	s := &aclServer{acls: map[string]*api.NetworkACL{"on-the-server": liveACL(nil, nil)}}
	r := netResource(map[string]string{"security.acls": "svc-in, on-the-server, nowhere"})
	opts := PlanOptions{acls: map[string]bool{"svc-in": true}} // svc-in is in the stack: created first, no lookup needed
	p, err := planOne(s, r, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one, for the ACL that exists nowhere", p.Warnings)
	}
	for _, want := range []string{"config security.acls", `"nowhere"`, "does not declare", "does not have", "refuse"} {
		if !strings.Contains(p.Warnings[0], want) {
			t.Errorf("warning %q does not mention %q", p.Warnings[0], want)
		}
	}
	if !slices.Equal(s.reads, []string{"on-the-server", "nowhere"}) {
		t.Errorf("the server was asked about %v: a declared ACL must not be looked up, an undeclared one once", s.reads)
	}
}

func TestPlanOne_NetworkWarningSurvivesWhateverElseThePlanSays(t *testing.T) {
	// A network that does not exist yet (create) still names an ACL that is nowhere: said on the create plan, and on apply's note.
	s := &aclServer{}
	p, err := planOne(s, netResource(map[string]string{"security.acls": "nowhere"}), PlanOptions{acls: map[string]bool{}})
	if err != nil || p.Action != ActionCreate {
		t.Fatalf("planOne() = %v, %v", p.Action, err)
	}
	if len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], `"nowhere"`) {
		t.Errorf("warnings = %q", p.Warnings)
	}
}

func TestACLReferenceWarnings_DeviceOnAnInstance(t *testing.T) {
	s := &aclServer{acls: map[string]*api.NetworkACL{}}
	r := Resource{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{
		"eth0": {"type": "nic", "network": "svclab0", "security.acls": "nowhere"},
		"eth1": {"type": "nic", "network": "svclab0"},
		"data": {"type": "disk", "security.acls": "not-a-nic"},
	}}
	got := aclReferenceWarnings(s, r, map[string]bool{})
	if len(got) != 1 || !strings.Contains(got[0], `device "eth0" security.acls`) || !strings.Contains(got[0], `"nowhere"`) {
		t.Errorf("warnings = %q, want one naming device eth0 and the ACL", got)
	}
}

func TestACLReferenceWarnings_AnUnreadableServerIsUnverifiedNotMissing(t *testing.T) {
	got := aclReferenceWarnings(&aclServer{err: errForbidden}, netResource(map[string]string{"security.acls": "x"}), map[string]bool{})
	if len(got) != 1 || !strings.Contains(got[0], "could not be asked") || strings.Contains(got[0], "does not have") {
		t.Errorf("warnings = %q: a 403 says nothing about whether the ACL exists", got)
	}
}

func TestACLReferenceWarnings_NothingToSayWhenNothingIsAttached(t *testing.T) {
	s := &aclServer{} // any read would be recorded
	for _, r := range []Resource{
		netResource(nil),
		netResource(map[string]string{"security.acls": ""}),
		{Kind: KindInstance, Name: "ha", Devices: map[string]map[string]string{"eth0": {"type": "nic", "network": "x"}}},
	} {
		if got := aclReferenceWarnings(s, r, nil); len(got) != 0 {
			t.Errorf("%s: warnings = %q", r.Name, got)
		}
	}
	if len(s.reads) != 0 {
		t.Errorf("the server was asked %v for nothing", s.reads)
	}
}

func TestDiffACLRules_ReorderNeedsTheSameRules(t *testing.T) {
	// Same length, but a rule differs as well: not a reorder, so it reads as the change it is.
	got := diffACLRules("ingress", held(ruleHA, ruleBlock), held(ruleBlock, ruleMQTT))
	for _, line := range got {
		if strings.Contains(line, "reordered") {
			t.Errorf("a list with a different rule is not a reorder: %q", got)
		}
	}
	if len(got) == 0 {
		t.Error("expected a diff")
	}
}

func TestDiffACLRules_RepeatedRulesCount(t *testing.T) {
	// Incus refuses duplicates, but the comparison must still not call [A A B] and [A B B] a reorder.
	if sameRules(held(ruleHA, ruleHA, ruleMQTT), held(ruleHA, ruleMQTT, ruleMQTT)) {
		t.Error("sameRules ignored a repeat")
	}
}

func TestKindNetworkACLIsKnown(t *testing.T) {
	if _, ok := kindPriority[KindNetworkACL]; !ok {
		t.Fatal("network-acl has no kind priority, so it would be an unknown kind")
	}
	if string(KindNetworkACL) != "network-acl" {
		t.Errorf("the kind is spelled %q; Incus's word is network-acl", KindNetworkACL)
	}
}

// stackServer is a server holding a bridge and ACLs that both remember what is written to them, for running the whole recipe.
type stackServer struct{ aclServer }

func (s *stackServer) UpdateNetwork(_ string, put api.NetworkPut, etag string) error {
	s.netServer.updated = append(s.netServer.updated, put)
	s.netServer.etag = etag
	s.netServer.live.NetworkPut = put
	return nil
}

// recipeFromTheDocs returns the stack file the "lab recipe" section of docs/network-acl.md shows, so the example a reader copies is the one tested.
func recipeFromTheDocs(t *testing.T) string {
	t.Helper()
	doc, err := os.ReadFile("../../docs/network-acl.md")
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(doc), "## The lab recipe as a stack")
	if !ok {
		t.Fatal("docs/network-acl.md has no 'The lab recipe as a stack' section")
	}
	_, block, ok := strings.Cut(after, "```yaml\n")
	if !ok {
		t.Fatal("the recipe section has no yaml block")
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		t.Fatal("the recipe's yaml block is not closed")
	}
	return block
}

func TestRecipe_TheDocsExampleReproducesTheLabRecipeAndConverges(t *testing.T) {
	resources, err := LoadFile(writeTemp(t, recipeFromTheDocs(t)))
	if err != nil {
		t.Fatalf("the example in docs/network-acl.md does not load: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("the example holds %d resources, want the ACL and the network", len(resources))
	}

	// What `incus network acl rule add svclab0-in ingress action=allow source=198.51.100.221/32 destination_port=8123 protocol=tcp state=enabled` stores.
	wantRule := api.NetworkACLRule{Action: "allow", Source: "198.51.100.221/32", Protocol: "tcp", DestinationPort: "8123", State: "enabled"}
	var acl, network Resource
	for _, r := range resources {
		switch r.Kind {
		case KindNetworkACL:
			acl = r
		case KindNetwork:
			network = r
		}
	}
	if acl.Name != "svclab0-in" || len(acl.Ingress) != 1 || acl.Ingress[0] != wantRule || len(acl.Egress) != 0 {
		t.Errorf("the ACL in the example = %+v, want one ingress rule %+v", acl, wantRule)
	}
	if network.Name != "svclab0" || network.Config["security.acls"] != "svclab0-in" || network.Config["security.acls.default.egress.action"] != "allow" {
		t.Errorf("the network in the example = %+v, want security.acls=svclab0-in and the default egress action allow", network)
	}

	levels, err := Levels(resources)
	if err != nil {
		t.Fatal(err)
	}
	if !inLevel(levels, 0, "svclab0-in") || !inLevel(levels, 1, "svclab0") {
		t.Fatalf("want the ACL created before the network that names it, got %v", levels)
	}

	// The bridge exists already, with two instances on it, as in the lab: attaching the ACL must go through without allow_disruption.
	server := &stackServer{}
	server.netServer.live = liveBridge(map[string]string{"ipv4.address": "203.0.113.1/24", "ipv4.nat": "true"}, "/1.0/instances/ha-lab?project=ha-eval", "/1.0/instances/mosquitto?project=ha-eval")
	opts := PlanOptions{}.ForResources(resources)
	var notes []string
	run := func() ([]string, error) {
		notes = nil
		return applyLevels(context.Background(), levels, func(r Resource, note func(string, ...any)) (outcome, error) {
			return applyOne(server, r, opts, func(f string, a ...any) {
				notes = append(notes, fmt.Sprintf(f, a...))
				note(f, a...)
			})
		})
	}

	if _, err := run(); err != nil {
		t.Fatalf("first apply: %v (%v)", err, notes)
	}
	if len(server.created) != 1 || len(server.netServer.updated) != 1 {
		t.Fatalf("first apply: %d ACL creates, %d network updates; want 1 and 1 (%v)", len(server.created), len(server.netServer.updated), notes)
	}
	if got := server.netServer.updated[0].Config; got["security.acls"] != "svclab0-in" || got["ipv4.address"] != "203.0.113.1/24" || got["ipv4.nat"] != "true" {
		t.Errorf("the bridge was written %v: the ACL keys are set and its address and NAT are kept", got)
	}
	for _, n := range notes {
		if strings.Contains(n, "BLOCKED") || strings.Contains(n, "warning") {
			t.Errorf("unexpected note on first apply: %s", n)
		}
	}

	// Second plan: no changes on either resource. Second apply: no writes.
	plans, err := PlanWithOptions(server, resources, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.Action != ActionNone {
			t.Errorf("%s/%s after apply: %v %v, want no changes", p.Resource.Kind, p.Resource.Name, p.Action, p.Changes)
		}
	}
	if _, err := run(); err != nil {
		t.Fatalf("second apply: %v (%v)", err, notes)
	}
	if len(server.created) != 1 || len(server.updated) != 0 || len(server.netServer.updated) != 1 {
		t.Errorf("second apply wrote again: %d ACL creates, %d ACL updates, %d network updates", len(server.created), len(server.updated), len(server.netServer.updated))
	}
}

// derive_ingress is only a marker at load: the rules are filled in by internal/derive before the stack is planned. A marked ACL carries no rules of
// its own here, which is how a plan of a stack that was not prepared (a test, a command that never derives) can tell it from one declared empty.
func TestLoadFile_NetworkACLDeriveIngress(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, "kind: network-acl\nname: a-in\nderive_ingress: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || !rs[0].DeriveIngress || rs[0].Ingress != nil {
		t.Errorf("got %+v, want a marked ACL with no ingress list", rs)
	}
}
