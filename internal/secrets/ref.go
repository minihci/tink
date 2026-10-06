package secrets

import (
	"fmt"
	"strings"
)

// The reference syntax: ${secret:NAME} inside a string. A `$` directly before it escapes it, so
// $${secret:NAME} is the literal text ${secret:NAME}. Only a handful of fields expand references
// (see internal/resolve); everywhere else one is an error.
const (
	refOpen  = "${secret:"
	refClose = "}"
)

// HasRef reports whether s contains the reference opener, escaped or not. Fields that do not
// expand references use it to refuse one rather than pass it through as a literal.
func HasRef(s string) bool { return strings.Contains(s, refOpen) }

type refToken struct {
	text  string // literal text, when name is empty
	name  string // the secret, for a reference
	isRef bool
}

func parseRefs(s string) ([]refToken, error) {
	var out []refToken
	rest := s
	for {
		i := strings.Index(rest, refOpen)
		if i < 0 {
			if rest != "" {
				out = append(out, refToken{text: rest})
			}
			return out, nil
		}
		if i > 0 && rest[i-1] == '$' { // escaped: drop the escaping $, keep the opener literally
			out = append(out, refToken{text: rest[:i-1] + refOpen})
			rest = rest[i+len(refOpen):]
			continue
		}
		if i > 0 {
			out = append(out, refToken{text: rest[:i]})
		}
		after := rest[i+len(refOpen):]
		end := strings.Index(after, refClose)
		if end < 0 {
			return nil, fmt.Errorf("unterminated %s... reference (missing %q)", refOpen, refClose)
		}
		name := after[:end]
		if err := ValidateName(name); err != nil {
			return nil, fmt.Errorf("in a %s...} reference: %w", refOpen, err)
		}
		out = append(out, refToken{name: name, isRef: true})
		rest = after[end+len(refClose):]
	}
}

// CheckTemplate reports whether s is well-formed: every reference terminated and naming a valid
// secret.
func CheckTemplate(s string) error {
	_, err := parseRefs(s)
	return err
}

// Refs returns the secrets s references, in order of first appearance, without duplicates. Escaped
// openers are not references.
func Refs(s string) []string {
	toks, err := parseRefs(s)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, t := range toks {
		if t.isRef && !seen[t.name] {
			seen[t.name] = true
			names = append(names, t.name)
		}
	}
	return names
}

// Expand replaces every reference in s with get(name), and turns each escaped opener into the
// literal text. It stops at the first secret get cannot supply.
func Expand(s string, get func(name string) (string, error)) (string, error) {
	toks, err := parseRefs(s)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, t := range toks {
		if !t.isRef {
			b.WriteString(t.text)
			continue
		}
		v, err := get(t.name)
		if err != nil {
			return "", err
		}
		b.WriteString(v)
	}
	return b.String(), nil
}
