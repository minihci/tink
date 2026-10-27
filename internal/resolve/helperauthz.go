package resolve

import (
	"fmt"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/helper"
)

// AuthzNarrow is the one value of a helper's `authz:` field: the stack asks that the daemon's certificate be narrowed to what the daemon does.
//
//	kind: helper
//	name: helper
//	authz: narrow
//
// The mechanism is `tink daemon authz` (internal/daemon, docs/helper.md, "Narrowing the daemon's certificate"): an authorization scriptlet for that
// certificate alone and a route to it. This file is the declarative surface of it, and nothing else:
//
//   - plan SAYS how the certificate stands for every helper it adopts, declared or not: unrestricted, narrowed, or narrowed with rules that no longer
//     match the volumes and dns helpers on the server. These are notes and warnings, never an action or an exit status.
//   - a helper that says `authz: narrow` is narrowed by apply, after it is installed if it is not there yet, and its rules are rewritten when they
//     are out of date. A second plan then says no changes.
//   - removing the field removes nothing (a stack never deletes): plan says "narrowed, not declared", and `tink daemon authz remove` is the way back.
//
// The authorizer is one scriptlet and one route for the whole server, so two helpers asking in one stack are both BLOCKED. A second stack asking for a
// second helper cannot be told apart from the same helper re-enrolled with a new certificate, and is left to the identity being one name in the trust
// store (helper.TrustName): see docs/helper.md.
//
// The capability itself is not imported here (internal/daemon imports this package): it is a HelperAuthz, handed to planning in PlanOptions, as what the
// helper can read is. A stack that says `authz: narrow` when none was handed over is BLOCKED rather than skipped, because a plan that quietly left the
// certificate unrestricted would say the opposite of what it was asked.
const AuthzNarrow = "narrow"

// AuthzState is how a helper's certificate stands on the server.
type AuthzState struct {
	// Narrowed is whether the server routes unrestricted TLS clients through a script tink generated.
	Narrowed bool
	// UpToDate is whether that script is exactly the one the volumes and dns helpers on the server would produce now.
	UpToDate bool
	// Rules is how many rules the certificate would be given now.
	Rules int
	// Refusal, when set, is why tink would not write: something that is not its own is in the way (a scriptlet it did not generate, a route someone chose).
	Refusal string
}

// HelperAuthz is the narrowing of a helper's certificate, which planning and applying are handed: internal/daemon is the one that implements it.
type HelperAuthz interface {
	// PlanAuthz reads the server and says how h's certificate stands. It writes nothing.
	PlanAuthz(server incus.InstanceServer, h helper.Found) (AuthzState, error)
	// ApplyAuthz installs the script and the route for h's certificate, in one write.
	ApplyAuthz(server incus.InstanceServer, h helper.Found) error
}

// WithHelperAuthz hands planning (and, through it, applying) the narrowing of the daemon's certificate. Without it a helper that declares `authz: narrow`
// is BLOCKED, and one that does not is not asked about its certificate.
func (o PlanOptions) WithHelperAuthz(a HelperAuthz) PlanOptions {
	o.helperAuthz = a
	return o
}

// authzNarrowers are the labels of the helpers in resources that ask for the narrowing, in the order given.
func authzNarrowers(resources []Resource) []string {
	var out []string
	for _, r := range resources {
		if r.Kind == KindHelper && r.Authz == AuthzNarrow {
			project, name := helperTarget(r)
			out = append(out, project+"/"+name)
		}
	}
	return out
}

// planAuthz adds to the plan of the helper r what it says about its certificate. found is the helper on the server, nil when there is none yet.
func planAuthz(server incus.InstanceServer, r Resource, found *helper.Found, p PlannedResource, opts PlanOptions) PlannedResource {
	declared := r.Authz == AuthzNarrow
	block := func(reason string) PlannedResource {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{reason}}
	}
	if declared {
		project, name := helperTarget(r)
		label := project + "/" + name
		for _, other := range opts.narrowers {
			if other != label {
				return block(fmt.Sprintf("the helper %s also says authz: narrow, and the authorizer is one scriptlet for the whole server: keep it on one helper", other))
			}
		}
		if opts.helperAuthz == nil {
			return block("authz: narrow was asked of a tink that was not given the narrowing of the daemon's certificate (this is a tink bug: refusing to leave it unrestricted without saying so)")
		}
	}
	if opts.helperAuthz == nil {
		return p
	}
	if found == nil {
		if declared {
			p.Changes = append(p.Changes, "then narrow its certificate to what the daemon does (an authorization scriptlet for it alone; `tink daemon authz`)")
		}
		return p
	}

	st, err := opts.helperAuthz.PlanAuthz(server, *found)
	switch {
	case err != nil && declared:
		return block("cannot narrow the daemon's certificate: " + err.Error())
	case err != nil:
		p.Warnings = append(p.Warnings, "cannot tell whether the daemon's certificate is narrowed: "+err.Error())
		return p
	case st.Refusal != "" && declared:
		return block("cannot narrow the daemon's certificate: " + st.Refusal)
	case st.Refusal != "":
		p.Notes = append(p.Notes, "the daemon's certificate is not narrowed by tink: "+st.Refusal)
		return p
	}

	switch {
	case declared && st.Narrowed && st.UpToDate:
		p.Notes = append(p.Notes, fmt.Sprintf("the daemon's certificate is narrowed (%s)", rulesOf(st.Rules)))
	case declared && st.Narrowed:
		p.Action = ActionUpdate
		p.Changes = append(p.Changes, fmt.Sprintf("rewrite the authorization scriptlet for what the daemon touches now (%s)", rulesOf(st.Rules)))
	case declared:
		p.Action = ActionUpdate
		p.Changes = append(p.Changes, fmt.Sprintf("narrow the daemon's certificate: install an authorization scriptlet for it alone (%s) and route unrestricted TLS clients to it (`tink daemon authz`)", rulesOf(st.Rules)))
	case st.Narrowed:
		p.Notes = append(p.Notes, "the daemon's certificate is narrowed and the stack does not say so: authz: narrow on this helper keeps it that way (`tink daemon authz remove` undoes it)")
		if !st.UpToDate {
			p.Warnings = append(p.Warnings, "the daemon's certificate is narrowed by rules that no longer match what the daemon touches (its volumes, the dns helpers, its flags): `tink daemon authz apply` (or authz: narrow) rewrites them")
		}
	default:
		p.Notes = append(p.Notes, "the daemon's certificate is unrestricted: it has the reach of root on the host (authz: narrow on this helper narrows it)")
	}
	return p
}

func rulesOf(n int) string {
	if n == 1 {
		return "1 rule"
	}
	return fmt.Sprintf("%d rules", n)
}

// narrowHelper installs the narrowing for the helper r, which must be there.
func narrowHelper(server incus.InstanceServer, r Resource, az HelperAuthz) error {
	if az == nil {
		return fmt.Errorf("authz: narrow was asked of a tink that was not given the narrowing of the daemon's certificate (this is a tink bug)")
	}
	project, name := helperTarget(r)
	all, err := helper.Find(server)
	if err != nil {
		return err
	}
	for _, f := range all {
		if f.Project == project && f.Name == name {
			return az.ApplyAuthz(server, f)
		}
	}
	return fmt.Errorf("narrowing the certificate of %s: no helper by that name on the server", project+"/"+name)
}

// validateAuthz rejects a value of `authz:` that is not one of the known ones.
func validateAuthz(r Resource) error {
	switch r.Authz {
	case "", AuthzNarrow:
		return nil
	}
	return fmt.Errorf("resource %q: authz %q is not one tink knows (the only value is %s)", r.Name, r.Authz, AuthzNarrow)
}
