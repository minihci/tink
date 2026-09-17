package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

func TestRender_SubstitutesKnownVars(t *testing.T) {
	path := writeTemp(t, "profile.yaml", "domain: ${INCUS_UI_DOMAIN}\nemail: ${ADMIN_EMAIL}\n")
	cfg := Config{IncusUIDomain: "incus.example.com", AdminEmail: "admin@example.com"}

	out, err := render(path, cfg)
	if err != nil {
		t.Fatalf("render returned error: %v", err)
	}
	want := "domain: incus.example.com\nemail: admin@example.com\n"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestRender_ErrorsOnUnrecognizedVar(t *testing.T) {
	path := writeTemp(t, "profile.yaml", "domain: ${SOME_TYPO}\n")
	if _, err := render(path, Config{}); err == nil {
		t.Fatal("expected an error for an unrecognized ${SOME_TYPO}, got nil")
	}
}

func TestRenderServerConfig_SplicesIndentedScriptlet(t *testing.T) {
	serverConfig := writeTemp(t, "server-config.yaml", strings.Join([]string{
		"config:",
		"  core.https_address: \"${INCUS_API_ADDR}\"",
		"  authorization.scriptlet: |",
		"__AUTHORIZATION_SCRIPTLET__",
		"",
	}, "\n"))
	scriptlet := writeTemp(t, "authorization.star", "def authorize(details, object, entitlement):\n    return True\n")

	out, err := renderServerConfig(serverConfig, scriptlet, Config{IncusAPIAddr: "10.0.0.1:9443"})
	if err != nil {
		t.Fatalf("renderServerConfig returned error: %v", err)
	}

	want := strings.Join([]string{
		"config:",
		"  core.https_address: \"10.0.0.1:9443\"",
		"  authorization.scriptlet: |",
		"    def authorize(details, object, entitlement):",
		"        return True",
		"",
	}, "\n")
	if out != want {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
}

func TestRenderServerConfig_ErrorsWithoutSentinel(t *testing.T) {
	serverConfig := writeTemp(t, "server-config.yaml", "config:\n  foo: bar\n")
	scriptlet := writeTemp(t, "authorization.star", "return True\n")

	if _, err := renderServerConfig(serverConfig, scriptlet, Config{}); err == nil {
		t.Fatal("expected an error when the sentinel line is missing, got nil")
	}
}
