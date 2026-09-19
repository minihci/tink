package resolve

import (
	"fmt"
	"sort"
)

// Levels groups resources into ordered batches: every resource in level
// N depends only on resources in levels < N, so everything within one
// level can be created in parallel. Kind priority breaks ties among
// resources with no dependency relationship to each other, matching the
// same coarse "containers before contents" ordering incus-apply uses.
func Levels(resources []Resource) ([][]Resource, error) {
	all := make(map[string]*Resource, len(resources))
	for i := range resources {
		if _, dup := all[resources[i].Name]; dup {
			return nil, fmt.Errorf("duplicate resource name %q", resources[i].Name)
		}
		all[resources[i].Name] = &resources[i]
	}
	inheritProject(all)

	deps := make(map[string][]string, len(resources))
	for _, r := range resources {
		deps[r.Name] = r.dependencies(all)
	}

	// Runs after deps above, not before: dependencies() needs Image to
	// still hold the referenced image resource's own Name to find the
	// edge; incus init needs the real Alias. Doing the rewrite here,
	// once, means every downstream consumer (createInstance, planned
	// diffs, ...) just sees the resolved Incus-level value already, the
	// same after-dependencies/before-levels slot inheritProject already
	// uses for the same reason.
	resolveImageAlias(all)

	remaining := make(map[string]*Resource, len(resources))
	for name, r := range all {
		remaining[name] = r
	}

	var levels [][]Resource
	for len(remaining) > 0 {
		var level []Resource
		for name, r := range remaining {
			ready := true
			for _, dep := range deps[name] {
				if _, stillWaiting := remaining[dep]; stillWaiting {
					ready = false
					break
				}
			}
			if ready {
				level = append(level, *r)
			}
		}
		if len(level) == 0 {
			return nil, fmt.Errorf("cyclic or unresolvable dependency among: %v", remainingNames(remaining))
		}
		sort.SliceStable(level, func(i, j int) bool {
			return kindPriority[level[i].Kind] < kindPriority[level[j].Kind]
		})
		levels = append(levels, level)
		for _, r := range level {
			delete(remaining, r.Name)
		}
	}
	return levels, nil
}

// inheritProject fills in a file resource's own Project from its target
// Instance's Project, when left unset. Found the hard way, live: leaving
// a file resource's Project empty falls back to the daemon's default
// project, silently, the exact same class of mistake as the
// incus-apply#68 bug this whole exploration started from -- except this
// time it's a human forgetting to restate a project name that structural
// inference already knows, not a tool bug. Since a file resource already
// names its target instance by reference, use that same connection to
// inherit the project too, rather than trusting every file resource to
// redeclare something the graph already knows.
func inheritProject(all map[string]*Resource) {
	for _, r := range all {
		if r.Kind == KindFile && r.Project == "" && r.Instance != "" {
			if target, ok := all[r.Instance]; ok {
				r.Project = target.Project
			}
		}
	}
}

// resolveImageAlias rewrites an instance's Image from another resource's
// own tink-graph Name into that resource's real Incus Alias, when Image
// names a local kind: image resource -- found live while building this
// platform's own haos test project, where an instance's image: and an
// image resource's alias: were two independently-typed strings with
// nothing tying them together but coincidence, needing a change in two
// places for one rename. Left untouched when Image doesn't match any
// local image resource's Name (a container pulling a remote reference
// like docker-oci:redis:7 is the normal case, and names no local
// resource at all) -- so this is additive, not a new required form.
func resolveImageAlias(all map[string]*Resource) {
	for _, r := range all {
		if r.Image == "" {
			continue
		}
		if target, ok := all[r.Image]; ok && target.Kind == KindImage {
			r.Image = target.Alias
		}
	}
}

func remainingNames(remaining map[string]*Resource) []string {
	names := make([]string, 0, len(remaining))
	for name := range remaining {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
