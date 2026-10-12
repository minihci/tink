package resolve

import (
	"fmt"
	"sort"
)

// validateDependsOn rejects a depends_on entry that names nothing in the stack, or names the resource itself.
//
// Structural references (a profile, a volume, an instance's project) are read leniently on purpose: they may name an
// object that already exists on the server and is not declared here, and then there is nothing to order. depends_on is
// different. It exists only to order resources within this one apply, so a name that is not in the stack can only be a
// typo or a stale rename, and ignoring it quietly changes the order the author asked for (resource.dependencies drops a
// name it cannot find). The error names the closest resource when one is near enough to be the likely intent.
func validateDependsOn(resources []Resource) error {
	names := map[string]bool{}
	for _, r := range resources {
		if r.Kind != KindStack { // the stack's own name is metadata, not a resource (see StackName)
			names[r.Name] = true
		}
	}
	for _, r := range resources {
		if r.Kind == KindStack {
			continue
		}
		for _, d := range r.DependsOn {
			if d == r.Name {
				return fmt.Errorf("resource %q: depends_on names itself", r.Name)
			}
			if names[d] {
				continue
			}
			hint := ""
			if near := closestName(d, names); near != "" {
				hint = fmt.Sprintf(" (did you mean %q?)", near)
			}
			return fmt.Errorf("resource %q: depends_on names %q, which is not a resource in this stack%s -- depends_on only orders resources within one apply; for something that already exists on the server there is nothing to wait for", r.Name, d, hint)
		}
	}
	return nil
}

// closestName returns the name nearest to want, if it is near enough to be a typo of it (at most two edits, and no more than a
// third of the name), preferring the alphabetically first on a tie so the message is the same on every run.
func closestName(want string, names map[string]bool) string {
	limit := len(want) / 3
	if limit > 2 {
		limit = 2
	}
	if limit < 1 {
		limit = 1
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	best, bestDist := "", limit+1
	for _, n := range sorted {
		if d := editDistance(want, n); d < bestDist {
			best, bestDist = n, d
		}
	}
	return best
}

// editDistance is the Levenshtein distance between a and b.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
