package resolve

import (
	"fmt"
	"regexp"
)

var stackNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// validateStack rejects a `kind: stack` whose name could not be kept as a config value people read: lower case letters,
// digits, '.', '_' and '-'. The stack declaration says nothing else; it is only a name.
func validateStack(r Resource) error {
	if r.Kind != KindStack {
		return nil
	}
	if !stackNamePattern.MatchString(r.Name) {
		return fmt.Errorf("resource %q: a stack's name is lower case letters, digits, '.', '_' and '-' (at most 63, starting with a letter or digit)", r.Name)
	}
	return nil
}

// StackName is the name the stack gives itself with a `kind: stack` document, or "" when it does not. A stack is one
// thing: two declarations, even in different files, are an error.
//
// The declaration is metadata, not a resource: it is left out of the dependency graph (Levels), so a stack can be named
// after an instance or a volume it contains without the two colliding.
func StackName(resources []Resource) (string, error) {
	name := ""
	for _, r := range resources {
		if r.Kind != KindStack {
			continue
		}
		if name != "" {
			return "", fmt.Errorf("a stack is named once: found kind: stack %q and %q", name, r.Name)
		}
		name = r.Name
	}
	return name, nil
}
