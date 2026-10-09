package ingress

import "testing"

func TestRender_MatchesHandWrittenRouteShape(t *testing.T) {
	files, err := Render([]Registration{
		{Name: "ns-caddy", Domain: "ns.xlii.co", Port: "80", Address: "10.77.20.32"},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	content, ok := files["ns-caddy.caddy"]
	if !ok {
		t.Fatalf("expected a ns-caddy.caddy entry, got keys %v", keysOf(files))
	}

	want := `ns.xlii.co {
	encode zstd gzip

	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		X-Content-Type-Options "nosniff"
		X-Frame-Options "SAMEORIGIN"
		Referrer-Policy "same-origin"
		-Server
	}

	reverse_proxy http://10.77.20.32:80 {
		header_up X-Real-IP {remote_host}
	}

	log {
		output file /data/access.log {
			roll_size 10MiB
			roll_keep 5
		}
		format json
	}
}
`
	if content != want {
		t.Fatalf("rendered content mismatch:\ngot:\n%s\nwant:\n%s", content, want)
	}
}

func TestRender_PrefixesNonDefaultProjectFilenames(t *testing.T) {
	files, err := Render([]Registration{
		{Name: "ns-caddy", Project: "default", Domain: "ns.xlii.co", Port: "80", Address: "10.77.20.32"},
		{Name: "ns-caddy", Project: "nightscout", Domain: "ns-staging.xlii.co", Port: "80", Address: "10.135.20.32"},
	})
	if err != nil {
		t.Fatalf("Render returned error: %v", err)
	}

	for _, want := range []string{"ns-caddy.caddy", "nightscout_ns-caddy.caddy"} {
		if _, ok := files[want]; !ok {
			t.Fatalf("expected a %q entry, got keys %v", want, keysOf(files))
		}
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestRender_PutsAnAutheliaRouteBehindForwardAuth(t *testing.T) {
	files, err := Render([]Registration{{Name: "ns", Domain: "ns.example.com", Port: "1337", Address: "10.0.0.5", Auth: "authelia"}})
	if err != nil {
		t.Fatal(err)
	}
	got := files["ns.caddy"]
	want := `	forward_auth {$AUTHELIA_ADDR} {
		uri /api/authz/forward-auth
		copy_headers Remote-User Remote-Groups Remote-Email Remote-Name
	}

	reverse_proxy http://10.0.0.5:1337 {`
	if !contains(got, want) {
		t.Errorf("the sign-in check must come before the proxy:\n%s", got)
	}
	// a route with no auth is byte-for-byte what it always was
	plain, _ := Render([]Registration{{Name: "ns", Domain: "ns.example.com", Port: "1337", Address: "10.0.0.5"}})
	if contains(plain["ns.caddy"], "forward_auth") {
		t.Errorf("no auth, no forward_auth:\n%s", plain["ns.caddy"])
	}
	// and an auth value tink does not know adds nothing
	other, _ := Render([]Registration{{Name: "ns", Domain: "ns.example.com", Port: "1337", Address: "10.0.0.5", Auth: "authentik"}})
	if contains(other["ns.caddy"], "forward_auth") {
		t.Errorf("only a value tink knows changes the route:\n%s", other["ns.caddy"])
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
