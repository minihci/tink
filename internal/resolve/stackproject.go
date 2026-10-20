package resolve

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// takesDefaultProject says which kinds live inside an Incus project and so receive the stack's default one. A project is not inside
// another project, a network and a network ACL are server-global (Incus keeps them in the default project unless a project has
// features.networks, which tink does not manage; a `project:` on either is a load error), the stack declaration is metadata, a publish follows its instance's project (it is a declaration, so it takes the stack's default and is never created), a backup
// target describes where copies go (a server or pool, not a project), and kind: incus is argv for the local CLI. File and exec resources
// are handled separately: they follow the instance they act on.
func takesDefaultProject(k Kind) bool {
	switch k {
	case KindProfile, KindStorageVolume, KindInstance, KindImage, KindPublish:
		return true
	}
	return false
}

// stackProject is the project the stack's own `kind: stack` document names, or "". A stack that names itself twice has no single
// answer (Levels reports that error), so it has no default either.
func stackProject(resources []Resource) string {
	if _, err := StackName(resources); err != nil {
		return ""
	}
	for _, r := range resources {
		if r.Kind == KindStack {
			return r.Project
		}
	}
	return ""
}

// applyStackProject gives every resource that lives in a project and names none the project the stack declares for itself, so a stack
// that belongs to one project says so once. Without it every resource repeats `project:`, and the one that forgets it is created in the
// server's default project without a word: on a host where that project holds production, that is the mistake to make impossible.
//
// A resource's own project always wins. A file or exec resource acts on an instance, so it follows that instance (its own project,
// or the default it just received), the same rule inheritProject applies; only one whose instance is not in this stack takes the default.
//
// It changes the caller's resources in place, like the other passes over them, and is safe to run twice: LoadFiles runs it so every
// consumer of a loaded stack sees the final project, and Levels runs it again for callers that build resources by hand.
func applyStackProject(resources []Resource) {
	def := stackProject(resources)
	if def == "" {
		return
	}
	byName := make(map[string]*Resource, len(resources))
	for i := range resources {
		if inGraph(resources[i].Kind) {
			byName[resources[i].Name] = &resources[i]
		}
		if resources[i].Project == "" && takesDefaultProject(resources[i].Kind) {
			resources[i].Project = def
		}
	}
	for i := range resources {
		r := &resources[i]
		if (r.Kind != KindFile && r.Kind != KindExec) || r.Project != "" {
			continue
		}
		if target, ok := byName[r.Instance]; ok && target.Project != "" {
			r.Project = target.Project
		} else {
			r.Project = def
		}
	}
}

// mixedProjectNotes finds the mistake the default above prevents when a stack does not use it: some resources name a project and
// others, which live in a project too, do not, so those are created in the server's default project. It returns a warning per such
// resource, by name, and nothing for a stack where either all or none of them name one. It is only a warning: putting some resources
// in the default project on purpose is legitimate, it just should not happen by omission.
func mixedProjectNotes(resources []Resource) map[string]string {
	var named, unnamed []Resource
	projects := map[string]bool{}
	for _, r := range resources {
		if !takesDefaultProject(r.Kind) {
			continue
		}
		if r.Project != "" {
			named = append(named, r)
			projects[r.Project] = true
		} else {
			unnamed = append(unnamed, r)
		}
	}
	if len(named) == 0 || len(unnamed) == 0 {
		return nil
	}
	sort.Slice(named, func(i, j int) bool { return named[i].Name < named[j].Name })
	var names []string
	for i, r := range named {
		if i == 3 {
			names = append(names, fmt.Sprintf("%d more", len(named)-3))
			break
		}
		names = append(names, fmt.Sprintf("%s/%s", r.Kind, r.Name))
	}
	var projs []string
	for p := range projects {
		projs = append(projs, fmt.Sprintf("%q", p))
	}
	sort.Strings(projs)
	note := fmt.Sprintf("names no project, so it is created in the server's default project, while %s are in project %s -- if that is not intended, add `project:` here, or set it once on the stack's `kind: stack` document",
		strings.Join(names, ", "), strings.Join(projs, ", "))
	out := make(map[string]string, len(unnamed))
	for _, r := range unnamed {
		out[r.Name] = note
	}
	return out
}

// planOne plans one resource and adds the stack-level note about its project, if there is one. planKind does the work per kind.
//
// Planning only reads, so a read the server never answers is abandoned at a deadline (opbounds.go) with nothing to account for: the error says nothing
// was changed.
func planOne(server incus.InstanceServer, r Resource, opts PlanOptions) (PlannedResource, error) {
	var p PlannedResource
	var err error
	if boundsPlan(r) {
		p, err = incusapi.Bounded(opLimit(opts.opTimeout, false), "planning "+string(r.Kind)+"/"+r.Name, func() (PlannedResource, error) { return planKind(server, r, opts) })
		var te *incusapi.TimeoutError
		if errors.As(err, &te) {
			err = fmt.Errorf("%w: the server did not answer a read within %s; nothing was changed", incusapi.ErrTimeout, te.After)
		}
	} else {
		p, err = planKind(server, r, opts)
	}
	if err == nil {
		if note := opts.projectNotes[r.Name]; note != "" {
			p.Warnings = append(p.Warnings, note)
		}
	}
	return p, err
}
