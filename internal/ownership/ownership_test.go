package ownership

import (
	"reflect"
	"testing"
)

func TestAddressOfEachKind(t *testing.T) {
	for _, c := range []struct {
		kind, project, pool, name string
		want                      string
	}{
		{KindInstance, "web", "", "app", "instance/web/app"},
		{KindProfile, "web", "", "base", "profile/web/base"},
		{KindStorageVolume, "web", "fast", "data", "storage-volume/web/fast/data"},
		{KindImage, "web", "", "haos", "image/web/haos"},
		{KindProject, "", "", "web", "project/web"},
		{KindNetwork, "", "", "br-lab", "network/br-lab"},
		{KindNetworkACL, "", "", "lan", "network-acl/lan"},
		// an empty project or pool is the default one, so a declaration that leaves them out meets a listing that spells them out
		{KindInstance, "", "", "app", "instance/default/app"},
		{KindProfile, "default", "", "base", "profile/default/base"},
		{KindStorageVolume, "", "", "data", "storage-volume/default/default/data"},
		{KindStorageVolume, "", "fast", "data", "storage-volume/default/fast/data"},
		{KindStorageVolume, "web", "", "data", "storage-volume/web/default/data"},
		{KindImage, "", "", "haos", "image/default/haos"},
		// a server-global kind and a project take no project: one that is given is not part of the address
		{KindNetwork, "web", "fast", "br-lab", "network/br-lab"},
		{KindNetworkACL, "web", "", "lan", "network-acl/lan"},
		{KindProject, "web", "", "web", "project/web"},
		// a kind that carries no stamp has no address
		{"file", "web", "", "x", ""},
		{"", "", "", "x", ""},
	} {
		if got := Address(c.kind, c.project, c.pool, c.name); got != c.want {
			t.Errorf("Address(%q, %q, %q, %q) = %q, want %q", c.kind, c.project, c.pool, c.name, got, c.want)
		}
	}
}

func TestClassify(t *testing.T) {
	const addr = "storage-volume/default/default/data"
	cases := []struct {
		name  string
		cfg   map[string]string
		stack string
		state State
		owner string
	}{
		{"no stamp at all", nil, "lab", Unstamped, ""},
		{"an empty owner stamp is no stamp", map[string]string{Key: ""}, "lab", Unstamped, ""},
		{"a resource stamp without an owner is no claim", map[string]string{ResourceKey: addr}, "lab", Unstamped, ""},
		{"here: this stack's stamp and its own address", map[string]string{Key: "lab", ResourceKey: addr}, "lab", Here, "lab"},
		{"copy: this stack's stamp and another object's address", map[string]string{Key: "lab", ResourceKey: "storage-volume/default/default/other"}, "lab", Copy, "lab"},
		{"copy: the same name in another project is another object", map[string]string{Key: "lab", ResourceKey: "storage-volume/web/default/data"}, "lab", Copy, "lab"},
		{"unbound: this stack's stamp and no resource stamp yet", map[string]string{Key: "lab"}, "lab", Unbound, "lab"},
		{"unbound: an empty resource stamp is none", map[string]string{Key: "lab", ResourceKey: ""}, "lab", Unbound, "lab"},
		{"elsewhere: another stack's stamp", map[string]string{Key: "other", ResourceKey: addr}, "lab", Elsewhere, "other"},
		{"elsewhere does not look at the resource stamp", map[string]string{Key: "other"}, "lab", Elsewhere, "other"},
		{"a stack with no name has no answer", map[string]string{Key: "lab", ResourceKey: addr}, "", Unknown, ""},
		{"nor does a nil config", nil, "", Unknown, ""},
	}
	for _, c := range cases {
		if s, o := Classify(c.cfg, c.stack, addr); s != c.state || o != c.owner {
			t.Errorf("%s: Classify(%v, %q) = %v, %q; want %v, %q", c.name, c.cfg, c.stack, s, o, c.state, c.owner)
		}
	}
	if s, _ := Classify(map[string]string{Key: "lab", ResourceKey: addr}, "lab", ""); s != Unknown {
		t.Errorf("an object with no address has no answer, got %v", s)
	}
}

func TestEveryStateHasItsOwnName(t *testing.T) {
	// the states are told apart by their names, so a message that prints one is not ambiguous
	seen := map[string]State{}
	for _, s := range []State{Unknown, Unstamped, Here, Elsewhere, Copy, Unbound} {
		if prev, dup := seen[s.String()]; dup {
			t.Errorf("states %d and %d are both called %q", prev, s, s.String())
		}
		seen[s.String()] = s
	}
}

func TestWithCopiesAndNeverChangesTheMapItIsGiven(t *testing.T) {
	in := map[string]string{"a": "b"}
	out := With(in, "lab", "instance/default/web")
	if !reflect.DeepEqual(out, map[string]string{"a": "b", Key: "lab", ResourceKey: "instance/default/web"}) {
		t.Errorf("With = %v", out)
	}
	if _, ok := in[Key]; ok {
		t.Error("With changed its argument")
	}
	if _, ok := in[ResourceKey]; ok {
		t.Error("With changed its argument")
	}
	if got := With(nil, "lab", "instance/default/web"); !reflect.DeepEqual(got, map[string]string{Key: "lab", ResourceKey: "instance/default/web"}) {
		t.Errorf("With(nil) = %v", got)
	}
	if got := With(in, "", "instance/default/web"); !reflect.DeepEqual(got, in) {
		t.Errorf("a stack with no name adds nothing: %v", got)
	}
	if got := With(in, "lab", ""); !reflect.DeepEqual(got, map[string]string{"a": "b", Key: "lab"}) {
		t.Errorf("no address, no resource stamp (the object would be unbound, which is not a claim about another object): %v", got)
	}
}

// What With writes, Classify reads back as here.
func TestWhatWithWritesReadsBackAsHere(t *testing.T) {
	addr := Address(KindInstance, "web", "", "app")
	if s, _ := Classify(With(nil, "lab", addr), "lab", addr); s != Here {
		t.Errorf("an object created with the stamps reads %v, want here", s)
	}
	if s, _ := Classify(With(nil, "lab", addr), "lab", Address(KindInstance, "web", "", "app-copy")); s != Copy {
		t.Errorf("the same stamps on an object of another name read %v, want copy", s)
	}
}

func TestTheStampIsIntent(t *testing.T) {
	if Key != "user.tink.intent.stack" {
		t.Errorf("Key = %q, want the stack stamp under the intent prefix", Key)
	}
	if ClassOf(Key) != Intent || ClassOf(ResourceKey) != Intent || ClassOf(AppliedKey) != Intent {
		t.Error("the owner stamp, the resource stamp and the applied record are intent")
	}
}

func TestClassOfIsAPrefixTest(t *testing.T) {
	for key, want := range map[string]Class{
		// facts: the short list, and nothing else
		"user.tink.role":             Fact,
		"user.tink.leg.eth0":         Fact,
		"user.tink.offer.web.port":   Fact,
		"user.tink.offer.web.listen": Fact,
		"user.tink.dns.zone":         Fact,
		// intent
		"user.tink.intent.stack":          Intent,
		"user.tink.intent.resource":       Intent,
		"user.tink.intent.site":           Intent,
		"user.tink.intent.publish.web":    Intent,
		"user.tink.intent.derive.ingress": Intent,
		"user.tink.intent.backup.policy":  Intent,
		"user.tink.intent.applied":        Intent,
		// outcomes
		"user.tink.outcome.backup.copy.nas.copied-at": Outcome,
		"user.tink.outcome.backup.verified-at":        Outcome,
		"user.tink.outcome.exec.reload.trigger-hash":  Outcome,
		"user.tink.outcome.helper":                    Outcome,
		"user.tink.outcome.helper.status":             Outcome,
		"user.tink.outcome.run.command":               Outcome,
		// not tink's to classify: Incus's, a person's own, and names that only look like a class or a fact
		"volatile.uuid":         Unclassified,
		"user.note":             Unclassified,
		"user.tink.stack":       Unclassified,
		"user.tink.roles":       Unclassified,
		"user.tink.dns.zones":   Unclassified,
		"user.tink.intent":      Unclassified,
		"user.tink.outcome":     Unclassified,
		"user.tink.example.key": Unclassified,
	} {
		if got := ClassOf(key); got != want {
			t.Errorf("ClassOf(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestWhatACopyDropsIsTheOutcomesThePolicyAndTheAppliedRecord(t *testing.T) {
	const policy = "user.tink.intent.backup.policy"
	for key, want := range map[string]bool{
		"user.tink.outcome.backup.copy.nas.copied-at": true,
		"user.tink.outcome.backup.copied-at":          true,
		"user.tink.outcome.backup.verified-at":        true,
		policy:                                        true,
		AppliedKey:                                    true,
		Key:                                           false,
		ResourceKey:                                   false,
		"user.tink.role":                              false,
		"user.note":                                   false,
	} {
		if got := DroppedOnCopy(key, policy); got != want {
			t.Errorf("DroppedOnCopy(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestOnlyTheKeysNotDerivedIntoAConfigAreStamps(t *testing.T) {
	for key, want := range map[string]bool{
		Key: true, ResourceKey: true, AppliedKey: true,
		"user.tink.intent.site": false, "user.tink.intent.publish.web": false, "user.tink.intent.derive.ingress": false,
	} {
		if got := IsStamp(key); got != want {
			t.Errorf("IsStamp(%q) = %v, want %v", key, got, want)
		}
	}
}
