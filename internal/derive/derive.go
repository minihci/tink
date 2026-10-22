// Package derive is the derive stage: the passes that work out, from what a stack declares, what a stack would otherwise make a person write by
// hand (docs/roadmap.md, Phase 3; the proposal is #177 and the join is #130). It sits above resolve and join, which it joins: resolve plans a
// stack and knows no capability, join knows the platform's helpers and the publishes, and a derived value needs both.
//
// A pass here changes the loaded resources in place, before the graph is built, so that everything after it (plan, apply, the checks) sees the
// stack as it will be planned and a derived value is never a surprise to a later pass. prepare.Prepare is where the passes run, in order.
//
// The first pass is the ingress rules of a network ACL.
package derive

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/join"
	"github.com/minihci/tink/internal/resolve"
)

// ACLRules fills the ingress rules of every `kind: network-acl` that says derive_ingress: true.
//
// The rules are the `acl` rendering of the join (docs/join.md): for each app of this stack that a publish shows, the ingress helper's addresses may
// reach the app's port, on the ACL attached to the network the app's NIC is on. They are worked out from the stack's own instances, networks and
// publishes joined to the platform as the server holds it (the helpers, with their roles, legs and addresses), not from the server's other apps: a
// stack is planned on its own, so another stack's fault cannot stop it, and the helpers are what the rules need.
//
// The direction is always declared. An ACL that nothing is published through gets `ingress: []`, not a missing list, because a missing list means
// "not managed" and would leave the rule of a publish that was removed in place (docs/network-acl.md).
//
// A network that more than one stack uses should not name an ACL that derives its rules in one of them: the rules are those of the stack being
// planned, and the other stack's would be removed as stale. Give each stack its own bridge (#177), or write the rules.
//
// A stack with no such ACL is returned untouched and the server is not read. The returned lines say what was derived, for the person running plan.
func ACLRules(server incus.InstanceServer, resources []resolve.Resource) ([]string, error) {
	var marked []int
	for i, r := range resources {
		if r.Kind == resolve.KindNetworkACL && r.DeriveIngress {
			marked = append(marked, i)
		}
	}
	if len(marked) == 0 {
		return nil, nil
	}

	platform, err := join.PlatformFromServer(server)
	if err != nil {
		return nil, fmt.Errorf("deriving ingress rules: %w", err)
	}
	res := join.Build(join.Overlay(platform, []join.Stack{{Name: "stack", Resources: resources}}))
	if res.Errors() > 0 {
		var lines []string
		for _, f := range res.Findings {
			if f.Severity == join.Error {
				lines = append(lines, "  "+f.String())
			}
		}
		names := make([]string, len(marked))
		for i, m := range marked {
			names[i] = "network-acl/" + resources[m].Name
		}
		return nil, fmt.Errorf("cannot derive the ingress rules of %s:\n%s", strings.Join(names, ", "), strings.Join(lines, "\n"))
	}

	rulesOf := map[string]join.ACLRuleSet{}
	for _, set := range join.ACLRules(res) {
		rulesOf[set.ACL] = set
	}
	var notices []string
	for _, m := range marked {
		name := resources[m].Name
		set := rulesOf[name]
		rules := make([]api.NetworkACLRule, 0, len(set.Rules)) // never nil: the direction is declared
		var labels []string
		for _, rule := range set.Rules {
			rules = append(rules, api.NetworkACLRule{
				Action:          "allow",
				Source:          rule.Sources,
				Protocol:        "tcp",
				DestinationPort: strconv.Itoa(rule.Port),
				State:           "enabled",
				Description:     rule.Description(),
			})
			labels = append(labels, rule.Labels...)
		}
		resources[m].Ingress = rules
		sort.Strings(labels)
		if len(rules) == 0 {
			notices = append(notices, fmt.Sprintf("network-acl/%s: no publish reaches an app behind it, so its ingress rules are none", name))
		} else {
			notices = append(notices, fmt.Sprintf("network-acl/%s: %d ingress rule(s) derived from the publishes %s", name, len(rules), strings.Join(labels, ", ")))
		}
	}
	return notices, nil
}
