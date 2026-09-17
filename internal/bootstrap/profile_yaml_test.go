package bootstrap

import "testing"

// realIngressProfileYAML is a verbatim copy of incus-host's
// ingress/ingress.profile.yaml (config+devices only, comments trimmed) --
// embedded rather than read from a sibling checkout so this test stays
// portable (CI here doesn't have incus-host checked out) while still
// exercising the parser against real, not synthetic, profile shape.
const realIngressProfileYAML = `name: ingress
description: Shared public edge — owns :80/:443, routes from routes/*.caddy (OCI application container)
config:
  boot.autorestart: "true"
  limits.cpu: "1"
  limits.memory: 256MiB
  environment.ACME_EMAIL: "${ADMIN_EMAIL}"
  environment.INCUS_UI_DOMAIN: "${INCUS_UI_DOMAIN}"
  environment.INCUS_UI_ADDR: "${INCUS_UI_STATIC_IP}:80"
  environment.AUTH_DOMAIN: "${AUTH_DOMAIN}"
  environment.AUTHELIA_ADDR: "${AUTHELIA_STATIC_IP}:9091"
devices:
  http:
    connect: tcp:127.0.0.1:80
    listen: tcp:0.0.0.0:80
    type: proxy
  https:
    connect: tcp:127.0.0.1:443
    listen: tcp:0.0.0.0:443
    type: proxy
  caddy-data:
    type: disk
    source: ingress-caddy-data
    path: /data
    pool: "${STORAGE_POOL}"
  routes:
    type: disk
    source: ingress-routes
    path: /etc/caddy/routes
    pool: "${STORAGE_POOL}"
`

func TestParseProfileYAML_RealIngressProfileShape(t *testing.T) {
	path := writeTemp(t, "ingress.profile.yaml", realIngressProfileYAML)
	cfg := Config{
		AdminEmail:        "admin@example.com",
		IncusUIDomain:     "incus.example.com",
		AuthDomain:        "auth.example.com",
		IncusUIStaticIP:   "10.77.0.21",
		AuthelialStaticIP: "10.77.0.20",
		StoragePool:       "default",
	}

	put, err := parseProfileYAML(path, cfg)
	if err != nil {
		t.Fatalf("parseProfileYAML returned error: %v", err)
	}

	wantConfig := map[string]string{
		"boot.autorestart":            "true",
		"limits.cpu":                  "1",
		"limits.memory":               "256MiB",
		"environment.ACME_EMAIL":      "admin@example.com",
		"environment.INCUS_UI_DOMAIN": "incus.example.com",
		"environment.INCUS_UI_ADDR":   "10.77.0.21:80",
		"environment.AUTH_DOMAIN":     "auth.example.com",
		"environment.AUTHELIA_ADDR":   "10.77.0.20:9091",
	}
	for k, want := range wantConfig {
		if got := put.Config[k]; got != want {
			t.Errorf("Config[%q] = %q, want %q", k, got, want)
		}
	}
	if len(put.Config) != len(wantConfig) {
		t.Errorf("Config has %d keys, want %d: %+v", len(put.Config), len(wantConfig), put.Config)
	}

	for _, deviceName := range []string{"http", "https", "caddy-data", "routes"} {
		if _, ok := put.Devices[deviceName]; !ok {
			t.Errorf("expected a %q device, got devices: %+v", deviceName, put.Devices)
		}
	}
	if got := put.Devices["caddy-data"]["pool"]; got != "default" {
		t.Errorf("caddy-data device pool = %q, want %q", got, "default")
	}
}
