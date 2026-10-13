package identity

import "testing"

func TestNormalize_SortsGroupsAndMembersAndLeavesTheInputAlone(t *testing.T) {
	in := Snapshot{Groups: []Group{
		{Name: "family", Members: []Person{{ID: "bob"}, {ID: "alice"}}},
		{Name: "admins", Members: []Person{{ID: "alice"}}},
	}}
	out := in.Normalize()

	if out.Groups[0].Name != "admins" || out.Groups[1].Name != "family" {
		t.Fatalf("groups not sorted by name: %+v", out.Groups)
	}
	if out.Groups[1].Members[0].ID != "alice" || out.Groups[1].Members[1].ID != "bob" {
		t.Fatalf("members not sorted by id: %+v", out.Groups[1].Members)
	}
	if in.Groups[0].Name != "family" || in.Groups[0].Members[0].ID != "bob" {
		t.Fatalf("Normalize changed its input: %+v", in.Groups)
	}
}

func TestGroup_FindsTheExactName(t *testing.T) {
	s := Snapshot{Groups: []Group{{Name: "family"}}}
	if _, ok := s.Group("family"); !ok {
		t.Fatal("family should be found")
	}
	if _, ok := s.Group("Family"); ok {
		t.Fatal("lookup is exact: Family is not family")
	}
	if _, ok := s.Group("nope"); ok {
		t.Fatal("a missing group must not be found")
	}
}
