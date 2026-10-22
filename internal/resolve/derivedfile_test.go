package resolve

import (
	"strings"
	"testing"
)

func TestFillDerived(t *testing.T) {
	content := "route /health\n" + DeriveBegin + "\n" + DeriveEnd + "\nafter\n"
	got, err := FillDerived(content, "@a host a.example.org\nhandle @a {\n\timport sso\n}\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "route /health\n" + DeriveBegin + "\n@a host a.example.org\nhandle @a {\n\timport sso\n}\n" + DeriveEnd + "\nafter\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	// filled again with something else, the old region goes
	again, err := FillDerived(got, "@b host b.example.org\n")
	if err != nil || strings.Contains(again, "a.example.org") || !strings.Contains(again, "b.example.org") {
		t.Errorf("a second fill must replace the first: %v\n%s", err, again)
	}
	// nothing published: the markers stay and the region is empty
	empty, err := FillDerived(got, "")
	if err != nil || empty != "route /health\n"+DeriveBegin+"\n"+DeriveEnd+"\nafter\n" {
		t.Errorf("an empty rendering leaves an empty region: %v\n%q", err, empty)
	}
}

// A region inside a YAML list keeps its indentation.
func TestFillDerivedIndentsAsTheMarkerIs(t *testing.T) {
	content := "rules:\n  - domain: auth.example.org\n    policy: bypass\n    " + DeriveBegin + "\n    " + DeriveEnd + "\nsession:\n"
	got, err := FillDerived(content, "- domain: 'a.example.org'\n  policy: one_factor\n")
	if err != nil {
		t.Fatal(err)
	}
	want := "rules:\n  - domain: auth.example.org\n    policy: bypass\n    " + DeriveBegin + "\n    - domain: 'a.example.org'\n      policy: one_factor\n    " + DeriveEnd + "\nsession:\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFillDerivedRefusesAMissingOrBrokenRegion(t *testing.T) {
	for name, c := range map[string]struct{ content, want string }{
		"no markers":       {"nothing here\n", "needs a line"},
		"no end":           {DeriveBegin + "\n", "no \"# END tink derive\" after it"},
		"no begin":         {DeriveEnd + "\n", "no \"# BEGIN tink derive\" before it"},
		"end first":        {DeriveEnd + "\n" + DeriveBegin + "\n", "comes before"},
		"two begins":       {DeriveBegin + "\n" + DeriveBegin + "\n" + DeriveEnd + "\n", "appears twice"},
		"two ends":         {DeriveBegin + "\n" + DeriveEnd + "\n" + DeriveEnd + "\n", "appears twice"},
		"marker in a line": {"x " + DeriveBegin + "\n" + DeriveEnd + "\n", "no \"# BEGIN tink derive\" before it"},
	} {
		if _, err := FillDerived(c.content, "x"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}

func TestLoadFile_DerivedFile(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, "kind: file\nname: routes\ninstance: ingress\npath: /etc/caddy/apps.caddy\nderive: caddy\ncontent: |\n  "+DeriveBegin+"\n  "+DeriveEnd+"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Derive != DeriveCaddy {
		t.Errorf("got %+v", rs)
	}
	for name, c := range map[string]struct{ yaml, want string }{
		"an unknown rendering": {"kind: file\nname: a\ninstance: i\npath: /p\nderive: mesh\ncontent: x\n", "not one of"},
		"no region":            {"kind: file\nname: a\ninstance: i\npath: /p\nderive: sso\ncontent: x\n", "needs a line"},
		"on another kind":      {"kind: instance\nname: a\nimage: x\nderive: caddy\n", "Derive"},
	} {
		if _, err := LoadFile(writeTemp(t, c.yaml)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}
