package lldap

import (
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// labReply is the shape of what the script prints for the lab directory: LLDAP's three system groups plus two of the site's, in the order LLDAP
// returned them (not sorted).
const labReply = `{"status":200,"data":{"groups":[` +
	`{"displayName":"lldap_admin","users":[{"id":"admin","email":"","displayName":"Administrator"}]},` +
	`{"displayName":"lldap_password_manager","users":[]},` +
	`{"displayName":"lldap_strict_readonly","users":[{"id":"authelia","email":"authelia@lab.test","displayName":"Authelia"}]},` +
	`{"displayName":"family","users":[{"id":"bob","email":"bob@lab.test","displayName":"Bob"},{"id":"alice","email":"alice@lab.test","displayName":"Alice"}]},` +
	`{"displayName":"admins","users":[{"id":"alice","email":"alice@lab.test","displayName":"Alice"}]}` +
	`]},"errors":null}`

type recorded struct {
	command []string
	stdin   string
	env     map[string]string
}

func fakeExec(code int, out string, err error, rec *recorded) Exec {
	return func(command []string, stdin io.Reader, env map[string]string) (int, string, error) {
		if rec != nil {
			b, _ := io.ReadAll(stdin)
			rec.command, rec.stdin, rec.env = command, string(b), env
		}
		return code, out, err
	}
}

func newReader(e Exec) Reader {
	return Reader{Exec: e, Instance: "lldap", User: "reader", Password: "s3cret-pw"}
}

func TestRead_ParsesTheLabDirectory(t *testing.T) {
	snap, err := newReader(fakeExec(0, labReply+"\n", nil, nil)).Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	var names []string
	for _, g := range snap.Groups {
		names = append(names, g.Name)
	}
	if got, want := strings.Join(names, ","), "admins,family,lldap_admin,lldap_password_manager,lldap_strict_readonly"; got != want {
		t.Fatalf("groups = %s, want %s (sorted)", got, want)
	}
	family, _ := snap.Group("family")
	if family.System {
		t.Error("family is the site's, not a system group")
	}
	if len(family.Members) != 2 || family.Members[0].ID != "alice" || family.Members[1].ID != "bob" || family.Members[0].Email != "alice@lab.test" {
		t.Errorf("family members = %+v, want alice then bob with emails", family.Members)
	}
	for _, n := range []string{"lldap_admin", "lldap_password_manager", "lldap_strict_readonly"} {
		g, _ := snap.Group(n)
		if !g.System {
			t.Errorf("%s should be marked System", n)
		}
	}
}

func TestRead_SendsThePasswordOnStdinOnly(t *testing.T) {
	var rec recorded
	if _, err := newReader(fakeExec(0, labReply, nil, &rec)).Read(); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.stdin != "s3cret-pw\n" {
		t.Errorf("stdin = %q, want the password and a newline", rec.stdin)
	}
	for _, a := range rec.command {
		if strings.Contains(a, "s3cret-pw") {
			t.Errorf("the password is on the command line: %q", a)
		}
	}
	for k, v := range rec.env {
		if strings.Contains(v, "s3cret-pw") {
			t.Errorf("the password is in the environment (%s)", k)
		}
	}
	if rec.env["TINK_ID_USER"] != "reader" || rec.env["TINK_ID_PORT"] != DefaultPort {
		t.Errorf("env = %v, want the user and the default port", rec.env)
	}
	if len(rec.command) != 3 || rec.command[0] != "sh" || rec.command[1] != "-c" {
		t.Errorf("command = %v, want sh -c <script>", rec.command)
	}
}

func TestRead_UsesTheConfiguredPort(t *testing.T) {
	var rec recorded
	r := newReader(fakeExec(0, labReply, nil, &rec))
	r.Port = "9999"
	if _, err := r.Read(); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.env["TINK_ID_PORT"] != "9999" {
		t.Errorf("port = %q, want 9999", rec.env["TINK_ID_PORT"])
	}
}

func TestRead_IgnoresAnythingBeforeTheReplyLine(t *testing.T) {
	if _, err := newReader(fakeExec(0, "some noise\n"+labReply+"\n", nil, nil)).Read(); err != nil {
		t.Fatalf("noise before the reply line should be ignored: %v", err)
	}
}

// Every way the read can go wrong must be an error that says which. None may come back as an empty Snapshot.
func TestRead_FailuresAreErrorsNeverAnEmptyDirectory(t *testing.T) {
	cases := []struct {
		name string
		exec Exec
		want string
	}{
		{"no curl", fakeExec(12, "tink-identity: curl is not installed in this instance\n", nil, nil), "curl is not installed"},
		{"no jq", fakeExec(13, "tink-identity: jq is not installed in this instance", nil, nil), "jq is not installed"},
		{"login refused", fakeExec(20, "tink-identity: the login was refused (HTTP 401)", nil, nil), "login was refused (HTTP 401)"},
		{"cannot reach lldap", fakeExec(15, "tink-identity: could not reach LLDAP on http://localhost:17170", nil, nil), "could not reach LLDAP"},
		{"script died with no message", fakeExec(1, "", nil, nil), "exited 1 with no message"},
		{"instance not running", fakeExec(0, "", errors.New("instance is not running"), nil), "instance is not running"},
		{"not json", fakeExec(0, "hello", nil, nil), "not the JSON expected"},
		{"http error from the query", fakeExec(0, `{"status":500,"data":null,"errors":null}`, nil, nil), "HTTP 500"},
		{"graphql errors", fakeExec(0, `{"status":200,"data":null,"errors":[{"message":"Unauthorized"}]}`, nil, nil), "Unauthorized"},
		{"no data", fakeExec(0, `{"status":200,"data":null,"errors":null}`, nil, nil), "no data"},
		{"no groups at all", fakeExec(0, `{"status":200,"data":{"groups":[]},"errors":null}`, nil, nil), "no groups at all"},
		{"unnamed group", fakeExec(0, `{"status":200,"data":{"groups":[{"displayName":"","users":[]}]},"errors":null}`, nil, nil), "no name"},
		{"member without an id", fakeExec(0, `{"status":200,"data":{"groups":[{"displayName":"g","users":[{"id":"","email":"x"}]}]},"errors":null}`, nil, nil), "member with no id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap, err := newReader(c.exec).Read()
			if err == nil {
				t.Fatalf("expected an error, got %+v", snap)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
			if !strings.Contains(err.Error(), "lldap") {
				t.Errorf("error %q should name the instance", err)
			}
			if len(snap.Groups) != 0 {
				t.Errorf("a failed read must not return groups, got %+v", snap)
			}
			if strings.Contains(err.Error(), "s3cret-pw") {
				t.Errorf("the password must never appear in an error: %q", err)
			}
		})
	}
}

// Incus puts the whole command into some of its errors; the message must say what went wrong, not repeat the script.
func TestRead_ATransportErrorDoesNotRepeatTheScript(t *testing.T) {
	boom := errors.New("starting exec of [sh -c " + script + "] on nosuch: Failed to fetch instance \"nosuch\": Instance not found")
	_, err := newReader(fakeExec(0, "", boom, nil)).Read()
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "fail()") || strings.Contains(err.Error(), "curl -sS") {
		t.Errorf("the script is in the error:\n%s", err)
	}
	if !strings.Contains(err.Error(), "Instance not found") || !strings.Contains(err.Error(), "<the lookup script>") {
		t.Errorf("error = %q, want the cause and a placeholder for the script", err)
	}
}

func TestRead_RefusesToRunWithoutWhatItNeeds(t *testing.T) {
	ok := fakeExec(0, labReply, nil, nil)
	for name, r := range map[string]Reader{
		"no exec":     {Instance: "lldap", User: "u", Password: "p"},
		"no user":     {Exec: ok, Instance: "lldap", Password: "p"},
		"no password": {Exec: ok, Instance: "lldap", User: "u"},
	} {
		if _, err := r.Read(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// The script is the part this package cannot exercise without an LLDAP instance (it is run live), but a syntax error should still fail here.
func TestScript_IsValidShell(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh on this machine")
	}
	if out, err := exec.Command(sh, "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("the script does not parse: %v\n%s", err, out)
	}
	if strings.Contains(script, "TINK_ID_PASSWORD") {
		t.Error("the password must come in on stdin, not the environment")
	}
}
