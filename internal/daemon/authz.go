package daemon

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/authz"
	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/resolve"
)

// The server config keys the narrow identity is installed through. The scriptlet is one server-wide key and the route is the class `tls`: unrestricted
// TLS clients, the daemon's certificate among them. Every other client of that class is allowed by the generated script as Incus would allow it, so the
// route changes nothing for anyone but the identities in the table (internal/authz).
const (
	AuthzScriptletKey = "authorization.scriptlet"
	AuthzRouteKey     = "authorization.client.tls"
	authzRoute        = "scriptlet"
)

// AuthzOptions say which certificate is narrowed and what the daemon it belongs to does, so that the rules name what it touches.
type AuthzOptions struct {
	Server incus.InstanceServer
	// Identity is the daemon's certificate by its name in the host's trust store (default helper.TrustName).
	Identity string
	// Status is the volume the daemon publishes its status on, or nil.
	Status *authz.Volume
	// Reconcile is whether the daemon reconciles the dns records (--reconcile-dns).
	Reconcile bool
	// Derived is whether it keeps the stamped derived files (--reconcile-derived).
	Derived bool

	// scope reads the server for what the daemon copies and where the dns helpers are; nil is the real read. A test seam.
	scope func(incus.InstanceServer, AuthzOptions) (authz.Scope, []string, error)
}

// AuthzPlan is what installing the narrow identity would do, and whether it may.
type AuthzPlan struct {
	Identity    string
	Fingerprint string
	Rules       []authz.Rule
	Script      string
	// Notes are things worth saying that do not stop it: a policy that could not be read, so its volume is not covered.
	Notes []string

	// What the server has now.
	CurrentScriptlet string
	CurrentRoute     string
	// Refusal, when set, is why Apply would not write: something that is not tink's is in the way. Nothing is guessed.
	Refusal string
	// UpToDate says the server already has exactly this script and this route.
	UpToDate bool
}

// PlanAuthz works out the scriptlet for the daemon's certificate from what the server has now, and says what installing it would change. It writes nothing.
func PlanAuthz(o AuthzOptions) (AuthzPlan, error) {
	name := o.Identity
	if name == "" {
		name = helper.TrustName
	}
	p := AuthzPlan{Identity: name}

	certs, err := o.Server.GetCertificates()
	if err != nil {
		return p, fmt.Errorf("reading the trust store: %w", err)
	}
	var cert *api.Certificate
	for i := range certs {
		if certs[i].Name == name {
			cert = &certs[i]
			break
		}
	}
	if cert == nil {
		return p, fmt.Errorf("no certificate named %q is trusted by this server, so there is nothing to narrow (`tink helper install` enrols the helper's; --identity names another)", name)
	}
	if cert.Restricted {
		return p, fmt.Errorf("the certificate %q is restricted, so Incus judges it by authorization.client.tls-restricted and not by the class this routes (tls): make it unrestricted, or route that class", name)
	}
	p.Fingerprint = cert.Fingerprint

	scope := scopeFromServer
	if o.scope != nil {
		scope = o.scope
	}
	sc, notes, err := scope(o.Server, o)
	if err != nil {
		return p, err
	}
	p.Notes = notes
	p.Rules = authz.DaemonRules(sc)
	p.Script, err = authz.Generate([]authz.Identity{{Name: name, Fingerprint: cert.Fingerprint, Rules: p.Rules}}, authz.Incus75)
	if err != nil {
		return p, err
	}

	srv, _, err := o.Server.GetServer()
	if err != nil {
		return p, fmt.Errorf("reading the server config: %w", err)
	}
	p.CurrentScriptlet, p.CurrentRoute = srv.Config[AuthzScriptletKey], srv.Config[AuthzRouteKey]
	switch {
	case p.CurrentScriptlet != "" && !authz.IsOurs(p.CurrentScriptlet):
		p.Refusal = fmt.Sprintf("%s holds a scriptlet tink did not generate (it does not start with tink's marker), so it is left alone; remove it yourself, or fold the daemon's rules into it", AuthzScriptletKey)
	case p.CurrentRoute != "" && p.CurrentRoute != authzRoute:
		p.Refusal = fmt.Sprintf("%s is %q: someone chose how unrestricted TLS clients are judged, and tink does not change that", AuthzRouteKey, p.CurrentRoute)
	}
	p.UpToDate = p.Refusal == "" && p.CurrentScriptlet == p.Script && p.CurrentRoute == authzRoute
	return p, nil
}

// ApplyAuthz installs the plan: the script and the route, in one write, so the route never names a script that is not there. A server that already has
// exactly this changes nothing. It returns the plan it acted on.
func ApplyAuthz(o AuthzOptions) (AuthzPlan, error) {
	p, err := PlanAuthz(o)
	if err != nil {
		return p, err
	}
	if p.Refusal != "" {
		return p, fmt.Errorf("not installed: %s", p.Refusal)
	}
	if p.UpToDate {
		return p, nil
	}
	if err := incusapi.MergeServerConfig(o.Server, map[string]string{AuthzScriptletKey: p.Script, AuthzRouteKey: authzRoute}, nil); err != nil {
		return p, err
	}
	return p, nil
}

// Narrowed says the server routes unrestricted TLS clients through a script tink generated, whether or not it is the one PlanAuthz would write now.
func (p AuthzPlan) Narrowed() bool {
	return authz.IsOurs(p.CurrentScriptlet) && p.CurrentRoute == authzRoute
}

// HelperAuthz is the narrowing as a stack declares it (`authz: narrow` on a helper, resolve.HelperAuthz): the same plan and apply as `tink daemon authz`, with
// the daemon's choices read from the helper on the server instead of given as flags.
type HelperAuthz struct{}

var _ resolve.HelperAuthz = HelperAuthz{}

// optionsFor says what the daemon h touches. Its status volume is the one its instance points at, in its own project; it reconciles the dns records when
// the entrypoint it was installed with, or has since been given, says --reconcile-dns.
func (HelperAuthz) optionsFor(server incus.InstanceServer, h helper.Found) (AuthzOptions, error) {
	entry := strings.Fields(h.Config["oci.entrypoint"])
	o := AuthzOptions{Server: server, Reconcile: slices.Contains(entry, "--reconcile-dns"), Derived: slices.Contains(entry, "--reconcile-derived")}
	if h.StatusVolume != "" {
		pool, name, err := helper.ParseStatusVolume(h.StatusVolume)
		if err != nil {
			return o, fmt.Errorf("the helper's status volume: %w", err)
		}
		o.Status = &authz.Volume{Project: h.Project, Pool: pool, Name: name}
	}
	return o, nil
}

// PlanAuthz reads the server and says how the certificate of h stands.
func (a HelperAuthz) PlanAuthz(server incus.InstanceServer, h helper.Found) (resolve.AuthzState, error) {
	o, err := a.optionsFor(server, h)
	if err != nil {
		return resolve.AuthzState{}, err
	}
	p, err := PlanAuthz(o)
	if err != nil {
		return resolve.AuthzState{}, err
	}
	return resolve.AuthzState{Narrowed: p.Narrowed(), UpToDate: p.UpToDate, Rules: len(p.Rules), Refusal: p.Refusal}, nil
}

// ApplyAuthz installs the script and the route for the certificate of h.
func (a HelperAuthz) ApplyAuthz(server incus.InstanceServer, h helper.Found) error {
	o, err := a.optionsFor(server, h)
	if err != nil {
		return err
	}
	_, err = ApplyAuthz(o)
	return err
}

// RemoveAuthz takes the narrow identity away: the route and the script, in one write. It only removes a script tink generated; a route to a script that is
// not tink's is left, with an error that says so. It reports whether anything was removed.
func RemoveAuthz(server incus.InstanceServer) (bool, error) {
	srv, _, err := server.GetServer()
	if err != nil {
		return false, fmt.Errorf("reading the server config: %w", err)
	}
	script, route := srv.Config[AuthzScriptletKey], srv.Config[AuthzRouteKey]
	if script != "" && !authz.IsOurs(script) {
		return false, fmt.Errorf("%s holds a scriptlet tink did not generate, so nothing is removed", AuthzScriptletKey)
	}
	if script == "" && route != authzRoute {
		return false, nil
	}
	if script == "" && route == authzRoute {
		// a route to nothing: unset the route (it is the one thing tink put there), the script is already gone
		return true, incusapi.MergeServerConfig(server, nil, []string{AuthzRouteKey})
	}
	unset := []string{AuthzScriptletKey}
	if route == authzRoute {
		unset = append(unset, AuthzRouteKey)
	}
	return true, incusapi.MergeServerConfig(server, nil, unset)
}

// scopeFromServer reads what the daemon touches: the volumes that carry a copy policy and the pools their restore points land in, and the dns helpers.
// A policy that cannot be read is not covered; it is said in the notes, because a copy that is refused later is a failure nobody asked for.
func scopeFromServer(server incus.InstanceServer, o AuthzOptions) (authz.Scope, []string, error) {
	sc := authz.Scope{Status: o.Status, Reconcile: o.Reconcile, Derived: o.Derived}
	var notes []string

	items, problems, err := backuprun.Discover(backuprun.ServerEngine{Server: server})
	if err != nil {
		return sc, nil, fmt.Errorf("reading the volumes' copy policies: %w", err)
	}
	sc.Copies = copiesOf(items)
	for label, perr := range problems {
		notes = append(notes, fmt.Sprintf("%s: its copy policy cannot be read, so it is not covered (%v)", label, perr))
	}
	sort.Strings(notes)

	if o.Reconcile || o.Derived {
		insts, err := server.GetInstancesAllProjects(api.InstanceTypeAny)
		if err != nil {
			return sc, nil, fmt.Errorf("listing the instances for the dns helpers and the derived files: %w", err)
		}
		sc, notes = instanceScope(sc, notes, insts, o)
	}
	return sc, notes, nil
}

// copiesOf turns the volumes with a copy policy into what the daemon is asked for on this server: the volume itself, and the pools of this server that its
// restore points land in. A copy to another server's pool (a remote target) creates nothing here.
func copiesOf(items []backuprun.Item) []authz.Copy {
	var out []authz.Copy
	for _, it := range items {
		project, pool := it.Volume.Project, it.Volume.Pool
		if project == "" {
			project = api.ProjectDefaultName
		}
		if pool == "" {
			pool = "default"
		}
		seen := map[string]bool{}
		var targets []string
		for _, c := range it.Copies {
			if c.Target.Remote == "" && c.Target.Pool != "" && !seen[c.Target.Pool] {
				seen[c.Target.Pool] = true
				targets = append(targets, c.Target.Pool)
			}
		}
		sort.Strings(targets)
		out = append(out, authz.Copy{Source: authz.Volume{Project: project, Pool: pool, Name: it.Volume.Name}, TargetPools: targets})
	}
	return out
}

// derivedStampsOf decodes the derived-file stamps in an instance's config: the files it holds, whether any says restart, and the keys that could not be read.
func derivedStampsOf(project, name string, cfg map[string]string) (files []resolve.DerivedFile, restart bool, bad []string) {
	for k, v := range cfg {
		if !strings.HasPrefix(k, resolve.DeriveStampPrefix) {
			continue
		}
		f, err := resolve.DecodeDeriveStamp(project, name, k, v)
		if err != nil {
			bad = append(bad, k)
			continue
		}
		files = append(files, f)
		restart = restart || f.Restart
	}
	sort.Strings(bad)
	return files, restart, bad
}

func sortInstances(in []authz.Instance) {
	sort.Slice(in, func(i, j int) bool {
		if in[i].Project != in[j].Project {
			return in[i].Project < in[j].Project
		}
		return in[i].Name < in[j].Name
	})
}

// instanceScope adds to sc the instances the daemon writes files on and restarts: the dns helpers for --reconcile-dns, and for --reconcile-derived every
// instance that carries the stamp of a derived file (and, of those, the ones whose stamp says restart). A stamp that cannot be read does not cover its
// instance, and is said in the notes.
func instanceScope(sc authz.Scope, notes []string, insts []api.Instance, o AuthzOptions) (authz.Scope, []string) {
	for _, in := range insts {
		at := authz.Instance{Project: in.Project, Name: in.Name}
		if o.Reconcile && strings.TrimSpace(in.Config[resolve.RoleKey]) == "dns" {
			sc.DNS = append(sc.DNS, at)
		}
		if o.Derived {
			files, restart, bad := derivedStampsOf(in.Project, in.Name, in.Config)
			if len(files) > 0 {
				sc.Files = append(sc.Files, at)
			}
			if restart {
				sc.Restart = append(sc.Restart, at)
			}
			for _, key := range bad {
				notes = append(notes, fmt.Sprintf("%s/%s: the stamp %s cannot be read, so it does not cover that instance", in.Project, in.Name, key))
			}
		}
	}
	sortInstances(sc.DNS)
	sortInstances(sc.Files)
	sortInstances(sc.Restart)
	sort.Strings(notes)
	return sc, notes
}
