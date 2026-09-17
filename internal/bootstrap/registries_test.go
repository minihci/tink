package bootstrap

import "testing"

func TestRegistryHostAndPath(t *testing.T) {
	cases := []struct {
		in         string
		host, path string
	}{
		{"ghcr.io/wyomarus", "ghcr.io", "wyomarus/"},
		{"127.0.0.1:5000", "127.0.0.1:5000", ""},
		{"docker.io/library", "docker.io", "library/"},
	}
	for _, c := range cases {
		host, path := registryHostAndPath(c.in)
		if host != c.host || path != c.path {
			t.Errorf("registryHostAndPath(%q) = (%q, %q), want (%q, %q)", c.in, host, path, c.host, c.path)
		}
	}
}
