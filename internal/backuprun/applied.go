package backuprun

import (
	"fmt"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/volbackup"
)

// Unapplied compares the copies a stack declares with the copy policies the volumes carry (Discover), for the volumes in declared, and
// says in what way they differ: a volume with no policy, one whose policy cannot be read, one whose copies are not the stack's, and a
// volume that shares its name with another that carries a policy, which a run by name would pick up too. It is empty when running the volumes' policies runs what the stack
// declares, which is what a run handed to the helper does: the helper has only the volumes.
//
// Targets are compared by name, pool and remote, which is everything a policy carries of them; the address and fingerprint a stack may
// add are for `tink remote add` and not part of what runs.
func Unapplied(declared, applied []Item, problems map[string]error) []string {
	byKey := map[string]Item{}
	for _, a := range applied {
		byKey[placeKey(a.Volume)] = a
	}
	var out []string
	for _, d := range declared {
		a, ok := byKey[placeKey(d.Volume)]
		switch {
		case !ok:
			if err, bad := problems[d.Label]; bad {
				out = append(out, fmt.Sprintf("%s: its copy policy cannot be read: %v", d.Label, err))
			} else {
				out = append(out, fmt.Sprintf("%s: the volume carries no copy policy", d.Label))
			}
		case copiesKey(d.Copies) != copiesKey(a.Copies):
			out = append(out, fmt.Sprintf("%s: the copies on the volume are not the stack's", d.Label))
		default:
			// A run is handed over by name, and a name (a label, or a bare name) selects every volume it matches, in any project or pool.
			if matched, _ := Select(applied, []string{d.Label}); len(matched) > 1 {
				out = append(out, fmt.Sprintf("%s: more than one volume of that name carries a copy policy (in another project or pool), and a run by name would copy all of them", d.Label))
			}
		}
	}
	return out
}

func placeKey(v volbackup.Volume) string {
	project, pool := v.Project, v.Pool
	if project == "" {
		project = "default"
	}
	if pool == "" {
		pool = "default"
	}
	return project + "/" + pool + "/" + v.Name
}

func copiesKey(cs []Copy) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = strings.Join([]string{c.Target.Name, c.Target.Pool, c.Target.Remote, c.Schedule, c.Retain}, "\x1f")
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x1e")
}
