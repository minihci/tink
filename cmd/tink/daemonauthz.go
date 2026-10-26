package main

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/authz"
	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/incusapi"
)

func newDaemonAuthzCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "authz",
		Short: "Narrow the daemon's certificate to what it does (an authorization scriptlet for it alone)",
		Long: `By default Incus trusts an unrestricted client certificate with the whole server, and the daemon's is one: it has the reach of root on the host
(docs/helper.md, "Not confined"). authz narrows it without touching anyone else. It generates an authorization scriptlet that gives the daemon's
certificate exactly the rules in internal/authz and judges every other client as Incus's own routing does, and routes the class of unrestricted TLS
clients to it (authorization.client.tls: scriptlet). The daemon's certificate stays as it is: nothing is enrolled again.

  tink daemon authz plan      say what would be installed and whether it may; write nothing
  tink daemon authz apply     install it: the script and the route, in one write
  tink daemon authz remove    take it away again

The local Unix socket never reaches the scriptlet, so the way back is always open from the host:
  incus config unset authorization.client.tls

It only ever replaces a scriptlet it generated (one that starts with tink's marker), and it will not change a route someone chose. It does not follow the
volumes and dns helpers that appear later: run plan or apply again when they do. Name the daemon's choices with the same flags it runs with.`,
	}
	cmd.AddCommand(newDaemonAuthzPlanCmd(), newDaemonAuthzApplyCmd(), newDaemonAuthzRemoveCmd())
	return cmd
}

type authzFlags struct {
	socket, identity, statusVolume, statusProject string
	reconcile                                     bool
}

func (f *authzFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&f.identity, "identity", "", "the daemon's certificate, by its name in the trust store (default: the one `tink helper install` enrols)")
	cmd.Flags().StringVar(&f.statusVolume, "status-volume", "", "the POOL/VOLUME the daemon publishes its status on (as for tink daemon run)")
	cmd.Flags().StringVar(&f.statusProject, "status-project", "", "the project of --status-volume (default: the connection's own)")
	cmd.Flags().BoolVar(&f.reconcile, "reconcile-dns", false, "the daemon runs with --reconcile-dns: it also reads the instances, profiles and networks, watches events, and writes files on the dns helpers")
}

func (f *authzFlags) options(socket string) (daemon.AuthzOptions, error) {
	server, err := incusapi.Connect(socket)
	if err != nil {
		return daemon.AuthzOptions{}, fmt.Errorf("connecting to incus: %w", err)
	}
	o := daemon.AuthzOptions{Server: server, Identity: f.identity, Reconcile: f.reconcile}
	if f.statusVolume != "" {
		pool, name, ok := strings.Cut(f.statusVolume, "/")
		if !ok || pool == "" || name == "" {
			return o, fmt.Errorf("--status-volume wants POOL/VOLUME, not %q", f.statusVolume)
		}
		project := f.statusProject
		if project == "" {
			project = "default"
		}
		o.Status = &authz.Volume{Project: project, Pool: pool, Name: name}
	}
	return o, nil
}

func newDaemonAuthzPlanCmd() *cobra.Command {
	var f authzFlags
	var script, rules bool
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Say what authz apply would install and whether it may; write nothing",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := f.options(f.socket)
			if err != nil {
				return err
			}
			p, err := daemon.PlanAuthz(o)
			if err != nil {
				return err
			}
			printAuthzPlan(cmd.OutOrStdout(), p, script, rules)
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&script, "script", false, "also print the generated scriptlet")
	cmd.Flags().BoolVar(&rules, "rules", false, "also list the rules")
	return cmd
}

func newDaemonAuthzApplyCmd() *cobra.Command {
	var f authzFlags
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Install the narrow identity: the scriptlet and the route, in one write",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			o, err := f.options(f.socket)
			if err != nil {
				return err
			}
			p, err := daemon.ApplyAuthz(o)
			if err != nil {
				printAuthzPlan(cmd.OutOrStdout(), p, false, false)
				return err
			}
			if p.UpToDate {
				fmt.Fprintln(cmd.OutOrStdout(), "no changes: the server already has this scriptlet and this route")
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed: %s (%d rules) and %s: scriptlet\nto undo it from the host: incus config unset %s\n",
				daemon.AuthzScriptletKey, len(p.Rules), daemon.AuthzRouteKey, daemon.AuthzRouteKey)
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func newDaemonAuthzRemoveCmd() *cobra.Command {
	var socket string
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Take the narrow identity away again",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			removed, err := daemon.RemoveAuthz(server)
			if err != nil {
				return err
			}
			if !removed {
				fmt.Fprintln(cmd.OutOrStdout(), "no changes: there is no scriptlet of tink's here")
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed: the daemon's certificate is judged as any unrestricted client is")
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	return cmd
}

func printAuthzPlan(out io.Writer, p daemon.AuthzPlan, script, rules bool) {
	fmt.Fprintf(out, "identity: %s", p.Identity)
	if p.Fingerprint != "" {
		fmt.Fprintf(out, " (fingerprint %s…)", p.Fingerprint[:12])
	}
	fmt.Fprintln(out)
	cur := p.CurrentRoute
	if cur == "" {
		cur = "(unset: Incus's own routing)"
	}
	fmt.Fprintf(out, "%s: %s -> scriptlet\n", daemon.AuthzRouteKey, cur)
	switch {
	case p.CurrentScriptlet == "":
		fmt.Fprintf(out, "%s: none -> generated by tink (%d rules)\n", daemon.AuthzScriptletKey, len(p.Rules))
	case !strings.HasPrefix(p.CurrentScriptlet, authz.Marker):
		fmt.Fprintf(out, "%s: someone else's\n", daemon.AuthzScriptletKey)
	case p.CurrentScriptlet == p.Script:
		fmt.Fprintf(out, "%s: tink's, already this one (%d rules)\n", daemon.AuthzScriptletKey, len(p.Rules))
	default:
		fmt.Fprintf(out, "%s: tink's -> rewritten (%d rules)\n", daemon.AuthzScriptletKey, len(p.Rules))
	}
	for _, n := range p.Notes {
		fmt.Fprintf(out, "note: %s\n", n)
	}
	switch {
	case p.Refusal != "":
		fmt.Fprintf(out, "REFUSED: %s\n", p.Refusal)
	case p.UpToDate:
		fmt.Fprintln(out, "up to date: apply would change nothing")
	default:
		fmt.Fprintln(out, "apply would install it")
	}
	if rules {
		fmt.Fprintln(out, "rules:")
		lines := make([]string, 0, len(p.Rules))
		for _, r := range p.Rules {
			kind := "exact "
			if r.Prefix {
				kind = "prefix"
			}
			lines = append(lines, fmt.Sprintf("  %s %-24s %s", kind, r.Entitlement, r.Object))
		}
		sort.Strings(lines)
		fmt.Fprintln(out, strings.Join(lines, "\n"))
	}
	if script {
		fmt.Fprintf(out, "--- scriptlet\n%s", p.Script)
	}
}
