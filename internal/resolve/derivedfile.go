package resolve

import (
	"encoding/json"
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
	DeriveDNS   = "dns"   // the join's `dns` rendering: the dns helper's generated records, one line per published name
	DeriveMesh  = "mesh"  // the join's `mesh` rendering: a JSON array of A records, the format of Headscale's extra_records file. The WHOLE file: JSON has no comment to mark a region with

	DeriveBegin = "# BEGIN tink derive"
	DeriveEnd   = "# END tink derive"
)

func validateDerivedFile(r Resource) error {
	if r.Kind != KindFile {
		return nil
	}
	if r.Seed && r.Derive != "" {
		return fmt.Errorf("resource %q: seed and derive cannot be combined: a derived file is rewritten whenever the stacks change, and a seed file is never rewritten", r.Name)
	}
	if r.Derive == "" {
		if r.AdoptExisting || r.AllowEmpty {
			return fmt.Errorf("resource %q: adopt_existing and allow_empty belong to a file that has a derive field", r.Name)
		}
		return nil
	}
	switch r.Derive {
	case DeriveCaddy, DeriveSSO, DeriveDNS:
	case DeriveMesh:
		if r.Content != "" {
			return fmt.Errorf("resource %q: derive: %s is the whole file, a JSON array tink writes; it takes no content or source_path", r.Name, DeriveMesh)
		}
		return nil
	default:
		return fmt.Errorf("resource %q: derive %q is not one of %s, %s, %s, %s", r.Name, r.Derive, DeriveCaddy, DeriveSSO, DeriveDNS, DeriveMesh)
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

// hasDerivedMarkers reports whether content has a line that is the DeriveBegin marker: the sign that tink wrote the file.
func hasDerivedMarkers(content string) bool {
	for _, l := range strings.Split(content, "\n") {
		if strings.TrimSpace(l) == DeriveBegin {
			return true
		}
	}
	return false
}

// derivedLines are the lines between the markers that are not blank or comments, or, when content has no markers, those of the whole of it.
func derivedLines(content string) []string {
	lines := strings.Split(content, "\n")
	if hasDerivedMarkers(content) {
		in := false
		var region []string
		for _, l := range lines {
			switch strings.TrimSpace(l) {
			case DeriveBegin:
				in = true
			case DeriveEnd:
				in = false
			default:
				if in {
					region = append(region, l)
				}
			}
		}
		lines = region
	}
	var out []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// DerivedCheck is what the guards of a derived file are judged on, as plain values: `plan` fills it from the file's resource and what it read, and the
// daemon from a stamp and what it read, so the two cannot disagree about when a file is not theirs to replace.
type DerivedCheck struct {
	// Render is the value of `derive:`; Path and Instance say where the file is, for the message.
	Render, Path, Instance string
	// Live is what the file holds now and Desired what it would hold. Zone is the site's zone, for the mesh view.
	Live, Desired, Zone string
	// Adopt and AllowEmpty are the operator's decisions (adopt_existing, allow_empty). The daemon passes neither, ever.
	Adopt, AllowEmpty bool
}

// CheckDerived applies the two refusals that protect a derived file to a file that exists and would be replaced. It is the dns applier's rules
// (docs/join.md), made the plan's, for every derived file:
//
//   - A file that has lines in it and no DeriveBegin line was not written by tink. Replacing it would lose whatever those lines say (records written
//     by hand, routes), so it is refused until adopt_existing says to take it over, once.
//   - A rendering with nothing in it does not empty a region that has lines in it. The site publishing no name at all usually means the wrong
//     server, or stacks that have not been applied yet, and an empty file would take every name down; allow_empty says it is meant.
//
// It returns the reason the file is not to be replaced, or a note to print when it is replaced under adopt_existing. Both empty means go ahead.
func CheckDerived(c DerivedCheck) (refusal, note string) {
	if c.Render == DeriveMesh {
		return checkRecords(c)
	}
	liveLines := derivedLines(c.Live)
	switch {
	case !hasDerivedMarkers(c.Live) && len(liveLines) > 0:
		if c.Adopt {
			return "", fmt.Sprintf("adopt_existing: replaces the %d line(s) that %s on %s holds and tink did not write; remove adopt_existing once it is applied", len(liveLines), c.Path, c.Instance)
		}
		return fmt.Sprintf("%s on %s was not written by tink (it has no %q line) and holds %d line(s) that applying would remove: move any you want to keep out of it, or set adopt_existing: true once to take it over", c.Path, c.Instance, DeriveBegin, len(liveLines)), ""
	case len(derivedLines(c.Desired)) == 0 && len(liveLines) > 0 && !c.AllowEmpty:
		return fmt.Sprintf("nothing is published, so applying would empty the %d line(s) tink wrote to %s on %s; that usually means the wrong server, or stacks that have not been applied yet (allow_empty: true says it is meant)", len(liveLines), c.Path, c.Instance), ""
	}
	return "", ""
}

// guardDerived is CheckDerived for a file that `plan` has found would be replaced: a refusal makes the plan BLOCKED. live is what the plan read.
func guardDerived(f Resource, p PlannedResource, live string) PlannedResource {
	if f.Derive == "" || p.Action != ActionUpdate {
		return p
	}
	refusal, note := CheckDerived(DerivedCheck{Render: f.Derive, Path: f.Path, Instance: f.Instance, Live: live, Desired: f.Content, Zone: f.DerivedZone,
		Adopt: f.AdoptExisting, AllowEmpty: f.AllowEmpty})
	if refusal != "" {
		return PlannedResource{Resource: f, Action: ActionBlocked, Blocked: []string{refusal}}
	}
	if note != "" {
		p.Notes = append(p.Notes, note)
	}
	return p
}

// DerivedRegion returns the lines between the DeriveBegin and DeriveEnd lines of content, without them, or "" when content has no region. It is what
// a derive filled in, for a person to read (tink plan explain).
func DerivedRegion(content string) string {
	in := false
	var region []string
	for _, l := range strings.Split(content, "\n") {
		switch strings.TrimSpace(l) {
		case DeriveBegin:
			in = true
		case DeriveEnd:
			in = false
		default:
			if in {
				region = append(region, l)
			}
		}
	}
	return strings.Join(region, "\n")
}

// A whole-file rendering (derive: mesh) has no marker to say that tink wrote the file, so the guard looks at what is in it. The file is a JSON array of
// records {name, type, value}; a record whose name is outside the zone the site publishes in is not one tink wrote, and replacing the file would lose it.
// A file that is not that shape is not a records file at all. Either is refused until adopt_existing takes the file over, once. And as for the other
// derived files, a rendering with no record does not empty a file that has records until allow_empty says that is meant. A record inside the zone that
// the rendering lacks is a name that was unpublished, which is what the file is for.
func checkRecords(c DerivedCheck) (refusal, note string) {
	if strings.TrimSpace(c.Live) == "" {
		return "", ""
	}
	var records []struct{ Name, Type, Value string }
	if err := json.Unmarshal([]byte(c.Live), &records); err != nil {
		if c.Adopt {
			return "", fmt.Sprintf("adopt_existing: replaces %s on %s, which is not a JSON array of records; remove adopt_existing once it is applied", c.Path, c.Instance)
		}
		return fmt.Sprintf("%s on %s is not a JSON array of records ({name, type, value}), so it is not the file this was written for: set adopt_existing: true once to replace it", c.Path, c.Instance), ""
	}
	var foreign []string
	for _, r := range records {
		if c.Zone != "" && !strings.HasSuffix(strings.ToLower(r.Name), "."+strings.ToLower(c.Zone)) {
			foreign = append(foreign, r.Name)
		}
	}
	if len(foreign) > 0 {
		if c.Adopt {
			return "", fmt.Sprintf("adopt_existing: replaces %d record(s) of %s on %s outside %s; remove adopt_existing once it is applied", len(foreign), c.Path, c.Instance, c.Zone)
		}
		shown := foreign
		if len(shown) > 3 {
			shown = append(append([]string{}, shown[:3]...), "...")
		}
		return fmt.Sprintf("%s on %s holds %d record(s) outside the zone %s that tink did not write (%s), and applying would remove them: set adopt_existing: true once to take the file over", c.Path, c.Instance, len(foreign), c.Zone, strings.Join(shown, ", ")), ""
	}
	var desired []struct{ Name, Type, Value string }
	_ = json.Unmarshal([]byte(c.Desired), &desired)
	if len(desired) == 0 && len(records) > 0 && !c.AllowEmpty {
		return fmt.Sprintf("nothing is published, so applying would empty the %d record(s) in %s on %s; that usually means the wrong server, or stacks that have not been applied yet (allow_empty: true says it is meant)", len(records), c.Path, c.Instance), ""
	}
	return "", ""
}
