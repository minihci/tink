package main

import (
	"context"
	"fmt"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/join"
)

// dnsReconciler is the daemon's reconciler for the dns helper's generated records: it is `tink join apply dns --from-server` run whenever the server's
// instances, profiles or networks change, for each site named (the unnamed site when none is). It never adopts a file tink did not write and never empties
// the records; those are decisions the operator takes with the command.
func dnsReconciler(socket string, sites []string, resync time.Duration) *daemon.Reconciler {
	if len(sites) == 0 {
		sites = []string{""}
	}
	return &daemon.Reconciler{
		Resync: resync,
		Watch: func(ctx context.Context, ready func(), handle func(string)) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return err
			}
			return incusapi.WatchLifecycle(ctx, server, ready, handle)
		},
		Reconcile: func(context.Context) []daemon.ReconcileResult {
			server, err := incusapi.Connect(socket)
			if err != nil {
				out := make([]daemon.ReconcileResult, 0, len(sites))
				for _, site := range sites {
					out = append(out, resultOf(join.DNSOutcome{Site: site, State: join.DNSFailing, Reason: "the server could not be reached", Err: err}))
				}
				return out
			}
			return dnsResults(server, sites)
		},
	}
}

func dnsResults(server incus.InstanceServer, sites []string) []daemon.ReconcileResult {
	files := join.IncusFiles{Server: server}
	out := make([]daemon.ReconcileResult, 0, len(sites))
	for _, site := range sites {
		out = append(out, resultOf(join.ReconcileDNS(server, files, site, join.ApplyOptions{})))
	}
	return out
}

// resultOf turns what a reconcile of one site found into the daemon's result: State and Reason are the fixed words the status document carries (a pass that
// rewrote the records and one that found them right are both converged; the first is also said in the log), and the findings and the error go to the log.
func resultOf(o join.DNSOutcome) daemon.ReconcileResult {
	r := daemon.ReconcileResult{What: "dns", Site: o.Site, State: o.State, Reason: o.Reason, Detail: append([]string(nil), o.Findings...)}
	switch o.State {
	case join.DNSChanged:
		r.State, r.Wrote = "converged", true
		r.Detail = append(r.Detail, fmt.Sprintf("%s on %s: %d record(s) added, %d removed, read back", o.Report.Path, o.Report.Instance, len(o.Report.Added), len(o.Report.Removed)))
	case join.DNSUnchanged:
		r.State = "converged"
	}
	if o.Err != nil {
		r.Detail = append(r.Detail, o.Err.Error())
	}
	return r
}

// derivedReconciler is the daemon's reconciler for every stamped derived file: `join.ReconcileDerived` run whenever the server's instances, profiles or
// networks change, for each site named (the unnamed site when none is). It never adopts a file tink did not write, never empties a region that has lines,
// and never creates a region file; those are decisions the operator takes with `plan apply`.
func derivedReconciler(socket string, sites []string, resync time.Duration) *daemon.Reconciler {
	if len(sites) == 0 {
		sites = []string{""}
	}
	return &daemon.Reconciler{
		Resync: resync,
		Watch: func(ctx context.Context, ready func(), handle func(string)) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return err
			}
			return incusapi.WatchLifecycle(ctx, server, ready, handle)
		},
		Reconcile: func(context.Context) []daemon.ReconcileResult {
			server, err := incusapi.Connect(socket)
			if err != nil {
				out := make([]daemon.ReconcileResult, 0, len(sites))
				for _, site := range sites {
					out = append(out, derivedResultOf(join.DerivedOutcome{Site: site, State: join.DerivedFailing, Reason: "the server could not be reached", Err: err}))
				}
				return out
			}
			return derivedResults(server, sites)
		},
	}
}

func derivedResults(server incus.InstanceServer, sites []string) []daemon.ReconcileResult {
	files := join.IncusFiles{Server: server}
	restarter := join.IncusRestarter{Server: server}
	var out []daemon.ReconcileResult
	for _, site := range sites {
		for _, o := range join.ReconcileDerived(server, files, restarter, site) {
			out = append(out, derivedResultOf(o))
		}
	}
	return out
}

// derivedResultOf turns what a reconcile of one derived file found into the daemon's result. What is "derived" for a result about the whole site and
// "derived/NAME" for a file (the file document's name, which is not a secret); State and Reason are the fixed words the status document carries.
func derivedResultOf(o join.DerivedOutcome) daemon.ReconcileResult {
	what := "derived"
	if o.Name != "" {
		what += "/" + o.Name
	}
	r := daemon.ReconcileResult{What: what, Site: o.Site, State: o.State, Reason: o.Reason, Wrote: o.Wrote, Detail: append(append([]string(nil), o.Findings...), o.Detail...)}
	switch o.State {
	case join.DerivedChanged:
		r.State = "converged"
	case join.DerivedUnchanged:
		r.State = "converged"
	}
	if o.Err != nil {
		r.Detail = append(r.Detail, o.Err.Error())
	}
	return r
}
