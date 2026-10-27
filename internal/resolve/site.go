package resolve

import (
	"fmt"
	"strings"
)

// A site is the group of stacks that serve one set of published names: the platform stack that declares the helpers, and the app stacks whose publishes
// those helpers show. A server holds one site unless stacks say otherwise, and then the join (internal/join) and the derive passes (internal/derive) read
// one site at a time instead of the whole server (#189).
//
// `kind: stack` may say `site: NAME`. tink does not make an object for the site: the name is stamped where the server already learns things, on the
// instances the join reads across stacks,
//
//	user.tink.site: NAME
//
// on every instance of the stack that states a role (the helpers: user.tink.role) and on every instance a publish of the stack names (so a publish,
// which is stamped on its instance, is in the site of that instance and its stamp keeps its shape). An instance with a role or a publish stamp and no
// site is in the UNNAMED site, which is where everything is on a server that never says `site:`; that server behaves as it did before.
//
// The key is OWNED, like the publish stamps (publishstamp.go): it is derived from the stack, so a stack declaring it is refused, and one the stack no longer
// derives (the stack dropped `site:`, or the instance lost its role) is a planned removal. Only on instances the stack declares.
const SiteKey = "user.tink.site"

// RoleKey is the config key an instance states its role in (dns, ingress, sso, mesh). The join finds the helpers by it.
const RoleKey = "user.tink.role"

// StackSite is the site the stack's own `kind: stack` document names, or "" for the unnamed site. A stack that names itself twice has no single answer
// (Levels reports that error), so it has no site either.
func StackSite(resources []Resource) string {
	if _, err := StackName(resources); err != nil {
		return ""
	}
	for _, r := range resources {
		if r.Kind == KindStack {
			return r.Site
		}
	}
	return ""
}

// validateStackSite checks the `site:` of a `kind: stack` document: a name written the way a stack's is. Another kind setting it is refused by fieldOwners.
func validateStackSite(r Resource) error {
	if r.Kind != KindStack || r.Site == "" {
		return nil
	}
	if !stackNamePattern.MatchString(r.Site) {
		return fmt.Errorf("resource %q: a site's name is lower case letters, digits, '.', '_' and '-' (at most 63, starting with a letter or digit), as a stack's is: %q is not", r.Name, r.Site)
	}
	return nil
}

// applyStackSite gives the instances that a stack with a site puts in it their stamp: every instance that states a role, and every instance a
// `kind: publish` of the stack names, and every instance a `kind: file` that says derive: is pushed to. It changes the caller's resources in place, by giving each such instance a COPY of its config map that carries the
// key, and is safe to run twice. It runs after applyStackProject, so a publish and its instance have met in one project.
//
// A stack with no site gets nothing: it is in the unnamed site, which has no stamp.
func applyStackSite(resources []Resource) {
	site := StackSite(resources)
	if site == "" {
		return
	}
	type loc struct{ project, name string }
	at := func(project string) string {
		if project == "" {
			return "default"
		}
		return project
	}
	published := map[loc]bool{}
	for _, r := range resources {
		if r.Kind == KindPublish || (r.Kind == KindFile && r.Derive != "") { // a derived file is kept for the site its instance is in (derivestamp.go)
			published[loc{at(r.Project), r.Instance}] = true
		}
	}
	for i := range resources {
		r := &resources[i]
		if r.Kind != KindInstance {
			continue
		}
		if strings.TrimSpace(r.Config[RoleKey]) == "" && !published[loc{at(r.Project), r.Name}] {
			continue
		}
		if r.Config[SiteKey] == site {
			continue
		}
		cfg := make(map[string]string, len(r.Config)+1)
		for k, v := range r.Config {
			cfg[k] = v
		}
		cfg[SiteKey] = site
		r.Config = cfg
	}
}
