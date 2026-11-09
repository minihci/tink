package join

import (
	"errors"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/observe"
	"github.com/minihci/tink/internal/resolve"
)

// ReconcileDNS reads one site from the server and makes its dns helper's generated records equal to the rendering: `tink join apply dns --from-server`
// as a function, for the daemon. It is level-triggered: it reads everything it needs, compares, and writes only when the file differs, so running it
// again, or running it on an event that changed nothing, writes nothing. It never adopts a file tink did not write and never empties the records
// (opts.Adopt and opts.AllowEmpty are the operator's decisions, taken with the command and not here); a refusal is reported as blocked.
func ReconcileDNS(server incus.InstanceServer, f Files, site string, opts ApplyOptions) observe.Result {
	out := observe.Result{What: "dns", Site: site}
	stacks, err := FromServer(server, site)
	var se *StampError
	if errors.As(err, &se) {
		out.State, out.Reason, out.Err = observe.Blocked, "a publish stamp cannot be read", err
		out.Detail = append(out.Detail, se.Findings()...)
		return out
	}
	if err != nil {
		out.State, out.Reason, out.Err = observe.Failing, "the server could not be read", err
		return out
	}
	res := Build(stacks)
	out.Detail = append(out.Detail, res.Findings...)
	if !hasDNSRole(stacks) {
		out.State = observe.Skipped
		return out
	}
	if res.Errors() > 0 {
		out.State, out.Reason, out.Err = observe.Blocked, "the join has errors", errors.New("the join has errors")
		return out
	}
	rep, err := ApplyDNS(f, res, opts)
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		out.State, out.Reason, out.Err = observe.Blocked, "the apply was refused", err
	case err != nil:
		out.State, out.Reason, out.Err = observe.Failing, "the dns helper could not be read or written", err
	case rep.Written:
		out.State, out.Wrote = observe.Converged, true
		out.Detail = append(out.Detail, finding.Notef("the records were rewritten", "dns", "%s on %s: %d record(s) added, %d removed, read back", rep.Path, rep.Instance, len(rep.Added), len(rep.Removed)))
	default:
		out.State = observe.Converged
	}
	return out
}

// hasDNSRole says whether any instance of the site states the dns role. A site with none has no records to keep, which is a different thing from a site whose
// dns helper cannot be found (two of them, say), and that is a join error and is blocked.
func hasDNSRole(stacks []Stack) bool {
	for _, st := range stacks {
		for _, r := range st.Resources {
			if r.Kind == resolve.KindInstance && strings.TrimSpace(r.Config[resolve.RoleKey]) == "dns" {
				return true
			}
		}
	}
	return false
}
