// Package explain is what `tink plan explain` prints: the stack as the passes expanded it, and for every value a pass made, which pass made it
// (#129). A stack is facts, and the passes work out the rest (its defaults, a bridge of its own, the addresses, the ACL rules, the proxy's
// routes), so what `plan` is about to do is not in the files. This is the place to see it, and the debug and audit view of the derive stage.
//
// Provenance is not recorded by the passes. prepare tells a Recorder what each pass was handed and what it left, and the Recorder compares the two:
// a field that differs was made by that pass. Nothing is kept in the resources, there is no second copy of any of them in normal use (only a
// command that traces takes the snapshots), and a new pass is explained without being changed. What the passes know and a comparison cannot (that an
// address was kept and not chosen, which publishes a rule covers) is in the lines each pass reports, which are shown beside its changes.
//
// Nothing secret is printed. The secrets pass is summarised by a count; a value whose key looks like a secret, or was expanded from one, is shown as
// hidden, and a file's content is never printed whole: only the region a derive filled in.
package explain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/prepare"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/secrets"
)

// A Change is one field a pass made or altered.
type Change struct {
	Path string // config.boot.autostart, devices.eth0.network
	Old  string // empty when the field was not there
	New  string
}

// A Resource is what one pass did to one resource.
type Resource struct {
	ID      string // kind/name, with the project when it has one
	Added   bool   // the pass made the resource
	Changes []Change
	// Region is what a derive filled into a file, so that a person can read it: the lines between its markers. Empty for anything else.
	Region string
}

// A Pass is what one pass did: the lines it reported, and the resources it made or changed.
type Pass struct {
	Name      string
	Notices   []string
	Resources []Resource
}

// A Recorder is a prepare.Tracer that keeps what each pass changed. The zero value is ready to use.
type Recorder struct {
	Passes []Pass
	before snapshot
}

// Before takes the snapshot the next After is compared with.
func (r *Recorder) Before(pass string, resources []resolve.Resource) {
	r.before = take(resources)
}

// After records what pass changed, compared with the snapshot Before took.
func (r *Recorder) After(pass string, resources []resolve.Resource, notices []string) {
	after := take(resources)
	p := Pass{Name: pass, Notices: notices}
	if pass == prepare.PassSecrets {
		// Never the values. A count says that something was expanded, which is all a reader needs.
		n := 0
		for path := range after.fields {
			if strings.Contains(path, "|secret_keys.") && !hasKey(r.before.fields, path) {
				n++
			}
		}
		if n > 0 {
			p.Notices = append(p.Notices, fmt.Sprintf("%d secret reference(s) expanded; the values are not shown", n))
		}
		r.Passes = append(r.Passes, p)
		return
	}
	p.Resources = diff(r.before, after)
	r.Passes = append(r.Passes, p)
}

// Empty reports whether no pass made or changed anything and none had a line to say.
func (r *Recorder) Empty() bool {
	for _, p := range r.Passes {
		if len(p.Notices) > 0 || len(p.Resources) > 0 {
			return false
		}
	}
	return true
}

// A snapshot is every resource's non-empty fields, flattened to "ID|path" -> text, and the identity of each resource in the order it was in.
type snapshot struct {
	fields map[string]string
	ids    []string
	secret map[string]bool   // by "ID|config.key": expanded from a secret
	derive map[string]string // by ID: the rendering a file derives, "" for any other
	region map[string]string // by ID: the region of a derived file
}

func take(resources []resolve.Resource) snapshot {
	s := snapshot{fields: map[string]string{}, secret: map[string]bool{}, derive: map[string]string{}, region: map[string]string{}}
	for _, r := range resources {
		id := idOf(r)
		s.ids = append(s.ids, id)
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		for k := range r.SecretKeys {
			s.secret[id+"|config."+k] = true
		}
		s.derive[id] = r.Derive
		if r.Derive != "" {
			s.region[id] = resolve.DerivedRegion(r.Content)
		}
		flatten(id, "", m, s.fields)
	}
	return s
}

func idOf(r resolve.Resource) string {
	id := fmt.Sprintf("%s/%s", r.Kind, r.Name)
	if r.Project != "" {
		id += " (project " + r.Project + ")"
	}
	return id
}

// flatten writes the non-empty leaves of m under "id|path", with the Go field names lower-cased so that they read as the stack's keys do.
func flatten(id, prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		name := k
		if prefix == "" {
			name = snake(k)
		}
		path := name
		if prefix != "" {
			path = prefix + "." + k
		}
		switch x := v.(type) {
		case nil:
		case map[string]any:
			flatten(id, path, x, out)
		case string:
			if x != "" {
				out[id+"|"+path] = x
			}
		case bool:
			if x {
				out[id+"|"+path] = "true"
			}
		case float64:
			if x != 0 {
				out[id+"|"+path] = fmt.Sprint(x)
			}
		case []any:
			if len(x) > 0 {
				b, _ := json.Marshal(x)
				out[id+"|"+path] = string(b)
			}
		default:
			out[id+"|"+path] = fmt.Sprint(x)
		}
	}
}

func hasKey(m map[string]string, k string) bool { _, ok := m[k]; return ok }

// diff lists, in the order of the resources, what is in after and not in before: a resource that was not there, or fields that were not there or differ.
func diff(before, after snapshot) []Resource {
	had := map[string]bool{}
	for _, id := range before.ids {
		had[id] = true
	}
	var out []Resource
	seen := map[string]bool{}
	for _, id := range after.ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		res := Resource{ID: id, Added: !had[id]}
		var paths []string
		for key := range after.fields {
			if rid, path, ok := strings.Cut(key, "|"); ok && rid == id {
				if old, was := before.fields[key]; was && old == after.fields[key] {
					continue
				}
				if skip(path) {
					continue
				}
				paths = append(paths, path)
			}
		}
		sort.Strings(paths)
		for _, path := range paths {
			key := id + "|" + path
			c := Change{Path: path, Old: before.fields[key], New: after.fields[key]}
			c.Old, c.New = show(after, id, path, c.Old), show(after, id, path, c.New)
			res.Changes = append(res.Changes, c)
		}
		for _, c := range res.Changes {
			if c.Path == "content" && after.derive[id] != "" {
				res.Region = after.region[id]
			}
		}
		if res.Added || len(res.Changes) > 0 {
			out = append(out, res)
		}
	}
	return out
}

// skip says which fields are not worth a line: the identity a heading already gives, and the bookkeeping.
func skip(path string) bool {
	switch {
	case path == "kind", path == "name", path == "project":
		return true
	case strings.HasPrefix(path, "secret_keys"):
		return true
	}
	return false
}

// show is the text to print for a value: hidden when it is a secret, a summary or the derived region for a file's content, and as it is otherwise.
func show(s snapshot, id, path, value string) string {
	if value == "" {
		return ""
	}
	if key, ok := strings.CutPrefix(path, "config."); ok && (s.secret[id+"|"+path] || secrets.SensitiveKey(key)) {
		return secrets.HiddenValue
	}
	if i := strings.LastIndex(path, "."); i >= 0 && strings.HasPrefix(path, "devices.") && secrets.SensitiveKey(path[i+1:]) {
		return secrets.HiddenValue
	}
	if path == "content" {
		if s.derive[id] != "" {
			return "(the " + s.derive[id] + " region, below)"
		}
		return fmt.Sprintf("(%d bytes)", len(value))
	}
	return value
}

// Render writes what the recorder saw as text for a person. stack names the stack, or is empty.
func (r *Recorder) Render(stack string) string {
	var b strings.Builder
	title := "the stack"
	if stack != "" {
		title = "stack " + stack
	}
	if r.Empty() {
		fmt.Fprintf(&b, "%s: nothing was derived; it plans as it is written.\n", title)
		return b.String()
	}
	fmt.Fprintf(&b, "%s, as the passes expanded it. Each pass lists the lines it reported and what it made (+) or changed (~).\n", title)
	for _, p := range r.Passes {
		if len(p.Notices) == 0 && len(p.Resources) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s\n", p.Name)
		for _, n := range p.Notices {
			fmt.Fprintf(&b, "  %s\n", n)
		}
		for _, res := range p.Resources {
			mark := "~"
			if res.Added {
				mark = "+"
			}
			fmt.Fprintf(&b, "  %s %s\n", mark, res.ID)
			for _, c := range res.Changes {
				switch {
				case c.Path == "content" && strings.HasPrefix(c.New, "(the "):
					fmt.Fprintf(&b, "      content: %s\n", c.New)
					for _, l := range strings.Split(res.Region, "\n") {
						if l != "" {
							fmt.Fprintf(&b, "        | %s\n", l)
						}
					}
				case c.Old != "":
					fmt.Fprintf(&b, "      %s: %s -> %s\n", c.Path, c.Old, c.New)
				default:
					fmt.Fprintf(&b, "      %s: %s\n", c.Path, c.New)
				}
			}
		}
	}
	return b.String()
}

// snake turns a Go field name into the key a stack file writes it as: DeriveIngress is derive_ingress, OnImageChange is on_image_change, VM is vm.
func snake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' && name[i-1] >= 'a' && name[i-1] <= 'z' {
			b.WriteByte('_')
		}
		b.WriteRune(r)
	}
	return strings.ToLower(b.String())
}
