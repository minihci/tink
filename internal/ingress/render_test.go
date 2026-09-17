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
