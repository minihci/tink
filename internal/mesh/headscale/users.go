package headscale

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultBinary and DefaultConfig are where the upstream image and tink's example stack put Headscale's command and configuration.
const (
	DefaultBinary = "/ko-app/headscale"
	DefaultConfig = "/etc/headscale.yaml"
)

// User is a Headscale user, as `headscale users list` reports it.
type User struct {
	ID          uint64
	Name        string
	DisplayName string
	Email       string
}

// UsersReader lists Headscale's users by running its own command inside its instance, through the Incus exec API, so it works from anywhere that
// can reach the server and needs no key: the command talks to Headscale over the local socket.
type UsersReader struct {
	// Exec runs a command in the Headscale instance. incusapi.ExecInGuest has this shape, bound to an instance.
	Exec func(command []string) (exitCode int, output string, err error)
	// Instance names the instance, for messages only.
	Instance string
	// Binary and Config default to DefaultBinary and DefaultConfig.
	Binary string
	Config string
}

// Read lists the users. A failure is an error: an empty list from a command that succeeded is real (a Headscale with no users yet), but a command
// that failed must never look like one.
func (r UsersReader) Read() ([]User, error) {
	where := r.Instance
	if where == "" {
		where = "the Headscale instance"
	}
	if r.Exec == nil {
		return nil, fmt.Errorf("listing users on %s: no way to run a command in it", where)
	}
	bin, cfg := r.Binary, r.Config
	if bin == "" {
		bin = DefaultBinary
	}
	if cfg == "" {
		cfg = DefaultConfig
	}
	code, out, err := r.Exec([]string{bin, "-c", cfg, "users", "list", "-o", "json"})
	if err != nil {
		return nil, fmt.Errorf("listing users on %s: %w", where, err)
	}
	out = strings.TrimSpace(out)
	if code != 0 {
		if out == "" {
			out = fmt.Sprintf("the command exited %d with no message", code)
		}
		return nil, fmt.Errorf("listing users on %s: %s", where, out)
	}
	return parseUsers(where, out)
}

func parseUsers(where, out string) ([]User, error) {
	// The reply is a JSON array; anything before it (a log line) is not ours.
	i := strings.Index(out, "[")
	if i < 0 {
		return nil, fmt.Errorf("listing users on %s: the command printed no JSON array", where)
	}
	out = out[i:]
	var raw []struct {
		ID          uint64 `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("listing users on %s: the output was not the JSON expected: %w", where, err)
	}
	users := make([]User, len(raw))
	for i, u := range raw {
		users[i] = User{ID: u.ID, Name: u.Name, DisplayName: u.DisplayName, Email: u.Email}
	}
	return users, nil
}

// CrossCheck compares the members a projection wrote with the users Headscale has, and returns a warning for each member that matches none.
//
// Headscale accepts such a member without complaint (policy check calls the policy valid and a reload completes) and it simply matches nobody, so a
// typo, or a person who has no Headscale user yet, would otherwise go unnoticed. It is a warning because Headscale does not mind and the user may
// be created later. A member matches a user by "<name>@" (how Headscale's documentation writes a user) or by the user's email.
func CrossCheck(res Result, users []User) []Finding {
	known := map[string]bool{}
	for _, u := range users {
		if u.Name != "" {
			known[u.Name+"@"] = true
		}
		if u.Email != "" {
			known[u.Email] = true
		}
	}
	var out []Finding
	if len(users) == 0 && len(res.Groups) > 0 {
		out = append(out, Finding{Warning, "Headscale has no users yet, so no member of any group can match"})
		return out
	}
	for _, g := range res.Groups {
		for _, m := range g.Members {
			if !known[m] {
				out = append(out, Finding{Warning, fmt.Sprintf("group %q: %q matches no Headscale user (Headscale accepts it and it matches nobody)", g.Name, m)})
			}
		}
	}
	return out
}
