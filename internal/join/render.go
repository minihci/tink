package join

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The five renderings. Each is a pure function of a Result with no errors, so its output is a pure function of the documents: the same files print the
// same bytes. They are shaped for the engines tink uses today, and each says in Section.Title where it is meant to go.
//
// Two names the Caddy rendering imports are the platform's to define, by hand, in its Caddyfile: the snippet `sso` (the forward-auth call to the sign-on
// helper) and the snippet `no_portal_cookie` (which strips the portal's zone-wide session cookie before a request reaches the app). The join only
// uses them, so a route can never be rendered without either.
const (
	snippetSSO    = "sso"
	snippetNoSess = "no_portal_cookie"
	policyDefault = "one_factor"
)

// Section is one rendering, named for `tink join --only`.
type Section struct {
	Name  string // dns, mesh, caddy, sso or acl
	Title string
	Body  string
}

// Sections lists the renderings in a fixed order. A rendering that is empty because it does not apply (no mesh helper, no ACL that can be seen) is
// left out.
func (r Result) Sections() []Section {
	all := []Section{
		{"dns", fmt.Sprintf("records for the dns helper's hosts file (zone %s)", r.Zone), r.DNS},
		{"mesh", "extra records for the mesh control plane, so mesh clients get the mesh address (a JSON array)", r.Mesh},
		{"caddy", fmt.Sprintf("routes for the proxy, to sit inside the wildcard site block (they import the snippets %s and %s, which the platform's Caddyfile defines)", snippetSSO, snippetNoSess), r.Caddy},
		{"sso", "rules for the sign-on helper, to sit under access_control.rules", r.SSO},
		{"acl", "rules for the ACLs on the apps' bridges, under ingress:", r.ACL},
	}
	var out []Section
	for _, s := range all {
		if s.Body != "" {
			out = append(out, s)
		}
	}
	return out
}

// renderDNS is a hosts-file line for every published name, all answering with the ingress helper's LAN address.
func renderDNS(r Result) string {
	if r.DNSInstance == "" { // no dns helper: the names live in public DNS, and a warning says which
		return ""
	}
	var b strings.Builder
	for _, p := range r.Published {
		fmt.Fprintf(&b, "%s %s\n", r.LANAddress, p.FQDN)
	}
	return b.String()
}

type meshRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// renderMesh is Headscale's extra-records format: one A record per published name, answering with the ingress helper's address on the mesh leg. Empty
// when no mesh helper is declared.
func renderMesh(r Result) string {
	if !r.HasMesh {
		return ""
	}
	recs := make([]meshRecord, 0, len(r.Published))
	for _, p := range r.Published {
		recs = append(recs, meshRecord{p.FQDN, "A", r.MeshAddress})
	}
	out, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return "" // cannot happen: strings only
	}
	return string(out) + "\n"
}

// renderCaddy is one route per published name, as `handle` fragments for the wildcard site block: a site block per name would ask for its own
// certificate instead of using the wildcard one. The upstream is the app's address, not its name, because names do not resolve across bridges.
func renderCaddy(r Result) string {
	var b strings.Builder
	for _, p := range r.Published {
		m := matcherName(p.Label)
		fmt.Fprintf(&b, "@%s host %s\n", m, p.FQDN)
		fmt.Fprintf(&b, "handle @%s {\n", m)
		fmt.Fprintf(&b, "\timport %s\n", snippetSSO)
		fmt.Fprintf(&b, "\treverse_proxy %s:%d {\n", p.Address, p.Offer.Port)
		fmt.Fprintf(&b, "\t\timport %s\n", snippetNoSess)
		fmt.Fprintf(&b, "\t}\n}\n")
	}
	return b.String()
}

// renderSSO is one Authelia access rule per published name: who may open it is the publish's groups, how strongly they must sign in is the platform's
// default (a publish does not say; a field for it arrives when something needs two factors). One group is written as Authelia's plain `subject`; several
// as the OR of single-group lists.
func renderSSO(r Result) string {
	var b strings.Builder
	for _, p := range r.Published {
		fmt.Fprintf(&b, "- domain: '%s'\n", p.FQDN)
		fmt.Fprintf(&b, "  policy: %s\n", policyDefault)
		if len(p.Allow) == 1 {
			fmt.Fprintf(&b, "  subject: 'group:%s'\n", p.Allow[0])
			continue
		}
		parts := make([]string, len(p.Allow))
		for i, g := range p.Allow {
			parts[i] = fmt.Sprintf("['group:%s']", g)
		}
		fmt.Fprintf(&b, "  subject: [%s]\n", strings.Join(parts, ", "))
	}
	return b.String()
}

// ACLRule is one ingress rule the join derives for an ACL: the ingress helper's addresses may reach one port of the apps behind it.
type ACLRule struct {
	Port    int
	Sources string   // the ingress helper's addresses as an Incus ACL rule takes them: address/32, comma-separated
	Labels  []string // the publishes this rule covers, sorted
}

// Description is what the rule says about itself in `incus network acl show`.
func (a ACLRule) Description() string {
	return "ingress helper publishes " + strings.Join(a.Labels, ", ")
}

// ACLRuleSet is everything the join derives for one ACL: the networks it is found from and its rules, by port.
type ACLRuleSet struct {
	ACL      string
	Networks []string
	Rules    []ACLRule
}

// ACLRules is the `acl` rendering as data: for each ACL that an app with a publish sits behind, the rules that admit the ingress helper. It is
// what renderACL writes as text and what the derive stage fills into an ACL that asks for its rules to be derived, so the two cannot differ.
func ACLRules(r Result) []ACLRuleSet {
	byACL := map[string]map[int]*ACLRule{}
	nets := map[string]map[string]bool{}
	for _, p := range r.Published {
		if p.ACL == "" {
			continue
		}
		if byACL[p.ACL] == nil {
			byACL[p.ACL] = map[int]*ACLRule{}
			nets[p.ACL] = map[string]bool{}
		}
		nets[p.ACL][p.Network] = true
		rule := byACL[p.ACL][p.Offer.Port]
		if rule == nil {
			rule = &ACLRule{Port: p.Offer.Port}
			byACL[p.ACL][p.Offer.Port] = rule
		}
		rule.Labels = append(rule.Labels, p.Label)
	}
	masked := make([]string, len(r.Sources))
	for i, a := range r.Sources {
		masked[i] = a + "/32"
	}
	sources := strings.Join(masked, ",") // an Incus ACL rule takes a comma-separated list of sources
	names := make([]string, 0, len(byACL))
	for n := range byACL {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ACLRuleSet, 0, len(names))
	for _, n := range names {
		set := ACLRuleSet{ACL: n}
		for nw := range nets[n] {
			set.Networks = append(set.Networks, nw)
		}
		sort.Strings(set.Networks)
		ports := make([]int, 0, len(byACL[n]))
		for port := range byACL[n] {
			ports = append(ports, port)
		}
		sort.Ints(ports)
		for _, port := range ports {
			rule := byACL[n][port]
			sort.Strings(rule.Labels)
			rule.Sources = sources
			set.Rules = append(set.Rules, *rule)
		}
		out = append(out, set)
	}
	return out
}

func renderACL(r Result) string {
	var b strings.Builder
	for _, set := range ACLRules(r) {
		fmt.Fprintf(&b, "# network-acl %s (network %s)\ningress:\n", set.ACL, strings.Join(set.Networks, ", "))
		for _, rule := range set.Rules {
			fmt.Fprintf(&b, "  - {action: allow, source: %s, protocol: tcp, destination_port: \"%d\", state: enabled, description: \"%s\"}\n",
				rule.Sources, rule.Port, rule.Description())
		}
	}
	return b.String()
}
