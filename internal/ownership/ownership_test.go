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

func TestTheKeyIsInsideTheTinkNamespace(t *testing.T) {
	if Key != "user.tink.stack" {
		t.Errorf("Key = %q: volumes stamped by earlier builds carry user.tink.stack", Key)
	}
}
