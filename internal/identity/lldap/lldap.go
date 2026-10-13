// Package lldap reads people and groups from an LLDAP directory (github.com/lldap/lldap) that runs as an Incus instance.
//
// It does not connect to the directory over the network. It runs a fixed script inside the LLDAP instance through the Incus exec API, so it works
// from anywhere that can reach the Incus server (including under --remote) and needs no published port. The script logs in with a read-only
// directory user and runs one GraphQL query with the curl and jq the upstream image ships. The password goes in on standard input, never on a
// command line. See docs/identity-projector-design.md.
package lldap

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/minihci/tink/internal/identity"
)

// DefaultPort is LLDAP's web and API port inside its instance.
const DefaultPort = "17170"

// systemPrefix starts the name of every group LLDAP keeps for itself (lldap_admin, lldap_password_manager, lldap_strict_readonly).
const systemPrefix = "lldap_"

// Exec runs a command in the directory's instance with the given standard input and extra environment. incusapi.ExecWithStdin has this shape,
// bound to an instance.
type Exec func(command []string, stdin io.Reader, env map[string]string) (exitCode int, output string, err error)

// Reader reads the directory through Exec.
type Reader struct {
	Exec Exec
	// Instance names the directory's instance, for messages only.
	Instance string
	// User and Password are a directory account that may read groups. A member of lldap_strict_readonly can, and cannot write.
	User     string
	Password string
	// Port is LLDAP's API port inside the instance. Empty means DefaultPort.
	Port string
}

// script is run with sh in the instance. It prints one line of JSON ({status, data, errors}) on success. On failure it prints one line starting
// "tink-identity: " to standard error and exits non-zero: 12 no curl, 13 no jq, 14 no password or an unusable request, 15 cannot reach LLDAP,
// 20 login refused, 21 login reply unusable, 22 query failed, 23 reply is not JSON. The token is handed to curl through its config on standard
// input, so it is not on a command line either.
const script = `set -u
fail() { echo "tink-identity: $1" >&2; exit "$2"; }
command -v curl >/dev/null 2>&1 || fail "curl is not installed in this instance" 12
command -v jq >/dev/null 2>&1 || fail "jq is not installed in this instance" 13
IFS= read -r PW || fail "no password on standard input" 14
BASE="http://localhost:${TINK_ID_PORT}"
LOGIN=$(jq -cn --arg u "$TINK_ID_USER" --arg p "$PW" '{username:$u,password:$p}') || fail "could not build the login request" 14
OUT=$(printf '%s' "$LOGIN" | curl -sS --max-time 15 -w '\n%{http_code}' -H 'Content-Type: application/json' -d @- "$BASE/auth/simple/login") || fail "could not reach LLDAP on $BASE" 15
CODE=$(printf '%s\n' "$OUT" | tail -n 1)
[ "$CODE" = 200 ] || fail "the login was refused (HTTP $CODE)" 20
TOKEN=$(printf '%s\n' "$OUT" | sed '$d' | jq -r '.token // empty') || fail "the login reply was not JSON" 21
[ -n "$TOKEN" ] || fail "the login reply had no token" 21
QUERY='{"query":"{ groups { id displayName users { id email displayName } } }"}'
OUT=$(printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" | curl -sS --max-time 30 -K - -w '\n%{http_code}' -H 'Content-Type: application/json' -d "$QUERY" "$BASE/api/graphql") || fail "could not query LLDAP" 22
CODE=$(printf '%s\n' "$OUT" | tail -n 1)
printf '%s\n' "$OUT" | sed '$d' | jq -c --argjson status "$CODE" '{status:$status, data:(.data // null), errors:(.errors // null)}' || fail "the GraphQL reply was not JSON" 23
`

// reply is the one line of JSON the script prints.
type reply struct {
	Status int `json:"status"`
	Data   *struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Users       []struct {
				ID          string `json:"id"`
				Email       string `json:"email"`
				DisplayName string `json:"displayName"`
			} `json:"users"`
		} `json:"groups"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Read implements identity.Reader. Every way the read can fail is an error that says which; none returns an empty Snapshot.
func (r Reader) Read() (identity.Snapshot, error) {
	where := r.Instance
	if where == "" {
		where = "the directory instance"
	}
	if r.Exec == nil {
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: no way to run a command in it", where)
	}
	if r.User == "" || r.Password == "" {
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: a read-only user and its password are required", where)
	}
	port := r.Port
	if port == "" {
		port = DefaultPort
	}

	code, output, err := r.Exec([]string{"sh", "-c", script}, strings.NewReader(r.Password+"\n"), map[string]string{
		"TINK_ID_USER": r.User,
		"TINK_ID_PORT": port,
	})
	if err != nil {
		// Incus's own error repeats the whole command (here, the script) in its text; say what went wrong, not the script.
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: %s", where, strings.ReplaceAll(err.Error(), script, "<the lookup script>"))
	}
	output = strings.TrimSpace(output)
	if code != 0 {
		msg := strings.TrimPrefix(output, "tink-identity: ")
		if msg == "" {
			msg = fmt.Sprintf("the script exited %d with no message", code)
		}
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: %s", where, msg)
	}
	return parse(where, output)
}

func parse(where, output string) (identity.Snapshot, error) {
	// The reply is the last line; anything before it is not ours.
	line := output
	if i := strings.LastIndex(output, "\n"); i >= 0 {
		line = output[i+1:]
	}
	var rep reply
	if err := json.Unmarshal([]byte(line), &rep); err != nil {
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: the reply was not the JSON expected: %w", where, err)
	}
	if rep.Status != 200 {
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: LLDAP answered the query with HTTP %d", where, rep.Status)
	}
	if len(rep.Errors) > 0 {
		msgs := make([]string, len(rep.Errors))
		for i, e := range rep.Errors {
			msgs[i] = e.Message
		}
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: LLDAP refused the query: %s", where, strings.Join(msgs, "; "))
	}
	if rep.Data == nil {
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: the reply had no data", where)
	}
	if len(rep.Data.Groups) == 0 {
		// An LLDAP directory always has its three system groups, so none at all means this is not what was asked for.
		return identity.Snapshot{}, fmt.Errorf("reading groups from %s: the reply had no groups at all (LLDAP always has its own system groups)", where)
	}

	var snap identity.Snapshot
	for _, g := range rep.Data.Groups {
		if g.DisplayName == "" {
			return identity.Snapshot{}, fmt.Errorf("reading groups from %s: a group came back with no name", where)
		}
		grp := identity.Group{Name: g.DisplayName, System: strings.HasPrefix(g.DisplayName, systemPrefix)}
		for _, u := range g.Users {
			if u.ID == "" {
				return identity.Snapshot{}, fmt.Errorf("reading groups from %s: group %q has a member with no id", where, g.DisplayName)
			}
			grp.Members = append(grp.Members, identity.Person{ID: u.ID, Email: u.Email, DisplayName: u.DisplayName})
		}
		snap.Groups = append(snap.Groups, grp)
	}
	return snap.Normalize(), nil
}
