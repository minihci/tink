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
		"an unknown rendering": {"kind: file\nname: a\ninstance: i\npath: /p\nderive: nope\ncontent: x\n", "not one of"},
		"no region":            {"kind: file\nname: a\ninstance: i\npath: /p\nderive: sso\ncontent: x\n", "needs a line"},
		"on another kind":      {"kind: instance\nname: a\nimage: x\nderive: caddy\n", "Derive"},
	} {
		if _, err := LoadFile(writeTemp(t, c.yaml)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, c.want)
		}
	}
}

func TestGuardDerived(t *testing.T) {
	filled := "# generated\n" + DeriveBegin + "\n192.0.2.3 a.example.org\n" + DeriveEnd + "\n"
	empty := "# generated\n" + DeriveBegin + "\n" + DeriveEnd + "\n"
	update := func(f Resource) PlannedResource {
		return PlannedResource{Resource: f, Action: ActionUpdate, Changes: []string{"content differs"}}
	}
	for _, c := range []struct {
		name    string
		file    Resource
		action  Action
		live    string
		blocked string
		note    string
	}{
		{"a file tink wrote, replaced by the new records", Resource{Derive: DeriveDNS, Content: filled}, ActionUpdate, "# generated\n" + DeriveBegin + "\n192.0.2.3 old.example.org\n" + DeriveEnd + "\n", "", ""},
		{"a file a person wrote, with records in it", Resource{Derive: DeriveDNS, Content: filled}, ActionBlocked, "192.0.2.9 hand.example.org\n", "was not written by tink", ""},
		{"the same, taken over once", Resource{Derive: DeriveDNS, Content: filled, AdoptExisting: true}, ActionUpdate, "192.0.2.9 hand.example.org\n", "", "adopt_existing: replaces the 1 line(s)"},
		{"a file with only comments is nobody's to protect", Resource{Derive: DeriveDNS, Content: filled}, ActionUpdate, "# nothing here\n", "", ""},
		{"nothing published, records on the file", Resource{Derive: DeriveDNS, Content: empty}, ActionBlocked, "# generated\n" + DeriveBegin + "\n192.0.2.3 a.example.org\n" + DeriveEnd + "\n", "nothing is published", ""},
		{"nothing published, and that is meant", Resource{Derive: DeriveDNS, Content: empty, AllowEmpty: true}, ActionUpdate, "# generated\n" + DeriveBegin + "\n192.0.2.3 a.example.org\n" + DeriveEnd + "\n", "", ""},
		{"nothing published, nothing there", Resource{Derive: DeriveDNS, Content: empty}, ActionUpdate, "# generated\n" + DeriveBegin + "\n" + DeriveEnd + "\nextra\n", "", ""},
		{"a file that is not derived is not guarded", Resource{Content: filled}, ActionUpdate, "192.0.2.9 hand.example.org\n", "", ""},
	} {
		c.file.Kind, c.file.Name, c.file.Instance, c.file.Path = KindFile, "hosts", "dns", "/hosts"
		got := guardDerived(c.file, update(c.file), c.live)
		if got.Action != c.action {
			t.Errorf("%s: action = %v, want %v (%v)", c.name, got.Action, c.action, got.Blocked)
			continue
		}
		if c.blocked != "" && (len(got.Blocked) != 1 || !strings.Contains(got.Blocked[0], c.blocked)) {
			t.Errorf("%s: blocked = %v, want it to contain %q", c.name, got.Blocked, c.blocked)
		}
		if c.note != "" && (len(got.Notes) != 1 || !strings.Contains(got.Notes[0], c.note)) {
			t.Errorf("%s: notes = %v, want one containing %q", c.name, got.Notes, c.note)
		}
	}
	// a file that does not exist, or already holds the content, is not guarded
	f := Resource{Kind: KindFile, Name: "h", Instance: "dns", Path: "/hosts", Derive: DeriveDNS, Content: "x"}
	for _, a := range []Action{ActionCreate, ActionNone} {
		if got := guardDerived(f, PlannedResource{Resource: f, Action: a}, "hand written\n"); got.Action != a {
			t.Errorf("%v must pass through, got %v", a, got.Action)
		}
	}
}

func TestLoadFile_DerivedFileOptions(t *testing.T) {
	text := "kind: file\nname: hosts\ninstance: dns\npath: /hosts\nderive: dns\nadopt_existing: true\nallow_empty: true\ncontent: |\n  " + DeriveBegin + "\n  " + DeriveEnd + "\n"
	rs, err := LoadFile(writeTemp(t, text))
	if err != nil {
		t.Fatal(err)
	}
	if !rs[0].AdoptExisting || !rs[0].AllowEmpty || rs[0].Derive != DeriveDNS {
		t.Errorf("got %+v", rs[0])
	}
	if _, err := LoadFile(writeTemp(t, "kind: file\nname: a\ninstance: i\npath: /p\nadopt_existing: true\ncontent: x\n")); err == nil || !strings.Contains(err.Error(), "belong to a file that has a derive field") {
		t.Errorf("adopt_existing without derive: err = %v", err)
	}
}

func TestLoadFile_DerivedMeshFile(t *testing.T) {
	rs, err := LoadFile(writeTemp(t, "kind: file\nname: mesh-records\ninstance: headscale\npath: /etc/headscale/extra-records.json\nderive: mesh\n"))
	if err != nil {
		t.Fatalf("a derive: mesh file takes no content: %v", err)
	}
	if rs[0].Derive != DeriveMesh || rs[0].Content != "" {
		t.Errorf("got %+v", rs[0])
	}
	if _, err := LoadFile(writeTemp(t, "kind: file\nname: a\ninstance: i\npath: /p\nderive: mesh\ncontent: '[]'\n")); err == nil || !strings.Contains(err.Error(), "takes no content") {
		t.Errorf("a derive: mesh file with content: err = %v", err)
	}
}

func TestGuardRecords(t *testing.T) {
	desired := "[\n  {\"name\": \"a.home.example.org\", \"type\": \"A\", \"value\": \"192.0.2.3\"}\n]\n"
	none := "[]\n"
	rec := func(name string) string {
		return "[{\"name\": \"" + name + "\", \"type\": \"A\", \"value\": \"192.0.2.9\"}]"
	}
	for _, c := range []struct {
		name    string
		file    Resource
		live    string
		action  Action
		blocked string
		note    string
	}{
		{"a file tink wrote, an old name replaced", Resource{Content: desired}, rec("old.home.example.org"), ActionUpdate, "", ""},
		{"an empty file", Resource{Content: desired}, "  \n", ActionUpdate, "", ""},
		{"a record outside the zone", Resource{Content: desired}, rec("hand.other.example.net"), ActionBlocked, "outside the zone home.example.org", ""},
		{"the same, taken over once", Resource{Content: desired, AdoptExisting: true}, rec("hand.other.example.net"), ActionUpdate, "", "adopt_existing: replaces 1 record(s)"},
		{"not a records file", Resource{Content: desired}, "listen_addr: 0.0.0.0\n", ActionBlocked, "not a JSON array of records", ""},
		{"not a records file, taken over once", Resource{Content: desired, AdoptExisting: true}, "listen_addr: 0.0.0.0\n", ActionUpdate, "", "which is not a JSON array"},
		{"nothing published, records on the file", Resource{Content: none}, rec("a.home.example.org"), ActionBlocked, "nothing is published", ""},
		{"nothing published, and that is meant", Resource{Content: none, AllowEmpty: true}, rec("a.home.example.org"), ActionUpdate, "", ""},
	} {
		c.file.Kind, c.file.Name, c.file.Instance, c.file.Path, c.file.Derive, c.file.DerivedZone = KindFile, "records", "hs", "/r.json", DeriveMesh, "home.example.org"
		got := guardDerived(c.file, PlannedResource{Resource: c.file, Action: ActionUpdate}, c.live)
		if got.Action != c.action {
			t.Errorf("%s: action = %v, want %v (%v)", c.name, got.Action, c.action, got.Blocked)
			continue
		}
		if c.blocked != "" && (len(got.Blocked) != 1 || !strings.Contains(got.Blocked[0], c.blocked)) {
			t.Errorf("%s: blocked = %v, want it to contain %q", c.name, got.Blocked, c.blocked)
		}
		if c.note != "" && (len(got.Notes) != 1 || !strings.Contains(got.Notes[0], c.note)) {
			t.Errorf("%s: notes = %v, want one containing %q", c.name, got.Notes, c.note)
		}
	}
}
