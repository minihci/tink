// Package identity is the identity role: who the people are and which groups they belong to, read from whatever directory holds them.
//
// It knows nothing about any one directory engine (see internal/identity/lldap) or about anything that consumes the result (see
// internal/mesh/headscale). See docs/identity-projector-design.md.
package identity

import (
	"sort"
	"strings"
)

// Person is one member of a group, as the directory describes them.
type Person struct {
	ID          string
	Email       string
	DisplayName string
}

// Group is a named set of people.
type Group struct {
	Name string
	// System is true for a group the directory engine keeps for itself (LLDAP's lldap_admin, ...), as opposed to one of the site's.
	System  bool
	Members []Person
}

// Snapshot is the directory's groups at one moment.
type Snapshot struct {
	Groups []Group
}

// Reader reads a Snapshot. A failed read is an error, never an empty Snapshot: an empty answer rendered into a policy could lock everyone out.
type Reader interface {
	Read() (Snapshot, error)
}

// Normalize returns a copy with groups sorted by name and each group's members sorted by ID, so the same directory always gives the same
// Snapshot and anything rendered from it is stable.
func (s Snapshot) Normalize() Snapshot {
	out := Snapshot{Groups: make([]Group, len(s.Groups))}
	for i, g := range s.Groups {
		members := append([]Person(nil), g.Members...)
		sort.Slice(members, func(a, b int) bool { return strings.Compare(members[a].ID, members[b].ID) < 0 })
		out.Groups[i] = Group{Name: g.Name, System: g.System, Members: members}
	}
	sort.Slice(out.Groups, func(a, b int) bool { return strings.Compare(out.Groups[a].Name, out.Groups[b].Name) < 0 })
	return out
}

// Group returns the group with exactly this name.
func (s Snapshot) Group(name string) (Group, bool) {
	for _, g := range s.Groups {
		if g.Name == name {
			return g, true
		}
	}
	return Group{}, false
}
