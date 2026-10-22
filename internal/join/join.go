// Package join computes, from the stack files a site declares, what the site's helpers would have to say for its published names to work, and
// prints it. It is the "join" of the offer-grant design (docs/join.md; the unbuilt parts are proposed in #130): the app states an offer (a port on one of its NICs), the site publishes it
// (a `kind: publish`: a name, and the groups that may use it), and the join looks across both, and across the platform helpers' own stamps, to
// derive the pieces no one should have to keep in step by hand: the dns records, the mesh view, the proxy's routes, the sign-on rules, and the ACL on
// the app's bridge.
//
// What it does, and deliberately no more:
//
//   - Build is a pure function of the documents it is given. It reads no server, holds no credential and writes nothing: `tink join` prints, and its
//     output is compared by eye or by diff with the hand-written pieces it will one day replace. The documents come from stack files (resolve.LoadFiles)
//     or from a live server (FromServer, server.go: the instances, profiles and networks as they are, and the publishes as the stamps `plan apply` put on
//     the instances), and Build cannot tell which. Applying any of it is later work, and needs the decisions about who holds the credential for
//     reconcile jobs (#130, open question 7).
//   - It fails closed. If any finding is an error it renders nothing, because a partial rendering (a mesh view missing one name) looks exactly like a
//     complete one. Warnings are reported beside a complete rendering.
//   - It knows no engine. The helpers are found by `user.tink.role`, the addresses by `user.tink.leg.<nic>`, the zone by `user.tink.dns.zone`; what
//     it prints is shaped for the engines tink uses today (hosts-file records, Headscale's extra records, Caddy, Authelia, an Incus ACL), but the facts
//     it derives from do not name them.
package join

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/resolve"
)

// Stack is one loaded stack file. Several stacks are joined at once: the apps' and the platform's are written separately, and the join is the one
// place that reads across them.
type Stack struct {
	Name      string
	Resources []resolve.Resource // as resolve.LoadFiles returns them: each carries its stack's default project
}

// Severity says whether a finding stops the rendering.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

// A Finding is something the join noticed that a person should read. Subject names the document it is about ("publish/whoami", "instance/whoami").
type Finding struct {
	Severity Severity
	Subject  string
	Message  string
}

func (f Finding) String() string {
	if f.Subject == "" {
		return fmt.Sprintf("%s: %s", f.Severity, f.Message)
	}
	return fmt.Sprintf("%s: %s: %s", f.Severity, f.Subject, f.Message)
}

// Published is one publish, joined to the offer it shows and the facts the renderings need.
type Published struct {
	Label    string // the publish's name: the host label
	FQDN     string // Label plus the zone
	Project  string
	Instance string
	Offer    Offer
	Allow    []string // groups, as declared
	Address  string   // the listening NIC's static IPv4 address
	Network  string   // the managed network that NIC is on
	ACL      string   // the ACL attached to that network, or "" when it cannot be seen
}

// Result is what the join found and what it would render.
type Result struct {
	Zone        string
	DNSProject  string // where the dns helper lives, so the DNS applier knows where to write
	DNSInstance string
	LANAddress  string // the ingress helper's address on its `serves` leg: what the dns records answer with
	MeshAddress string // the ingress helper's address on its `mesh` leg: what the mesh view answers with
	// PublicAddress is the ingress helper's address on its `public` leg: where the host's proxy devices carry traffic from the internet. Nothing is rendered
	// from it except that it is one of the sources below.
	PublicAddress string
	// Sources are the addresses the apps' ACLs admit: the ingress helper's addresses on its `mesh` and `public` legs, sorted, without repeats. They are
	// what the helper reaches the apps from, and a site needs at least one.
	Sources   []string
	HasMesh   bool // a helper with role mesh is declared
	Published []Published
	Findings  []Finding

	// The five renderings. All are empty when any finding is an error.
	DNS, Mesh, Caddy, SSO, ACL string
}

// Errors counts the findings that stop the rendering.
func (r Result) Errors() int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == Error {
			n++
		}
	}
	return n
}

type key struct{ project, name string }

// projectOf is the project a resource lives in, with the server's default spelled out so a resource that names none and one that names "default" meet.
func projectOf(r resolve.Resource) string {
	if r.Project == "" {
		return "default"
	}
	return r.Project
}

var zonePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// Build joins the stacks.
func Build(stacks []Stack) Result {
	var res Result
	add := func(sev Severity, subject, format string, args ...any) {
		res.Findings = append(res.Findings, Finding{sev, subject, fmt.Sprintf(format, args...)})
	}

	instances := map[key]resolve.Resource{}
	profiles := map[key]resolve.Resource{}
	networks := map[string]resolve.Resource{}
	roles := map[string][]resolve.Resource{}
	var publishes []resolve.Resource
	for _, s := range stacks {
		for _, r := range s.Resources {
			switch r.Kind {
			case resolve.KindInstance:
				k := key{projectOf(r), r.Name}
				if _, dup := instances[k]; dup {
					add(Error, "instance/"+r.Name, "declared twice (project %s) across the files given", k.project)
					continue
				}
				instances[k] = r
				if role := strings.TrimSpace(r.Config[roleKey]); role != "" {
					roles[role] = append(roles[role], r)
				}
			case resolve.KindProfile:
				profiles[key{projectOf(r), r.Name}] = r
			case resolve.KindNetwork:
				networks[r.Name] = r
			case resolve.KindPublish:
				publishes = append(publishes, r)
			}
		}
	}

	// Offers, parsed once per instance. Parse findings are about the instance, not about any one publish.
	offers := map[key]map[string]Offer{}
	instKeys := make([]key, 0, len(instances))
	for k := range instances {
		instKeys = append(instKeys, k)
	}
	sort.Slice(instKeys, func(i, j int) bool {
		if instKeys[i].project != instKeys[j].project {
			return instKeys[i].project < instKeys[j].project
		}
		return instKeys[i].name < instKeys[j].name
	})
	for _, k := range instKeys {
		o, fs := parseOffers(instances[k])
		offers[k] = o
		res.Findings = append(res.Findings, fs...)
	}

	// An app's http offer that no publish shows is worth a word. Helpers (anything with a role) reach people through the platform's own routes, so
	// they are not asked.
	published := map[string]bool{}
	for _, p := range publishes {
		published[fmt.Sprintf("%s/%s/%s", projectOf(p), p.Instance, p.Offer)] = true
	}
	for _, k := range instKeys {
		if instances[k].Config[roleKey] != "" {
			continue
		}
		names := make([]string, 0, len(offers[k]))
		for n := range offers[k] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if offers[k][n].Proto == protoHTTP && !published[fmt.Sprintf("%s/%s/%s", k.project, k.name, n)] {
				add(Warning, "instance/"+k.name, "offer %q (http) is shown by no publish: nobody can open it through the site's door", n)
			}
		}
	}

	if len(publishes) == 0 {
		res.HasMesh = len(roles["mesh"]) > 0 // a fact about the site, not about what is published
		add(Warning, "", "no publish is declared in the files given, so there is nothing to render")
		if len(roles["dns"]) == 1 { // no error either way: a site with no publishes need not have a dns helper; this only tells an explicit empty apply where to write
			d := roles["dns"][0]
			res.DNSProject, res.DNSInstance, res.Zone = projectOf(d), d.Name, strings.TrimSpace(d.Config[zoneKey])
		}
		sortFindings(res.Findings)
		return res
	}

	// The platform's own facts: found by role, never by instance name.
	only := func(role string) (resolve.Resource, bool) {
		switch len(roles[role]) {
		case 1:
			return roles[role][0], true
		case 0:
			add(Error, role+" helper", "no instance has %s: %s, and a publish needs one", roleKey, role)
		default:
			add(Error, role+" helper", "%d instances have %s: %s, and the join needs exactly one", len(roles[role]), roleKey, role)
		}
		return resolve.Resource{}, false
	}
	// The ingress helper is read first: with no dns helper (a site whose names live in public DNS) it is where the zone is stated.
	ing, haveIngress := only("ingress")
	switch len(roles["dns"]) {
	case 1:
		dns := roles["dns"][0]
		res.DNSProject, res.DNSInstance = projectOf(dns), dns.Name
		res.Zone = strings.TrimSpace(dns.Config[zoneKey])
		if !zonePattern.MatchString(res.Zone) {
			add(Error, "instance/"+dns.Name, "%s is %q: the dns helper must state the zone it serves (for example home.example.org), the part every published name ends with", zoneKey, res.Zone)
			res.Zone = ""
		}
		if z := strings.TrimSpace(ing.Config[zoneKey]); haveIngress && z != "" && res.Zone != "" && z != res.Zone {
			add(Error, "instance/"+ing.Name, "%s is %q, but the dns helper serves %q: a site has one zone", zoneKey, z, res.Zone)
		}
	case 0:
		if haveIngress {
			res.Zone = strings.TrimSpace(ing.Config[zoneKey])
			switch {
			case res.Zone == "":
				add(Error, "dns helper", "no instance has %s: dns, and the ingress helper states no zone (%s): a published name ends with the zone, and with no dns helper the ingress helper is where it is stated", roleKey, zoneKey)
			case !zonePattern.MatchString(res.Zone):
				add(Error, "instance/"+ing.Name, "%s is %q: with no dns helper the ingress helper must state the zone the published names end with (for example apps.example.org)", zoneKey, res.Zone)
				res.Zone = ""
			default:
				names := make([]string, 0, len(publishes))
				for _, p := range publishes {
					names = append(names, p.Name+"."+res.Zone)
				}
				sort.Strings(names)
				add(Warning, "dns helper", "no instance has %s: dns, so no dns records are rendered: point DNS for %s at the host that carries the ingress helper's traffic", roleKey, strings.Join(names, ", "))
			}
		}
	default:
		add(Error, "dns helper", "%d instances have %s: dns, and the join needs exactly one", len(roles["dns"]), roleKey)
	}
	if haveIngress {
		lg := legAddresses(ing, profiles, len(roles["dns"]) > 0, len(roles["mesh"]) > 0, add)
		res.LANAddress, res.MeshAddress, res.PublicAddress, res.Sources = lg.serves, lg.mesh, lg.public, lg.sources
	}
	if len(roles["sso"]) == 0 {
		add(Error, "sso helper", "no instance has %s: sso, and a publish is a sign-on rule that something has to enforce", roleKey)
	}
	res.HasMesh = len(roles["mesh"]) > 0
	if !res.HasMesh && res.MeshAddress != "" { // a site with no mesh leg never had a mesh to leave out
		add(Warning, "mesh helper", "no instance has %s: mesh, so the names are not offered to mesh clients", roleKey)
	}

	// The publishes, one at a time.
	labels := map[string]string{}
	for _, p := range publishes {
		subject := "publish/" + p.Name
		if prev, dup := labels[p.Name]; dup {
			add(Error, subject, "the name %q is also published by %s: a published name is unique in the zone", p.Name, prev)
			continue
		}
		labels[p.Name] = projectOf(p) + "/" + p.Instance
		pub, ok := resolvePublish(p, instances, offers, profiles, networks, add)
		if !ok {
			continue
		}
		pub.Label = p.Name
		if res.Zone != "" {
			pub.FQDN = p.Name + "." + res.Zone
		}
		res.Published = append(res.Published, pub)
	}
	sort.Slice(res.Published, func(i, j int) bool { return res.Published[i].Label < res.Published[j].Label })

	sortFindings(res.Findings)
	if res.Errors() > 0 {
		return res
	}
	res.DNS = renderDNS(res)
	res.Mesh = renderMesh(res)
	res.Caddy = renderCaddy(res)
	res.SSO = renderSSO(res)
	res.ACL = renderACL(res)
	return res
}

// legs are the addresses read from the ingress helper's legs.
type legs struct {
	serves, mesh, public string
	sources              []string // the addresses of the mesh and public legs, sorted, without repeats: what the apps' ACLs admit
}

// legPurposes are the words a leg may be.
var legPurposes = map[string]bool{"serves": true, "mesh": true, "public": true, "discovery": true}

// legAddresses reads the ingress helper's legs: user.tink.leg.<nic> says why a NIC is where it is, in one word or several separated by commas:
//
//	serves     clients on the LAN reach it here: the address the dns records answer with. Needed when there is a dns helper.
//	mesh       mesh clients reach it here: the address the mesh view answers with, and a source the apps' ACLs admit. Needed when there is a mesh helper.
//	public     the internet reaches it here, through the host's address, carried by proxy devices that connect to this NIC. A source the apps' ACLs admit.
//	discovery  not a way in for anyone; ignored here.
//
// Each rendering needs only what it renders. The dns records need a dns helper and a serves leg, the mesh records a mesh helper and a mesh leg; the proxy's
// routes, the sign-on rules and the apps' ACLs need neither, but the ACLs need at least one address to admit, so a site has a mesh leg or a public leg. At most
// one NIC has each purpose, and the NIC of a purpose that is read must have a static address.
func legAddresses(ing resolve.Resource, profiles map[key]resolve.Resource, needServes, needMesh bool, add func(Severity, string, string, ...any)) legs {
	subject := "instance/" + ing.Name
	devs := effectiveDevices(ing, profiles)
	byPurpose := map[string][]string{}
	for k, v := range ing.Config {
		if !strings.HasPrefix(k, legKey) {
			continue
		}
		nic := strings.TrimPrefix(k, legKey)
		purposes := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
		if len(purposes) == 0 {
			add(Error, subject, "%s is %q: a leg is serves, mesh, public or discovery (a NIC can have several, separated by commas)", k, v)
		}
		for _, p := range purposes {
			if !legPurposes[p] {
				add(Error, subject, "%s is %q: a leg is serves, mesh, public or discovery (a NIC can have several, separated by commas)", k, v)
				continue
			}
			byPurpose[p] = append(byPurpose[p], nic)
		}
	}
	pick := func(purpose, why string, required bool) (nic, addr string) {
		nics := byPurpose[purpose]
		sort.Strings(nics)
		switch len(nics) {
		case 0:
			if required {
				add(Error, subject, "no NIC is stamped %s<nic>: %s (%s)", legKey, purpose, why)
			}
			return "", ""
		case 1:
			dev, ok := devs[nics[0]]
			if !ok || dev["type"] != "nic" {
				add(Error, subject, "%s%s names a device that is not a NIC of this instance", legKey, nics[0])
				return "", ""
			}
			a := staticAddress(dev)
			if a == "" {
				add(Error, subject, "the %s leg (%s) has no static ipv4.address: the join needs a fixed address to render", purpose, nics[0])
			}
			return nics[0], a
		default:
			add(Error, subject, "%d NICs are marked %s (%s): the join cannot tell which address to use", len(nics), purpose, strings.Join(nics, ", "))
			return "", ""
		}
	}
	var lg legs
	if needServes {
		_, lg.serves = pick("serves", "that is the address the dns records answer with", true)
	}
	_, lg.mesh = pick("mesh", "that is the address the mesh view answers with and the source the apps' ACLs admit", needMesh)
	publicNIC, public := pick("public", "that is where the host's proxy devices carry traffic from the internet to", false)
	lg.public = public
	if publicNIC != "" && public != "" {
		publicLegFindings(subject, devs, publicNIC, public, add)
	}
	seen := map[string]bool{}
	for _, a := range []string{lg.mesh, lg.public} {
		if a != "" && !seen[a] {
			seen[a] = true
			lg.sources = append(lg.sources, a)
		}
	}
	sort.Strings(lg.sources)
	// With a mesh helper a missing mesh leg is already an error above; this one is for a site that has neither leg to give the ACLs an address.
	if len(lg.sources) == 0 && !needMesh {
		add(Error, subject, "no NIC is stamped %s<nic>: mesh or public: the apps' ACLs admit the address the ingress helper reaches them from, and that has to be one of those legs", legKey)
	}
	return lg
}

// publicLegFindings checks that the public leg is not just a word: that a proxy device on the host connects to its address, and with nat, so the ingress
// helper sees the client's address and not the relay's (measured on Incus 7.5.1, docs/public-exposure.md). Warnings: the devices may be applied later.
func publicLegFindings(subject string, devs map[string]map[string]string, nic, addr string, add func(Severity, string, string, ...any)) {
	carrying, natted := 0, 0
	for _, dev := range devs {
		if dev["type"] != "proxy" || dev["bind"] == "container" || dev["bind"] == "instance" || connectHost(dev["connect"]) != addr {
			continue
		}
		carrying++
		switch strings.ToLower(strings.TrimSpace(dev["nat"])) {
		case "true", "1", "yes", "on":
			natted++
		}
	}
	switch {
	case carrying == 0:
		add(Warning, subject, "the public leg (%s, %s) has no proxy device on the host that connects to it, so nothing carries the host's public address to this helper (docs/public-exposure.md)", nic, addr)
	case natted < carrying:
		add(Warning, subject, "%d of the %d proxy device(s) that carry traffic to the public leg (%s, %s) do not have nat on, so the helper sees the relay, not the client, as the source address (docs/public-exposure.md)", carrying-natted, carrying, nic, addr)
	}
}

// connectHost is the host of a proxy device's connect value (proto:host:port, an IPv6 host in brackets), or "" when it does not read.
func connectHost(connect string) string {
	_, rest, ok := strings.Cut(strings.TrimSpace(connect), ":")
	if !ok {
		return ""
	}
	i := strings.LastIndex(rest, ":")
	if i < 0 {
		return ""
	}
	return strings.Trim(rest[:i], "[]")
}

// resolvePublish joins one publish to its instance, its offer and the NIC the offer listens on.
func resolvePublish(p resolve.Resource, instances map[key]resolve.Resource, offers map[key]map[string]Offer, profiles map[key]resolve.Resource,
	networks map[string]resolve.Resource, add func(Severity, string, string, ...any)) (Published, bool) {
	subject := "publish/" + p.Name
	k := key{projectOf(p), p.Instance}
	inst, ok := instances[k]
	if !ok {
		add(Error, subject, "names instance %q (project %s), which no file given declares", p.Instance, k.project)
		return Published{}, false
	}
	have := offers[k]
	off, ok := have[p.Offer]
	if !ok {
		if len(have) == 0 {
			add(Error, subject, "instance %s makes no offer (no %s<offer>.port and .proto in its config)", inst.Name, offerKey)
		} else {
			names := make([]string, 0, len(have))
			for n := range have {
				names = append(names, n)
			}
			sort.Strings(names)
			add(Error, subject, "instance %s makes no offer %q (it makes: %s)", inst.Name, p.Offer, strings.Join(names, ", "))
		}
		return Published{}, false
	}
	if off.Proto != protoHTTP {
		add(Error, subject, "offer %s/%s speaks %q: only http can be published (a proxy route and a sign-on rule are for web traffic)", inst.Name, off.Name, off.Proto)
		return Published{}, false
	}

	devs := effectiveDevices(inst, profiles)
	nic := off.Listen
	if nic == "" {
		var nics []string
		for n, d := range devs {
			if d["type"] == "nic" {
				nics = append(nics, n)
			}
		}
		sort.Strings(nics)
		switch len(nics) {
		case 0:
			add(Error, subject, "instance %s has no NIC the join can see (a profile that is not in the files given cannot be read)", inst.Name)
			return Published{}, false
		case 1:
			nic = nics[0]
		default:
			add(Error, subject, "instance %s has %d NICs (%s): name the one the offer listens on with %s%s.listen", inst.Name, len(nics), strings.Join(nics, ", "), offerKey, off.Name)
			return Published{}, false
		}
	}
	dev, ok := devs[nic]
	if !ok || dev["type"] != "nic" {
		add(Error, subject, "offer %s/%s listens on %q, which is not a NIC of the instance", inst.Name, off.Name, nic)
		return Published{}, false
	}
	pub := Published{Project: k.project, Instance: inst.Name, Offer: off, Allow: p.Allow}
	pub.Address = staticAddress(dev)
	if pub.Address == "" {
		add(Error, subject, "NIC %s of %s has no static ipv4.address: the proxy reaches an app by address (names do not resolve across bridges) and the ACL names it, so it must not take one from DHCP", nic, inst.Name)
		return Published{}, false
	}
	pub.Network = dev["network"]
	if pub.Network == "" {
		add(Error, subject, "NIC %s of %s is not on a managed network (it is bridged to %q): no ACL can restrict who reaches it", nic, inst.Name, dev["parent"])
		return Published{}, false
	}
	nw, ok := networks[pub.Network]
	if !ok {
		add(Warning, subject, "network %s is not declared in the files given, so the ACL that should restrict it cannot be shown", pub.Network)
		return pub, true
	}
	var acls []string
	for _, a := range strings.Split(nw.Config["security.acls"], ",") {
		if a = strings.TrimSpace(a); a != "" {
			acls = append(acls, a)
		}
	}
	switch len(acls) {
	case 0:
		add(Error, subject, "network %s has no ACL attached (security.acls): any other bridge could reach %s", pub.Network, inst.Name)
		return Published{}, false
	case 1:
		pub.ACL = acls[0]
	default:
		add(Error, subject, "network %s has %d ACLs attached (%s): the join does not choose which one carries the rule", pub.Network, len(acls), strings.Join(acls, ", "))
		return Published{}, false
	}
	return pub, true
}

// matcherName is the Caddy matcher name for a host label: letters and digits, everything else an underscore. A label is lower case letters, digits and '-',
// and '-' is the only character mapped, so two different labels never share a matcher.
func matcherName(label string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, label)
}

// sortFindings puts errors first, then orders by subject and message, so the same input always prints the same report.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity == Error
		}
		if fs[i].Subject != fs[j].Subject {
			return fs[i].Subject < fs[j].Subject
		}
		return fs[i].Message < fs[j].Message
	})
}
