package resolve

import (
	"fmt"
	"strings"
)

// A kind: file with `derive:` is a file around a region that tink writes (docs/join.md, "Derived files"; the proposal is #130). The file is the
// person's: the proxy's other routes, the sign-on helper's other settings. The region is the join's rendering of the published names, and it is the
// only part that changes with them.
//
//	kind: file
//	name: ingress-routes
//	instance: ingress
//	path: /etc/caddy/apps.caddy
//	derive: caddy
//	content: |
//	  # BEGIN tink derive
//	  # END tink derive
//
// Before the stack is planned, internal/derive replaces what lies between the two marker lines with the rendering, indented as the first marker
// is, and the file is then an ordinary file whose content is the filled one: planned, pushed, and restarted for (restart: true) like any other.
const (
	DeriveCaddy = "caddy" // the join's `caddy` rendering: routes for the proxy's wildcard site
	DeriveSSO   = "sso"   // the join's `sso` rendering: rules for the sign-on helper's access control

	DeriveBegin = "# BEGIN tink derive"
	DeriveEnd   = "# END tink derive"
)

func validateDerivedFile(r Resource) error {
	if r.Kind != KindFile || r.Derive == "" {
		return nil
	}
	switch r.Derive {
	case DeriveCaddy, DeriveSSO:
	default:
		return fmt.Errorf("resource %q: derive %q is not one of %s, %s", r.Name, r.Derive, DeriveCaddy, DeriveSSO)
	}
	if _, err := FillDerived(r.Content, ""); err != nil {
		return fmt.Errorf("resource %q: %w", r.Name, err)
	}
	return nil
}

// FillDerived returns content with the region between its DeriveBegin and DeriveEnd lines replaced by rendering. Each line of the rendering takes the
// indentation of the DeriveBegin line, so a region inside a YAML list stays inside it. Each marker is a line of its own (surrounding space is
// ignored), appears once, and the begin comes first; anything else is an error that says what is wrong, because a region that cannot be found
// would silently leave the file without its routes.
func FillDerived(content, rendering string) (string, error) {
	lines := strings.Split(content, "\n")
	begin, end := -1, -1
	for i, l := range lines {
		switch strings.TrimSpace(l) {
		case DeriveBegin:
			if begin >= 0 {
				return "", fmt.Errorf("%q appears twice: a derived file has one region", DeriveBegin)
			}
			begin = i
		case DeriveEnd:
			if end >= 0 {
				return "", fmt.Errorf("%q appears twice: a derived file has one region", DeriveEnd)
			}
			end = i
		}
	}
	switch {
	case begin < 0 && end < 0:
		return "", fmt.Errorf("a file with derive: needs a line %q and a line %q around the region tink writes", DeriveBegin, DeriveEnd)
	case begin < 0:
		return "", fmt.Errorf("the line %q has no %q before it", DeriveEnd, DeriveBegin)
	case end < 0:
		return "", fmt.Errorf("the line %q has no %q after it", DeriveBegin, DeriveEnd)
	case end < begin:
		return "", fmt.Errorf("%q comes before %q", DeriveEnd, DeriveBegin)
	}
	indent := lines[begin][:len(lines[begin])-len(strings.TrimLeft(lines[begin], " \t"))]
	out := append([]string{}, lines[:begin+1]...)
	for _, l := range strings.Split(strings.TrimRight(rendering, "\n"), "\n") {
		if l != "" {
			out = append(out, indent+l)
		}
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}
