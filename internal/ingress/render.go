package ingress

import (
	"bytes"
	"fmt"
	"text/template"
)

// routeTemplate matches the hand-written routes in
// incus-host/ingress/routes/ byte-for-byte in shape, deliberately, so a
// generated file and a hand-written one are indistinguishable to Caddy.
// {remote_host} below is Caddy's own placeholder syntax, not a Go
// template action -- single braces pass through text/template untouched.
var routeTemplate = template.Must(template.New("route").Parse(
	`{{.Domain}} {
	encode zstd gzip

	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		X-Content-Type-Options "nosniff"
		X-Frame-Options "SAMEORIGIN"
		Referrer-Policy "same-origin"
		-Server
	}

	reverse_proxy http://{{.Address}}:{{.Port}} {
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
`))

// Render produces the desired set of generated/*.caddy files, keyed by
// filename, for the given registrations.
func Render(regs []Registration) (map[string]string, error) {
	files := make(map[string]string, len(regs))
	for _, reg := range regs {
		var buf bytes.Buffer
		if err := routeTemplate.Execute(&buf, reg); err != nil {
			return nil, fmt.Errorf("rendering route for %s: %w", reg.Name, err)
		}
		files[reg.Name+".caddy"] = buf.String()
	}
	return files, nil
}
