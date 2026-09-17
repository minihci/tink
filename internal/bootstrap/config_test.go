package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTempEnv(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "deploy.env")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp env: %v", err)
	}
	return path
}

func TestLoadConfig_Basic(t *testing.T) {
	path := writeTempEnv(t, `
# a comment
INCUS_UI_DOMAIN=incus.example.com
AUTH_DOMAIN=auth.example.com
BRIDGE_NETWORK=incusbr0
INCUS_API_ADDR=10.0.0.1:9443
AUTHELIA_STATIC_IP=10.0.0.20
INCUS_UI_STATIC_IP=10.0.0.21
STORAGE_POOL=default
ADMIN_USERNAME=admin
ADMIN_EMAIL=admin@example.com
IMAGE_REGISTRY=ghcr.io/example
`)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig returned error: %v", err)
	}
	if cfg.IncusUIDomain != "incus.example.com" || cfg.AdminEmail != "admin@example.com" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got: %v", err)
	}
}

func TestLoadConfig_RejectsUnrecognizedKey(t *testing.T) {
	path := writeTempEnv(t, "SOME_TYPO_KEY=value\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for an unrecognized key, got nil")
	}
}

func TestLoadConfig_RejectsMalformedLine(t *testing.T) {
	path := writeTempEnv(t, "not a valid line at all\n")
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected an error for a line without '=', got nil")
	}
}

func TestValidate_ReportsAllMissingAtOnce(t *testing.T) {
	cfg := Config{IncusUIDomain: "incus.example.com"}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for a mostly-empty config, got nil")
	}
	// Spot check a couple of the fields we know are missing show up.
	msg := err.Error()
	for _, want := range []string{"AUTH_DOMAIN", "STORAGE_POOL"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected error to mention %s, got: %s", want, msg)
		}
	}
}
