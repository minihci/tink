package main

import (
	"context"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/join"
	"github.com/minihci/tink/internal/observe"
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
		Reconcile: func(context.Context) []observe.Result {
			server, err := incusapi.Connect(socket)
			if err != nil {
				out := make([]observe.Result, 0, len(sites))
				for _, site := range sites {
					out = append(out, unreachable("dns", site, err))
				}
				return out
			}
			return dnsResults(server, sites)
		},
	}
}

func dnsResults(server incus.InstanceServer, sites []string) []observe.Result {
	files := join.IncusFiles{Server: server}
	out := make([]observe.Result, 0, len(sites))
	for _, site := range sites {
		out = append(out, join.ReconcileDNS(server, files, site, join.ApplyOptions{}))
	}
	return out
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
		Reconcile: func(context.Context) []observe.Result {
			server, err := incusapi.Connect(socket)
			if err != nil {
				out := make([]observe.Result, 0, len(sites))
				for _, site := range sites {
					out = append(out, unreachable("derived", site, err))
				}
				return out
			}
			return derivedResults(server, sites)
		},
	}
}

func derivedResults(server incus.InstanceServer, sites []string) []observe.Result {
	files := join.IncusFiles{Server: server}
	restarter := join.IncusRestarter{Server: server}
	var out []observe.Result
	for _, site := range sites {
		out = append(out, join.ReconcileDerived(server, files, restarter, site)...)
	}
	return out
}

// unreachable is what a pass reports for a site when it could not even connect to the server: the same state the reconcilers give a server that cannot be read,
// with the cause for the log.
func unreachable(what, site string, err error) observe.Result {
	return observe.Result{What: what, Site: site, State: observe.Failing, Reason: "the server could not be reached", Err: err}
}
