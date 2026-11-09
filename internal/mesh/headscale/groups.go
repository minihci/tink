// Package headscale projects the directory's groups into the pieces of a Headscale policy file.
//
// Today that is one piece: the "groups" object. It is a pure function of an identity.Snapshot and a few options; it does no I/O. What it
// returns is a FRAGMENT to be merged into the policy by hand, never a whole policy: Headscale treats a policy file with no "acls" or "grants"
// section as allow-all, so a fragment mistaken for a policy would open everything. See docs/identity-projector-design.md.
package headscale

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/identity"
)

// DefaultMemberFormat writes a person the way Headscale's own documentation writes a user: "alice@".
const DefaultMemberFormat = "{id}@"

// Options say what to project.
type Options struct {
	// Groups to emit, by directory name. Empty means every group that is not the directory engine's own system groups.
	Groups []string
	// MemberFormat writes one person as a policy name. {id} and {email} are replaced. Empty means DefaultMemberFormat.
	MemberFormat string
}

// GroupMembers is one group as emitted: its directory name and its members as they are written in the policy.
type GroupMembers struct {
	Name    string
	Members []string
}

// Result is the fragment and what was noticed while making it.
type Result struct {
	// Text is the huJSON fragment. It is empty when there is an Error finding: a partial projection is never produced.
	Text     string
	Findings []finding.Finding
	// Groups is what Text says, group by group, for checks that compare it with something else (see CrossCheck). Empty when Text is.
	Groups []GroupMembers
}

// HasErrors reports whether any finding is an Error.
func (r Result) HasErrors() bool { return finding.HasErrors(r.Findings) }

// groupName is deliberately conservative: a name outside it is reported, never rewritten.
var groupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

var placeholder = regexp.MustCompile(`\{[^{}]*\}`)

// Project makes the "groups" fragment from the directory snapshot.
func Project(s identity.Snapshot, o Options) Result {
	var res Result
	errorf := func(reason, format string, a ...any) {
		res.Findings = append(res.Findings, finding.Errorf(reason, "", format, a...))
	}
	warnf := func(reason, format string, a ...any) {
		res.Findings = append(res.Findings, finding.Warnf(reason, "", format, a...))
	}

	format := o.MemberFormat
	if format == "" {
		format = DefaultMemberFormat
	}
	formatOK := true
	left := placeholder.ReplaceAllStringFunc(format, func(p string) string {
		if p == "{id}" || p == "{email}" {
			return ""
		}
		return p
	})
	if strings.ContainsAny(left, "{}") {
		errorf("the member format has an unknown placeholder", "the member format %q has a placeholder other than {id} and {email}", format)
		formatOK = false
	} else if !strings.Contains(format, "{id}") && !strings.Contains(format, "{email}") {
		errorf("the member format names no one", "the member format %q has no {id} or {email}, so every person would get the same name", format)
		formatOK = false
	}

	s = s.Normalize()
	var targets []string
	if len(o.Groups) == 0 {
		for _, g := range s.Groups {
			if !g.System {
				targets = append(targets, g.Name)
			}
		}
		if len(targets) == 0 {
			warnf("there are no groups to emit", "the directory has no groups other than its own system groups, so there is nothing to emit")
		}
	} else {
		seen := map[string]bool{}
		for _, n := range o.Groups {
			if !seen[n] {
				seen[n] = true
				targets = append(targets, n)
			}
		}
		sort.Strings(targets)
	}

	var entries []entry
	for _, name := range targets {
		g, ok := s.Group(name)
		if !ok {
			errorf("a group is not in the directory", "group %q is not in the directory", name)
			continue
		}
		if !groupName.MatchString(name) {
			errorf("a group name cannot be used", "group %q cannot be used as a policy group name (letters, digits, '_', '.' and '-' only, starting with a letter or digit)", name)
			continue
		}
		e := entry{name: name, members: []string{}}
		if formatOK {
			seen := map[string]string{}
			for _, p := range g.Members {
				m := strings.NewReplacer("{id}", p.ID, "{email}", p.Email).Replace(format)
				if p.ID == "" || (strings.Contains(format, "{email}") && p.Email == "") {
					errorf("a person lacks a field the member format needs", "group %q: person %q has no %s to build a policy name from", name, personLabel(p), missingField(format, p))
					continue
				}
				if other, dup := seen[m]; dup {
					errorf("two people get one policy name", "group %q: %q and %q would both be written as %q", name, other, personLabel(p), m)
					continue
				}
				seen[m] = personLabel(p)
				e.members = append(e.members, m)
			}
			sort.Strings(e.members)
		}
		if len(g.Members) == 0 {
			warnf("a group has no members", "group %q has no members", name)
		}
		entries = append(entries, e)
	}

	if res.HasErrors() {
		return res
	}
	res.Text = render(entries)
	for _, e := range entries {
		res.Groups = append(res.Groups, GroupMembers{Name: e.name, Members: e.members})
	}
	return res
}

func personLabel(p identity.Person) string {
	if p.ID != "" {
		return p.ID
	}
	if p.Email != "" {
		return p.Email
	}
	return p.DisplayName
}

func missingField(format string, p identity.Person) string {
	if p.ID == "" {
		return "id"
	}
	return "email"
}

// entry is one group to write: its directory name and its members as policy names.
type entry struct {
	name    string
	members []string
}

// render writes the huJSON fragment. Trailing commas are legal in huJSON and keep the fragment easy to merge and to diff.
func render(entries []entry) string {
	var b strings.Builder
	b.WriteString("// generated by tink from the directory; do not edit by hand\n")
	if len(entries) == 0 {
		b.WriteString("\"groups\": {},\n")
		return b.String()
	}
	b.WriteString("\"groups\": {\n")
	for _, e := range entries {
		b.WriteString("  " + quote("group:"+e.name) + ": [")
		for i, m := range e.members {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quote(m))
		}
		b.WriteString("],\n")
	}
	b.WriteString("},\n")
	return b.String()
}

// quote is a JSON string literal without the HTML escaping of encoding/json's default, which would turn '<', '>' and '&' into \u escapes.
func quote(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}
