package ownership

import (
	"reflect"
	"testing"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		cfg   map[string]string
		stack string
		state State
		owner string
	}{
		{nil, "lab", Unstamped, ""},
		{map[string]string{Key: ""}, "lab", Unstamped, ""},
		{map[string]string{Key: "lab"}, "lab", Here, "lab"},
		{map[string]string{Key: "other"}, "lab", Elsewhere, "other"},
		{map[string]string{Key: "lab"}, "", Unknown, ""},
		{nil, "", Unknown, ""},
	}
	for _, c := range cases {
		if s, o := Classify(c.cfg, c.stack); s != c.state || o != c.owner {
			t.Errorf("Classify(%v, %q) = %v, %q; want %v, %q", c.cfg, c.stack, s, o, c.state, c.owner)
		}
	}
}

func TestWithCopiesAndNeverChangesTheMapItIsGiven(t *testing.T) {
	in := map[string]string{"a": "b"}
	out := With(in, "lab")
	if !reflect.DeepEqual(out, map[string]string{"a": "b", Key: "lab"}) {
		t.Errorf("With = %v", out)
	}
	if _, ok := in[Key]; ok {
		t.Error("With changed its argument")
	}
	if got := With(nil, "lab"); !reflect.DeepEqual(got, map[string]string{Key: "lab"}) {
		t.Errorf("With(nil) = %v", got)
	}
	if got := With(in, ""); !reflect.DeepEqual(got, in) {
		t.Errorf("a stack with no name adds nothing: %v", got)
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
