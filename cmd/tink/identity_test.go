package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/identity"
)

type fakeReader struct {
	snap identity.Snapshot
	err  error
}

func (f fakeReader) Read() (identity.Snapshot, error) { return f.snap, f.err }

func labDirectory() fakeReader {
	return fakeReader{snap: identity.Snapshot{Groups: []identity.Group{
		{Name: "lldap_admin", System: true, Members: []identity.Person{{ID: "admin"}}},
		{Name: "family", Members: []identity.Person{{ID: "bob", Email: "bob@lab.test"}, {ID: "alice", Email: "alice@lab.test"}}},
		{Name: "admins", Members: []identity.Person{{ID: "alice", Email: "alice@lab.test"}}},
		{Name: "kids"},
	}}}
}

func runGroups(t *testing.T, rd identity.Reader, o identityGroupsOptions) (stdout, stderr string, err error) {
	t.Helper()
	var out, errw bytes.Buffer
	err = runIdentityGroups(&out, &errw, rd, o)
	return out.String(), errw.String(), err
}

func TestIdentityGroups_TableListsEveryGroupAndMarksSystemOnes(t *testing.T) {
	out, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "table"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"GROUP", "admins", "family", "alice, bob", "lldap_admin (system)", "kids"} {
		if !strings.Contains(out, want) {
			t.Errorf("table is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "kids") || !strings.Contains(out, "-") {
		t.Errorf("an empty group should show a dash:\n%s", out)
	}
}

func TestIdentityGroups_GroupFlagFiltersAndRejectsUnknownNames(t *testing.T) {
	out, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "table", Groups: []string{"family"}})
	if err != nil || strings.Contains(out, "admins") || !strings.Contains(out, "family") {
		t.Fatalf("filter failed: err %v out\n%s", err, out)
	}
	if _, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "json", Groups: []string{"nope"}}); err == nil || !strings.Contains(err.Error(), `"nope" is not in the directory`) {
		t.Fatalf("want an unknown-group error, got %v", err)
	}
}

func TestIdentityGroups_JSONIsStable(t *testing.T) {
	out, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "json", Groups: []string{"kids", "family"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "groups": [
    {
      "name": "family",
      "system": false,
      "members": [
        {
          "id": "alice",
          "email": "alice@lab.test",
          "displayName": ""
        },
        {
          "id": "bob",
          "email": "bob@lab.test",
          "displayName": ""
        }
      ]
    },
    {
      "name": "kids",
      "system": false,
      "members": []
    }
  ]
}
`
	if out != want {
		t.Errorf("json =\n%s\nwant\n%s", out, want)
	}
}

func TestIdentityGroups_HeadscalePrintsTheFragmentAndFindingsGoToStderr(t *testing.T) {
	out, errw, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "headscale", Groups: []string{"family", "kids"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"group:family": ["alice@", "bob@"],`) || !strings.Contains(out, `"group:kids": [],`) {
		t.Errorf("fragment:\n%s", out)
	}
	if !strings.Contains(errw, `warning: group "kids" has no members`) || strings.Contains(out, "warning") {
		t.Errorf("the warning belongs on stderr only: stderr %q stdout %q", errw, out)
	}
}

func TestIdentityGroups_HeadscaleErrorPrintsNoFragmentAndFails(t *testing.T) {
	out, errw, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "headscale", Groups: []string{"nope"}})
	if err == nil {
		t.Fatal("want an error")
	}
	if out != "" {
		t.Errorf("no fragment may be printed after an error, got %q", out)
	}
	if !strings.Contains(errw, `error: group "nope" is not in the directory`) {
		t.Errorf("stderr = %q", errw)
	}
}

func TestIdentityGroups_CheckPrintsNoFragment(t *testing.T) {
	out, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "headscale", Groups: []string{"family"}, Check: true})
	if err != nil || out != "" {
		t.Fatalf("--check: err %v out %q", err, out)
	}
}

func TestIdentityGroups_ARefusedReadIsAnErrorAndPrintsNothing(t *testing.T) {
	for _, f := range []string{"table", "json", "headscale"} {
		out, _, err := runGroups(t, fakeReader{err: errors.New("login was refused")}, identityGroupsOptions{Format: f})
		if err == nil || out != "" {
			t.Errorf("%s: err %v out %q, want an error and no output", f, err, out)
		}
	}
}

func TestIdentityGroups_OptionChecks(t *testing.T) {
	if _, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "yaml"}); err == nil {
		t.Error("an unknown format must be refused")
	}
	if _, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "table", Check: true}); err == nil {
		t.Error("--check applies to headscale only")
	}
	if _, _, err := runGroups(t, labDirectory(), identityGroupsOptions{Format: "json", MemberFormat: "{id}@"}); err == nil {
		t.Error("--member-format applies to headscale only")
	}
}
