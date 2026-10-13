package headscale

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// labUsers is what `headscale users list -o json` printed on the lab Headscale: email and display_name are left out when empty.
const labUsers = `[
	{
		"id": 1,
		"name": "alice",
		"created_at": {"seconds": 1791880989, "nanos": 739303188}
	},
	{
		"id": 3,
		"name": "bob",
		"created_at": {"seconds": 1791881074, "nanos": 583118687}
	}
]`

func fakeUsersExec(code int, out string, err error, got *[]string) func([]string) (int, string, error) {
	return func(command []string) (int, string, error) {
		if got != nil {
			*got = command
		}
		return code, out, err
	}
}

func TestUsersReader_ParsesTheLabOutputAndRunsTheRightCommand(t *testing.T) {
	var ran []string
	users, err := UsersReader{Exec: fakeUsersExec(0, labUsers, nil, &ran), Instance: "headscale"}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Name != "alice" || users[0].ID != 1 || users[1].Name != "bob" || users[1].ID != 3 {
		t.Errorf("users = %+v", users)
	}
	want := []string{"/ko-app/headscale", "-c", "/etc/headscale.yaml", "users", "list", "-o", "json"}
	if !reflect.DeepEqual(ran, want) {
		t.Errorf("command = %v, want %v", ran, want)
	}
}

func TestUsersReader_UsesTheConfiguredBinaryAndConfig(t *testing.T) {
	var ran []string
	_, err := UsersReader{Exec: fakeUsersExec(0, "[]", nil, &ran), Binary: "/usr/bin/headscale", Config: "/etc/h.yaml"}.Read()
	if err != nil {
		t.Fatal(err)
	}
	if ran[0] != "/usr/bin/headscale" || ran[2] != "/etc/h.yaml" {
		t.Errorf("command = %v", ran)
	}
}

func TestUsersReader_NoUsersYetIsARealAnswer(t *testing.T) {
	users, err := UsersReader{Exec: fakeUsersExec(0, "[]", nil, nil)}.Read()
	if err != nil || len(users) != 0 {
		t.Fatalf("users %v err %v, want none and no error", users, err)
	}
}

func TestUsersReader_FailuresAreErrors(t *testing.T) {
	for name, c := range map[string]struct {
		exec func([]string) (int, string, error)
		want string
	}{
		"command failed":  {fakeUsersExec(1, "Error: could not connect to the socket", nil, nil), "could not connect to the socket"},
		"failed silently": {fakeUsersExec(2, "", nil, nil), "exited 2 with no message"},
		"instance gone":   {fakeUsersExec(0, "", errors.New("Instance not found"), nil), "Instance not found"},
		"not json":        {fakeUsersExec(0, "hello", nil, nil), "no JSON array"},
		"broken json":     {fakeUsersExec(0, "[{", nil, nil), "not the JSON expected"},
		"null":            {fakeUsersExec(0, "null", nil, nil), "no JSON array"},
	} {
		t.Run(name, func(t *testing.T) {
			users, err := UsersReader{Exec: c.exec, Instance: "headscale"}.Read()
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "headscale") {
				t.Fatalf("err = %v, want one mentioning %q and the instance", err, c.want)
			}
			if users != nil {
				t.Errorf("a failed read must not return users: %v", users)
			}
		})
	}
	if _, err := (UsersReader{}).Read(); err == nil {
		t.Error("no Exec must be an error")
	}
}

func TestUsersReader_IgnoresALogLineBeforeTheArray(t *testing.T) {
	if _, err := (UsersReader{Exec: fakeUsersExec(0, "some log line\n"+labUsers, nil, nil)}).Read(); err != nil {
		t.Fatal(err)
	}
}

func projected(t *testing.T, groups ...string) Result {
	t.Helper()
	res := Project(labSnapshot(), Options{Groups: groups})
	if res.HasErrors() {
		t.Fatalf("projection: %s", findings(res))
	}
	return res
}

func TestProject_KeepsTheEmittedMembersForChecks(t *testing.T) {
	res := projected(t, "family", "admins")
	want := []GroupMembers{{"admins", []string{"alice@"}}, {"family", []string{"alice@", "bob@"}}}
	if !reflect.DeepEqual(res.Groups, want) {
		t.Errorf("Groups = %+v, want %+v", res.Groups, want)
	}
}

func TestCrossCheck_AMemberWithNoHeadscaleUserIsAWarning(t *testing.T) {
	res := projected(t, "family", "admins")
	got := CrossCheck(res, []User{{Name: "alice"}})
	if len(got) != 1 || got[0].Severity != Warning || !strings.Contains(got[0].Message, `group "family": "bob@" matches no Headscale user`) {
		t.Fatalf("findings = %v", got)
	}
}

func TestCrossCheck_EveryMemberPresentIsQuiet(t *testing.T) {
	res := projected(t, "family", "admins")
	if got := CrossCheck(res, []User{{Name: "alice"}, {Name: "bob"}}); len(got) != 0 {
		t.Errorf("findings = %v, want none", got)
	}
}

func TestCrossCheck_AnEmailFormatMemberMatchesByEmail(t *testing.T) {
	res := Project(labSnapshot(), Options{Groups: []string{"admins"}, MemberFormat: "{email}"})
	if got := CrossCheck(res, []User{{Name: "someone-else", Email: "alice@lab.test"}}); len(got) != 0 {
		t.Errorf("findings = %v, want none", got)
	}
	if got := CrossCheck(res, []User{{Name: "alice"}}); len(got) != 1 {
		t.Errorf("a name-only user must not match an email member: %v", got)
	}
}

func TestCrossCheck_NoUsersAtAllIsOneWarningNotOnePerMember(t *testing.T) {
	res := projected(t, "family", "admins")
	got := CrossCheck(res, nil)
	if len(got) != 1 || !strings.Contains(got[0].Message, "no users yet") {
		t.Fatalf("findings = %v", got)
	}
}

func TestCrossCheck_NothingProjectedNothingToCheck(t *testing.T) {
	if got := CrossCheck(Result{}, nil); len(got) != 0 {
		t.Errorf("findings = %v", got)
	}
}
