package join

import (
	"errors"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/resolve"
)

// What a reconcile of the dns records found and did. They are fixed words, because the daemon's status document carries them and holds no error text (errors
// from Incus can echo credentials; the detail stays in the log).
const (
	DNSChanged   = "changed"    // the records differed; they were rewritten and read back
	DNSUnchanged = "no changes" // the file already held the rendering; nothing was written
	DNSBlocked   = "blocked"    // the join or an apply refusal stopped it, on purpose; nothing was written
	DNSSkipped   = "skipped"    // the site has no dns helper, so there is nothing to keep
	DNSFailing   = "failing"    // the server or the helper could not be read or written
)

// DNSOutcome is the result of one reconcile of one site.
type DNSOutcome struct {
	Site   string
	State  string // one of the DNS* words
	Reason string // short and fixed: what to look for in the log; empty when State is changed, no changes or skipped
	Report ApplyReport
	// Findings and Err are for a log. They are not for the status document.
	Findings []string
	Err      error
}

// ReconcileDNS reads one site from the server and makes its dns helper's generated records equal to the rendering: `tink join apply dns --from-server`
// as a function, for the daemon. It is level-triggered: it reads everything it needs, compares, and writes only when the file differs, so running it
// again, or running it on an event that changed nothing, writes nothing. It never adopts a file tink did not write and never empties the records
// (opts.Adopt and opts.AllowEmpty are the operator's decisions, taken with the command and not here); a refusal is reported as blocked.
func ReconcileDNS(server incus.InstanceServer, f Files, site string, opts ApplyOptions) DNSOutcome {
	out := DNSOutcome{Site: site}
	stacks, err := FromServer(server, site)
	var se *StampError
	if errors.As(err, &se) {
		out.State, out.Reason, out.Err = DNSBlocked, "a publish stamp cannot be read", err
		for _, fi := range se.Findings() {
			out.Findings = append(out.Findings, fi.String())
		}
		return out
	}
	if err != nil {
		out.State, out.Reason, out.Err = DNSFailing, "the server could not be read", err
		return out
	}
	res := Build(stacks)
	for _, fi := range res.Findings {
		out.Findings = append(out.Findings, fi.String())
	}
	if !hasDNSRole(stacks) {
		out.State = DNSSkipped
		return out
	}
	if res.Errors() > 0 {
		out.State, out.Reason, out.Err = DNSBlocked, "the join has errors", errors.New("the join has errors")
		return out
	}
	rep, err := ApplyDNS(f, res, opts)
	out.Report = rep
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		out.State, out.Reason, out.Err = DNSBlocked, "the apply was refused", err
	case err != nil:
		out.State, out.Reason, out.Err = DNSFailing, "the dns helper could not be read or written", err
	case rep.Written:
		out.State = DNSChanged
	default:
		out.State = DNSUnchanged
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
