package resolve

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/ownership"
)

// A kind: network-acl resource declares an Incus NETWORK ACL: a named list of firewall rules that a network (`security.acls` in its config) or an
// instance's NIC (`security.acls` on the device) can be held to. Until now a stack could name an ACL but not state it, so the rule that isolates a
// bridge was hand-made host state, and `kind: incus` (argv for the local CLI) is blocked under --remote. docs/network-acl.md has the reasoning and
// a complete example.
//
// What this does, and deliberately no more:
//
//   - Creates a missing ACL and updates one that differs. It never deletes an ACL, like every other kind.
//   - Is server-global, exactly like kind: network. Incus resolves the project an ACL lives in with the same rule it uses for networks
//     (project.NetworkProject): the default project, unless the project asked about has features.networks, which tink does not manage. So an ACL
//     for a bridge is in the default project whatever project its instances are in, a `project:` here is a load error rather than something silently
//     ignored, and the stack's default project (stackproject.go) is never applied to it.
//   - Takes Incus's field names for the ACL and for every rule (action, source, destination, protocol, source_port, destination_port, icmp_type,
//     icmp_code, description, state), so `incus network acl show` output pastes in unchanged. Rules are compared as an ORDERED list and sent in the order
//     declared. Incus says the order does not change filtering (it applies drop, then reject, then allow, whatever the list order), so a reorder is
//     reported as a reorder and never as a change of meaning; but the stored list is what a reader sees with `incus network acl show`, and keeping it
//     equal to the YAML keeps a diff readable.
//   - Treats a direction it was given as the whole list for that direction: a live rule the stack does not list is removed on apply, because a
//     stale `allow` left in a firewall is the kind of drift this is for. A direction the stack leaves out (`ingress` not written at all) is not
//     managed and its live rules are untouched; `ingress: []` says "none". This is a different rule from deleting the ACL, which never happens.
//   - Defaults a rule's `state` to enabled, as the incus CLI does (the API itself requires it), and trims the whitespace Incus trims, so a second
//     plan reads `no changes`.
//
// Attaching needs no field of its own: a network attaches ACLs through its config (security.acls, security.acls.default.ingress.action, ...) and a
// NIC through security.acls on the device. A name there that the stack declares is an ordering edge (Resource.dependencies), because Incus refuses
// to attach an ACL that does not exist; one the stack does not declare stays lenient, because it may exist already, and plan says so when it can see
// that it does not (aclReferenceWarnings).
//
// Not here: address sets, OVN, `tink export` of ACLs, rendering ACLs from other documents, and anything that deletes an ACL.

// Incus's own lists (internal/server/network/acl/driver_common.go, v7.4.0). allow-stateless is Incus's too: accepted by the API and, on a bridge,
// applied after the plain allow rules, so tink accepts every word Incus does.
var (
	aclActions   = []string{"allow", "allow-stateless", "drop", "reject"}
	aclStates    = []string{"enabled", "disabled", "logged"}
	aclProtocols = []string{"icmp4", "icmp6", "tcp", "udp"}
)

// aclRules returns the rules as Incus will hold them: whitespace trimmed as Incus trims it (NetworkACLRule.Normalise) and an empty state read as
// enabled, which is what the incus CLI sends when none is given. The API refuses a rule with no state, so this is also what makes the request valid.
// Comparing two lists that went through it is how a plan reads `no changes` on the second run.
func aclRules(rules []api.NetworkACLRule) []api.NetworkACLRule {
	if len(rules) == 0 {
		return nil
	}
	out := make([]api.NetworkACLRule, len(rules))
	for i, rule := range rules {
		rule.Normalise()
		if rule.State == "" {
			rule.State = "enabled"
		}
		out[i] = rule
	}
	return out
}

func validateNetworkACL(r Resource) error {
	if r.Kind != KindNetworkACL {
		return nil
	}
	if r.Project != "" {
		return fmt.Errorf("resource %q: kind network-acl is server-global and takes no project: (Incus keeps an ACL in the default project unless the project has features.networks, which tink does not manage)", r.Name)
	}
	if err := validateACLName(r.Name); err != nil {
		return fmt.Errorf("resource %q: not a valid network ACL name: %w", r.Name, err)
	}
	for k := range r.Config {
		if k == "" {
			return fmt.Errorf("resource %q: config has an empty key", r.Name)
		}
	}
	if r.DeriveIngress && r.Ingress != nil {
		return fmt.Errorf("resource %q: derive_ingress and ingress: are exclusive: either tink works the ingress rules out from the publishes, or you write them", r.Name)
	}
	if err := validateACLRules(r.Name, "ingress", r.Ingress); err != nil {
		return err
	}
	return validateACLRules(r.Name, "egress", r.Egress)
}

// aclNameRE is the character rule of Incus's ACL names. Incus (acl.ValidName) requires an API name that is also a valid hostname, so that an ACL
// can be told from an address in a rule; the hostname rule is the stricter of the two and leaves no room for the characters Incus reserves for its own
// selectors (@, % and #), so this is all of it. It is written out here, not imported, because Incus's validate package would add two modules to go.mod.
var aclNameRE = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// validateACLName applies Incus's rule for an ACL's name: 1 to 63 letters, digits and hyphens, not starting or ending with a hyphen, and not all digits.
func validateACLName(name string) error {
	switch {
	case len(name) < 1 || len(name) > 63:
		return fmt.Errorf("a name is 1 to 63 characters (%d given)", len(name))
	case !aclNameRE.MatchString(name):
		return fmt.Errorf("a name may use only letters, digits and '-' (Incus also keeps '@', '%%' and '#' for its own selectors)")
	case strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-"):
		return fmt.Errorf("a name cannot start or end with '-'")
	}
	if _, err := strconv.ParseUint(name, 10, 64); err == nil {
		return fmt.Errorf("a name cannot be a number")
	}
	return nil
}

// validateACLRules checks what Incus checks about a rule's words and the combinations it always refuses, so a plan never says `would create` for
// an ACL that apply cannot make. The errors name the resource, the direction and the rule's index in the list as written (counting from 0, as Incus
// does). Addresses and ports are left to the server: a typo in a CIDR is its error to report.
func validateACLRules(name, direction string, declared []api.NetworkACLRule) error {
	rules := aclRules(declared)
	for i, rule := range rules {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("resource %q: %s rule %d: %s", name, direction, i, fmt.Sprintf(format, args...))
		}
		if !slices.Contains(aclActions, rule.Action) {
			return fail("action %q is not one of: %s", rule.Action, strings.Join(aclActions, ", "))
		}
		if !slices.Contains(aclStates, rule.State) {
			return fail("state %q is not one of: %s", rule.State, strings.Join(aclStates, ", "))
		}
		if rule.Protocol != "" && !slices.Contains(aclProtocols, rule.Protocol) {
			hint := ""
			if rule.Protocol == "icmp" {
				hint = " (icmp4 for IPv4, icmp6 for IPv6)"
			}
			return fail("protocol %q is not one of: %s, or left out for any%s", rule.Protocol, strings.Join(aclProtocols, ", "), hint)
		}
		hasPorts := rule.Protocol == "tcp" || rule.Protocol == "udp"
		hasICMP := rule.Protocol == "icmp4" || rule.Protocol == "icmp6"
		if !hasPorts && (rule.SourcePort != "" || rule.DestinationPort != "") {
			return fail("source_port and destination_port need protocol tcp or udp")
		}
		if !hasICMP && (rule.ICMPType != "" || rule.ICMPCode != "") {
			return fail("icmp_type and icmp_code need protocol icmp4 or icmp6")
		}
		for j := 0; j < i; j++ {
			if rules[j] == rule {
				return fail("repeats rule %d, which Incus refuses", j)
			}
		}
	}
	return nil
}

// aclRef is one place a resource names an ACL, and where, for a message.
type aclRef struct{ name, where string }

// aclRefs lists the ACLs a resource attaches: those in a network's `security.acls`, and those in the `security.acls` of each NIC device (on a profile
// or an instance). Both are comma lists. Order is stable: the network's, then the devices by name.
func (r Resource) aclRefs() []aclRef {
	var refs []aclRef
	add := func(list, where string) {
		for _, name := range strings.Split(list, ",") {
			if name = strings.TrimSpace(name); name != "" {
				refs = append(refs, aclRef{name, where})
			}
		}
	}
	if r.Kind == KindNetwork {
		add(r.Config["security.acls"], "config security.acls")
	}
	devices := make([]string, 0, len(r.Devices))
	for name := range r.Devices {
		devices = append(devices, name)
	}
	sort.Strings(devices)
	for _, name := range devices {
		if dev := r.Devices[name]; dev["type"] == "nic" {
			add(dev["security.acls"], fmt.Sprintf("device %q security.acls", name))
		}
	}
	return refs
}

// declaredACLs is the set of ACL names the WHOLE stack declares, for aclReferenceWarnings: an ACL and the network that names it land in different levels.
func declaredACLs(resources []Resource) map[string]bool {
	out := map[string]bool{}
	for _, r := range resources {
		if r.Kind == KindNetworkACL {
			out[r.Name] = true
		}
	}
	return out
}

// aclReferenceWarnings is the plan finding for an ACL a resource names that the stack does not declare and the server does not have. Naming one that
// exists already is normal and silent; naming one that exists nowhere means Incus refuses the change, which plan can say before apply. A server that
// cannot be asked (403, a dropped connection) is reported as unverified and never as missing, the same rule as every other lookup.
func aclReferenceWarnings(server incus.InstanceServer, r Resource, declared map[string]bool) []string {
	var out []string
	checked := map[string]bool{}
	for _, ref := range r.aclRefs() {
		if declared[ref.name] || checked[ref.name] {
			continue
		}
		checked[ref.name] = true
		_, _, found, err := incusapi.LookupNetworkACL(server, ref.name)
		switch {
		case err != nil:
			out = append(out, fmt.Sprintf("%s names the network ACL %q, which this stack does not declare, and the server could not be asked whether it exists: %v", ref.where, ref.name, err))
		case !found:
			out = append(out, fmt.Sprintf("%s names the network ACL %q, which this stack does not declare and the server does not have: Incus will refuse to apply it until the ACL exists (declare a kind: network-acl for it)", ref.where, ref.name))
		}
	}
	return out
}

func planNetworkACL(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, found, err := incusapi.LookupNetworkACL(server, r.Name)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live network ACL: %w", err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}

	var changes []string
	if r.Description != "" && r.Description != current.Description {
		changes = append(changes, fmt.Sprintf("description: %q -> %q", current.Description, r.Description))
	}
	changes = append(changes, diffConfig(current.Config, r.Config, nil)...)
	if r.Ingress != nil {
		changes = append(changes, diffACLRules("ingress", aclRules(current.Ingress), aclRules(r.Ingress))...)
	}
	if r.Egress != nil {
		changes = append(changes, diffACLRules("egress", aclRules(current.Egress), aclRules(r.Egress))...)
	}
	if len(changes) == 0 {
		return observed(PlannedResource{Resource: r, Action: ActionNone}, current.Config), nil
	}
	return observed(PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes}, current.Config), nil
}

// createNetworkACL creates the ACL with the ownership stamp of stack (ownership.go) in the creation request itself; a stack with no name stamps nothing.
func createNetworkACL(server incus.InstanceServer, r Resource, stack string) error {
	return server.CreateNetworkACL(api.NetworkACLsPost{
		NetworkACLPost: api.NetworkACLPost{Name: r.Name},
		NetworkACLPut: api.NetworkACLPut{
			Description: r.Description,
			Ingress:     aclRules(r.Ingress),
			Egress:      aclRules(r.Egress),
			Config:      ownership.With(r.Config, stack),
		},
	})
}

// updateNetworkACL writes what the stack declares onto the live ACL: the description if it names one, the declared config keys merged into the live
// ones (as for a network), and each declared rule list in place of the live one. A direction the stack does not declare is sent back as it is.
func updateNetworkACL(server incus.InstanceServer, r Resource) error {
	return incusapi.RetryOnConflict(func() error {
		current, etag, err := server.GetNetworkACL(r.Name)
		if err != nil {
			return err
		}
		put := current.Writable()
		if r.Description != "" {
			put.Description = r.Description
		}
		if put.Config == nil {
			put.Config = api.ConfigMap{}
		}
		for k, v := range r.Config {
			put.Config[k] = v
		}
		if r.Ingress != nil {
			put.Ingress = aclRules(r.Ingress)
		}
		if r.Egress != nil {
			put.Egress = aclRules(r.Egress)
		}
		return server.UpdateNetworkACL(r.Name, put, etag)
	})
}

// aclRuleFields lists a rule's fields in the order Incus documents them and `incus network acl rule add` takes them.
func aclRuleFields(r api.NetworkACLRule) [][2]string {
	return [][2]string{
		{"action", r.Action}, {"source", r.Source}, {"destination", r.Destination}, {"protocol", r.Protocol},
		{"source_port", r.SourcePort}, {"destination_port", r.DestinationPort}, {"icmp_type", r.ICMPType}, {"icmp_code", r.ICMPCode},
		{"description", r.Description}, {"state", r.State},
	}
}

// aclRuleSummary writes a rule as the arguments `incus network acl rule add` takes (the lab recipe), leaving out what is unset.
func aclRuleSummary(r api.NetworkACLRule) string {
	var parts []string
	for _, f := range aclRuleFields(r) {
		switch {
		case f[1] == "":
		case f[0] == "description":
			parts = append(parts, fmt.Sprintf("%s=%q", f[0], f[1]))
		default:
			parts = append(parts, f[0]+"="+f[1])
		}
	}
	return strings.Join(parts, " ")
}

// diffACLRules compares two lists that went through aclRules, as ORDERED lists, and says which rule differs: a rule added (its index in the declared
// list), a rule removed (its index in the live list), or a rule changed (the fields that differ, at its index in the declared list). A list with the
// same rules in another order is one line saying so. Rules that match are found by the longest common subsequence, so inserting a rule in the middle
// is one `add`, and not a change to every rule after it.
func diffACLRules(direction string, current, desired []api.NetworkACLRule) []string {
	if slices.Equal(current, desired) {
		return nil
	}
	if len(current) == len(desired) && sameRules(current, desired) {
		return []string{fmt.Sprintf("%s: rules reordered (the same %d; Incus applies drop, then reject, then allow whatever the order, so filtering is unchanged): %s -> %s",
			direction, len(desired), summaries(current), summaries(desired))}
	}

	var out []string
	ci, di := 0, 0
	segment := func(cEnd, dEnd int) { // the rules between two matches: pair them up as changes, then what is left over is removed or added
		removed, added := current[ci:cEnd], desired[di:dEnd]
		k := 0
		for ; k < len(removed) && k < len(added); k++ {
			for f, have := range aclRuleFields(removed[k]) {
				if want := aclRuleFields(added[k])[f]; have[1] != want[1] {
					out = append(out, fmt.Sprintf("%s rule %d: %s: %q -> %q", direction, di+k, have[0], have[1], want[1]))
				}
			}
		}
		for j := k; j < len(removed); j++ {
			out = append(out, fmt.Sprintf("%s rule %d (in the live list): remove (%s)", direction, ci+j, aclRuleSummary(removed[j])))
		}
		for j := k; j < len(added); j++ {
			out = append(out, fmt.Sprintf("%s rule %d: add (%s)", direction, di+j, aclRuleSummary(added[j])))
		}
	}
	for _, m := range matchingRules(current, desired) {
		segment(m[0], m[1])
		ci, di = m[0]+1, m[1]+1
	}
	segment(len(current), len(desired))
	return out
}

// matchingRules returns the (live index, declared index) pairs of the longest common subsequence of the two lists, in order.
func matchingRules(a, b []api.NetworkACLRule) [][2]int {
	n, m := len(a), len(b)
	best := make([][]int, n+1)
	for i := range best {
		best[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				best[i][j] = best[i+1][j+1] + 1
			} else {
				best[i][j] = max(best[i+1][j], best[i][j+1])
			}
		}
	}
	var pairs [][2]int
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			pairs = append(pairs, [2]int{i, j})
			i++
			j++
		case best[i+1][j] >= best[i][j+1]:
			i++
		default:
			j++
		}
	}
	return pairs
}

// sameRules reports whether two lists hold the same rules, counting repeats, in any order.
func sameRules(a, b []api.NetworkACLRule) bool {
	count := map[api.NetworkACLRule]int{}
	for _, r := range a {
		count[r]++
	}
	for _, r := range b {
		count[r]--
	}
	for _, n := range count {
		if n != 0 {
			return false
		}
	}
	return true
}

func summaries(rules []api.NetworkACLRule) string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = "[" + aclRuleSummary(r) + "]"
	}
	return strings.Join(out, " ")
}
