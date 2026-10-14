package resolve

import (
	"strings"
	"testing"
)

const publishStack = `kind: stack
name: canary
project: canary
---
kind: instance
name: whoami
image: docker-oci:traefik/whoami:v1.12.0
---
kind: publish
name: whoami
instance: whoami
offer: web
allow: [family]
---
kind: publish
name: whoami-admin
instance: whoami
offer: web
allow: [admins, family]
`

func TestAPublishSharesItsInstancesNameAndIsNotPlanned(t *testing.T) {
	rs, err := loadStack(t, publishStack)
	if err != nil {
		t.Fatal(err)
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatalf("a publish named like its instance must not collide with it: %v", err)
	}
	n := 0
	for _, l := range levels {
		for _, r := range l {
			n++
			if r.Kind != KindInstance {
				t.Errorf("only the instance is planned, got %s/%s: a publish is a declaration, not something to converge", r.Kind, r.Name)
			}
		}
	}
	if n != 1 {
		t.Errorf("planned %d resources, want 1 (the instance)", n)
	}
}

func TestAPublishIsParsedWithItsFields(t *testing.T) {
	rs, err := loadStack(t, publishStack)
	if err != nil {
		t.Fatal(err)
	}
	var got []Resource
	for _, r := range rs {
		if r.Kind == KindPublish {
			got = append(got, r)
		}
	}
	if len(got) != 2 {
		t.Fatalf("publishes = %d, want 2", len(got))
	}
	if p := got[1]; p.Name != "whoami-admin" || p.Instance != "whoami" || p.Offer != "web" || strings.Join(p.Allow, ",") != "admins,family" {
		t.Errorf("second publish = %+v", p)
	}
}

func TestAPublishTakesTheStacksDefaultProject(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/s.yaml"
	if err := writeFile(path, publishStack); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rs {
		if r.Kind == KindPublish && r.Project != "canary" {
			t.Errorf("publish %s has project %q, want the stack's default %q: it must be judged against the instance in the same project", r.Name, r.Project, "canary")
		}
	}
}

func TestAMalformedPublishIsRejectedAtLoad(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"no instance", "kind: publish\nname: a\noffer: web\nallow: [family]\n", "needs instance"},
		{"no offer", "kind: publish\nname: a\ninstance: a\nallow: [family]\n", "needs offer"},
		{"no groups", "kind: publish\nname: a\ninstance: a\noffer: web\n", "needs allow"},
		{"empty groups", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: []\n", "needs allow"},
		{"a typed subject", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [\"group:family\"]\n", "plain group names"},
		{"an instance subject", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [\"instance:ha\"]\n", "plain group names"},
		{"a group twice", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [family, family]\n", "twice"},
		{"a quote in a group", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [\"fam'ily\"]\n", "corrupt"},
		{"an empty group name", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [\"\"]\n", "empty group"},
		{"an upper case name", "kind: publish\nname: Whoami\ninstance: a\noffer: web\nallow: [family]\n", "host label"},
		{"an underscore name", "kind: publish\nname: who_ami\ninstance: a\noffer: web\nallow: [family]\n", "host label"},
		{"a name starting with a hyphen", "kind: publish\nname: -a\ninstance: a\noffer: web\nallow: [family]\n", "host label"},
		{"a name with a dot", "kind: publish\nname: a.b\ninstance: a\noffer: web\nallow: [family]\n", "host label"},
		{"a bad offer name", "kind: publish\nname: a\ninstance: a\noffer: Web\nallow: [family]\n", "not an offer name"},
		{"depends_on", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [family]\ndepends_on: [a]\n", "no depends_on"},
		{"a field of another kind", "kind: publish\nname: a\ninstance: a\noffer: web\nallow: [family]\npath: /x\n", `does not use field "Path"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := loadStack(t, c.doc)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestOfferAndAllowBelongToPublishOnly(t *testing.T) {
	for _, doc := range []string{
		"kind: instance\nname: a\nimage: docker-oci:x:1\noffer: web\n",
		"kind: instance\nname: a\nimage: docker-oci:x:1\nallow: [family]\n",
		"kind: file\nname: f\ninstance: a\npath: /x\ncontent: y\noffer: web\n",
	} {
		if _, err := loadStack(t, doc); err == nil || !strings.Contains(err.Error(), "does not use field") {
			t.Errorf("a stray offer or allow must be rejected, got %v for %q", err, doc)
		}
	}
}

func TestTwoPublishesWithOneNameAreRejected(t *testing.T) {
	rs, err := loadStack(t, publishStack+"---\nkind: publish\nname: whoami\ninstance: whoami\noffer: web\nallow: [admins]\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Levels(rs); err == nil || !strings.Contains(err.Error(), "duplicate publish name") {
		t.Errorf("a published name is unique in the zone, got %v", err)
	}
}

func TestDependsOnCannotNameAPublish(t *testing.T) {
	rs, err := loadStack(t, publishStack+"---\nkind: exec\nname: x\ninstance: whoami\ncommand: [true]\ncheck: [true]\ndepends_on: [whoami-admin]\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Levels(rs); err == nil || !strings.Contains(err.Error(), "not a resource in this stack") {
		t.Errorf("a publish is not something to wait for, got %v", err)
	}
}

func TestAFileFollowingAnInstanceIsNotCapturedByAPublishOfTheSameName(t *testing.T) {
	// byName in applyStackProject maps names to resources; a publish named like the instance must not replace the instance there,
	// or the file would inherit whatever project the publish has.
	rs, err := loadStack(t, publishStack+"---\nkind: file\nname: cfg\ninstance: whoami\npath: /x\ncontent: y\n")
	if err != nil {
		t.Fatal(err)
	}
	applyStackProject(rs)
	for _, r := range rs {
		if r.Kind == KindFile && r.Project != "canary" {
			t.Errorf("file project = %q, want canary (the instance's)", r.Project)
		}
	}
}
