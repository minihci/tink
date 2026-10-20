package resolve

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// A kind: publish resource is a DECLARATION: it says that a name in the site's zone shows one offer of an instance, to the people in the named groups.
//
//	kind: publish
//	name: whoami               # the host label: the published name is this plus the dns helper's zone (whoami.home.example.org)
//	instance: whoami           # the instance that makes the offer
//	offer: web                 # which of its offers (user.tink.offer.web.* on the instance)
//	allow: [family]            # groups, never people
//
// tink converges nothing FOR it. `tink plan` leaves it out of the dependency graph exactly as it leaves out the `kind: stack` document, and for the same
// reason: it has nothing to create, nothing waits for it, and it must be allowed to share its name with the instance it publishes (the host label and
// the instance are very often one word, and the resolver's names are one flat namespace per stack). What reads it is the join (internal/join, `tink join`),
// which prints what the site's other helpers would need to match it: the dns records, the mesh view, the proxy's routes, the sign-on rules and the
// ACL on the app's bridge. Only the dns records can be applied so far.
//
// It is also stamped onto the instance it names, as the config key user.tink.publish.<name>, so that the join can be built from the server and not only from
// these files (publishstamp.go, which also says why that prefix is the one tink owns).
//
// Load-time checks only, because a publish is judged against other documents (the offer must exist, the helpers must be declared) by the join, which can
// see more than one stack. docs/join.md has the reasoning.

var (
	// publishNamePattern is a DNS label: the first part of the published name.
	publishNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// offerNamePattern is what follows user.tink.offer. on an instance, up to the next dot.
	offerNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
)

// OfferNamePattern reports whether s is a valid name for an offer. The join uses it to judge the keys on an instance with the same rule a publish's
// `offer:` is held to.
func OfferNamePattern(s string) bool { return offerNamePattern.MatchString(s) }

// inGraph says whether a kind is a resource to converge, and so a node in the dependency graph. The stack's name is metadata and a publish is a
// declaration that other commands read; neither is created, neither is waited for, and neither takes part in the flat namespace of resource names.
func inGraph(k Kind) bool { return k != KindStack && k != KindPublish }

// validatePublish rejects a malformed `kind: publish` at load time.
func validatePublish(r Resource) error {
	if r.Kind != KindPublish {
		return nil
	}
	if !publishNamePattern.MatchString(r.Name) {
		return fmt.Errorf("resource %q: a publish's name is the host label of the published name: lower case letters, digits and '-', at most 63, not starting or ending with '-'", r.Name)
	}
	if r.Instance == "" {
		return fmt.Errorf("resource %q: publish needs instance: the instance that makes the offer", r.Name)
	}
	if r.Offer == "" {
		return fmt.Errorf("resource %q: publish needs offer: which of %s's offers it shows (the part after user.tink.offer. on the instance)", r.Name, r.Instance)
	}
	if !offerNamePattern.MatchString(r.Offer) {
		return fmt.Errorf("resource %q: offer %q is not an offer name: lower case letters, digits and '-', starting with a letter", r.Name, r.Offer)
	}
	if len(r.Allow) == 0 {
		return fmt.Errorf("resource %q: publish needs allow: the groups that may use it (an empty list would publish a name nobody may open)", r.Name)
	}
	seen := map[string]bool{}
	for _, g := range r.Allow {
		if g == "" || strings.TrimSpace(g) != g {
			return fmt.Errorf("resource %q: allow lists an empty group name, or one with leading or trailing space", r.Name)
		}
		if strings.Contains(g, ":") {
			return fmt.Errorf("resource %q: allow lists %q, but a publish takes plain group names (family, not group:family): typed subjects such as instance:ha belong to access, which is not built", r.Name, g)
		}
		if strings.ContainsAny(g, "'\"\\\x00\r\n\t") {
			return fmt.Errorf("resource %q: allow lists %q, which has a quote, a backslash or a control character that would corrupt the sign-on rule rendered from it", r.Name, g)
		}
		if seen[g] {
			return fmt.Errorf("resource %q: allow lists the group %q twice", r.Name, g)
		}
		seen[g] = true
	}
	if len(r.DependsOn) > 0 {
		return fmt.Errorf("resource %q: a publish takes no depends_on (it is a declaration, not something that is created or waited for)", r.Name)
	}
	return nil
}

// validatePublishNames rejects two publishes with one name in a stack. The graph used to give this check for free, when every resource had a unique
// name; a publish is outside the graph so that it can share a name with its instance, which leaves this the one place that keeps host labels unique.
func validatePublishNames(resources []Resource) error {
	var dup []string
	seen := map[string]bool{}
	for _, r := range resources {
		if r.Kind != KindPublish {
			continue
		}
		if seen[r.Name] {
			dup = append(dup, r.Name)
		}
		seen[r.Name] = true
	}
	if len(dup) > 0 {
		sort.Strings(dup)
		return fmt.Errorf("duplicate publish name %q: a published name is unique in the zone", dup[0])
	}
	return nil
}
