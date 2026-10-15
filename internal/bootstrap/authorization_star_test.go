package bootstrap

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v4"
)

// The tests in this file read the files `tink deploy` really ships
// (configs/daemon), not fixtures. The authorization scriptlet is Starlark that
// Incus only evaluates when a request is made, so a wrong field path is not
// caught by `incus config set` and, before this file, by nothing in CI. They
// cannot say the script is right for a given Incus version (that is a
// throwaway-server check at every upgrade, written in the script's header);
// they pin the shape that was measured on Incus 7.5.1 so it cannot regress
// unnoticed.

var shippedDaemonDir = filepath.Join("..", "..", "configs", "daemon")

func readShipped(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(shippedDaemonDir, name))
	if err != nil {
		t.Fatalf("reading shipped %s: %v", name, err)
	}
	return string(b)
}

// starlarkCode returns src with comments removed (a '#' outside a string
// literal to the end of the line), keeping line breaks.
func starlarkCode(src string) string {
	var out strings.Builder
	for _, line := range strings.Split(src, "\n") {
		var quote rune
		cut := len(line)
		for i, r := range line {
			if quote != 0 {
				if r == quote {
					quote = 0
				}
				continue
			}
			if r == '"' || r == '\'' {
				quote = r
				continue
			}
			if r == '#' {
				cut = i
				break
			}
		}
		out.WriteString(line[:cut])
		out.WriteString("\n")
	}
	return out.String()
}

func TestAuthorizationStar_UsesTheFlatDetailsShape(t *testing.T) {
	code := starlarkCode(readShipped(t, "authorization.star"))

	// Incus 7.5.1 passes a flat `details`; `details.RequestDetails` does not
	// exist there and fails with `Invalid field "RequestDetails"` when a
	// request is made. The header comment may name the old path; the code
	// may not use it.
	if strings.Contains(code, "RequestDetails") {
		t.Errorf("authorization.star's code reads RequestDetails, which Incus 7.5.1 does not have; use details.Protocol")
	}
	if !strings.Contains(code, "details.Protocol") {
		t.Errorf("authorization.star's code never reads details.Protocol")
	}

	// Every field the script reads must be one of the seven measured on
	// Incus 7.5.1, so a typo or a field from another version fails here
	// instead of on a live request.
	known := map[string]bool{
		"Certificate": true, "Chain": true, "Claims": true,
		"IsAllProjectsRequest": true, "ProjectName": true,
		"Protocol": true, "Username": true,
	}
	for _, m := range regexp.MustCompile(`\bdetails\.(\w+)`).FindAllStringSubmatch(code, -1) {
		if !known[m[1]] {
			t.Errorf("authorization.star reads details.%s, which is not a field measured on Incus 7.5.1", m[1])
		}
	}
}

func TestAuthorizationStar_LooksLikeStarlark(t *testing.T) {
	src := readShipped(t, "authorization.star")

	if strings.TrimSpace(src) == "" {
		t.Fatal("authorization.star is empty")
	}
	if !strings.HasSuffix(src, "\n") {
		t.Error("authorization.star must end with a newline")
	}
	if strings.Contains(src, "\r") {
		t.Error("authorization.star must use LF line endings")
	}
	if strings.Contains(src, "\t") {
		t.Error("authorization.star must indent with spaces, not tabs")
	}

	code := starlarkCode(src)
	if !regexp.MustCompile(`(?m)^def authorize\(details, object, entitlement\):$`).MatchString(code) {
		t.Error("authorization.star must define authorize(details, object, entitlement) at top level")
	}

	// Every code line is the def or is indented under it.
	for i, line := range strings.Split(code, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "def authorize(") {
			t.Errorf("authorization.star line %d is top-level code other than the def: %q", i+1, line)
		}
	}

	// Brackets balance (strings and comments already skipped by starlarkCode
	// for comments; string contents are skipped here).
	var stack []rune
	pairs := map[rune]rune{')': '(', ']': '[', '}': '{'}
	var quote rune
	for _, r := range code {
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '"', '\'':
			quote = r
		case '(', '[', '{':
			stack = append(stack, r)
		case ')', ']', '}':
			if len(stack) == 0 || stack[len(stack)-1] != pairs[r] {
				t.Fatalf("authorization.star: unbalanced %q", r)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if len(stack) != 0 || quote != 0 {
		t.Errorf("authorization.star: unclosed bracket or string (%q left open)", string(stack))
	}
}

// What `tink deploy` really writes: the shipped template with the shipped
// script spliced in must be valid YAML whose scriptlet is the script, and it
// routes the oidc and tls classes to the scriptlet and keeps tls-restricted on
// Incus's own check (the certificate's project list). The docs (README.md,
// configs/README.md) and the comments in both files describe exactly this
// routing, so changing it means changing them in the same commit.
func TestShippedServerConfig_RoutesOIDCAndTLSToTheScriptletAndKeepsRestrictedOnTheProjectList(t *testing.T) {
	script := readShipped(t, "authorization.star")

	rendered, err := renderServerConfig(
		filepath.Join(shippedDaemonDir, "server-config.yaml"),
		filepath.Join(shippedDaemonDir, "authorization.star"),
		Config{AuthDomain: "auth.example.com", IncusAPIAddr: "10.0.0.1:8443", IncusUIStaticIP: "10.0.0.2"},
	)
	if err != nil {
		t.Fatalf("rendering the shipped server config: %v", err)
	}

	var doc struct {
		Config map[string]string `yaml:"config"`
	}
	if err := yaml.Unmarshal([]byte(rendered), &doc); err != nil {
		t.Fatalf("the rendered server config is not valid YAML: %v\n%s", err, rendered)
	}

	if got := doc.Config["authorization.scriptlet"]; got != script {
		t.Errorf("the spliced scriptlet differs from authorization.star:\n got: %q\nwant: %q", got, script)
	}
	for _, class := range []string{"oidc", "tls"} {
		if got := doc.Config["authorization.client."+class]; got != "scriptlet" {
			t.Errorf("authorization.client.%s = %q, want \"scriptlet\" (update the docs if this is deliberate)", class, got)
		}
	}
	// The script answers True for every tls client, so a restricted certificate routed to it would see every project; "tls" is Incus's own
	// check, the certificate's project list.
	if got := doc.Config["authorization.client.tls-restricted"]; got != "tls" {
		t.Errorf("authorization.client.tls-restricted = %q, want \"tls\": routed to the scriptlet, a restricted certificate gets full access instead of its project list (update the docs if this is deliberate)", got)
	}
	for _, class := range []string{"unix", "default"} {
		if got, set := doc.Config["authorization.client."+class]; set {
			t.Errorf("authorization.client.%s = %q is set: routing the local socket or the default class to a script that denies non-oidc/tls clients needs a deliberate decision", class, got)
		}
	}
}
