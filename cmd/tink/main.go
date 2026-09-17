// Command tink is the companion CLI for the Mini HCI / Tink homelab
// platform. See the repo README for what that means; this file is
// intentionally just argument wiring — every capability's real logic
// lives in its own internal package under internal/.
package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/backup"
	"github.com/minihci/tink/internal/bootstrap"
	"github.com/minihci/tink/internal/ingress"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tink",
		Short: "Tink is not Kubernetes",
		Long: `tink is the companion tool for the Mini HCI / Tink homelab platform:
opinionated, Incus-native primitives for running self-hosted projects,
made executable instead of just documented.`,
		SilenceUsage: true,
	}

	root.AddCommand(newApplyCmd())
	root.AddCommand(newIngressCmd())
	root.AddCommand(newMongoCmd())
	root.AddCommand(newVersionCmd())

	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print tink's build version",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), buildVersion())
			return nil
		},
	}
}

// buildVersion reads Go's own embedded VCS metadata (available whenever
// this binary was built with `go build` inside a git checkout) rather than
// relying on -ldflags injected by a release script that doesn't exist yet.
func buildVersion() string {
	version := "unknown"
	commit := "unknown"
	buildDate := "unknown"

	if info, ok := debug.ReadBuildInfo(); ok {
		version = info.Main.Version // already carries a +dirty suffix when vcs.modified is true
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				commit = s.Value
			case "vcs.time":
				buildDate = s.Value
			}
		}
	}

	return fmt.Sprintf("tink %s (commit %s, built %s, %s)", version, commit, buildDate, runtime.Version())
}

func newApplyCmd() *cobra.Command {
	var repoRoot, deployEnvPath string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Converge this host to its declared platform state (capability zero)",
		Long: `apply provisions and reconciles the platform's own infrastructure —
storage volumes, profiles, the ingress/authelia/incus-ui instances, and the
daemon's OIDC/authorization config — the same job
incus-host/scripts/deploy.sh does today, ported faithfully (including its
existing behavior of unconditionally recreating incus-ui/authelia/ingress
on every run, not just the first).

Use --dry-run to compute and report every action without touching the
daemon, the crontab, or any instance.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if deployEnvPath == "" {
				deployEnvPath = repoRoot + "/deploy.env"
			}
			cfg, err := bootstrap.LoadConfig(deployEnvPath)
			if err != nil {
				return err
			}
			result, err := bootstrap.Run(bootstrap.Options{
				RepoRoot: repoRoot,
				Config:   cfg,
				DryRun:   dryRun,
			})
			for _, action := range result.Actions {
				fmt.Fprintln(cmd.OutOrStdout(), action)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&repoRoot, "repo-root", ".", "path to the incus-host checkout")
	cmd.Flags().StringVar(&deployEnvPath, "deploy-env", "", "path to deploy.env (default: <repo-root>/deploy.env)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute and report every action without applying anything")
	return cmd
}

func newIngressCmd() *cobra.Command {
	ingressCmd := &cobra.Command{
		Use:   "ingress",
		Short: "Manage self-registered ingress routes",
	}

	ingressCmd.AddCommand(newIngressReconcileCmd())
	ingressCmd.AddCommand(newIngressStatusCmd())

	return ingressCmd
}

func newIngressReconcileCmd() *cobra.Command {
	opts := ingress.DefaultOptions()
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Discover registered instances and converge ingress routes to match",
		Long: `reconcile is tink's port of incus-host/reconciler/reconcile.sh: it
discovers instances that opt in via user.ingress.{domain,port,enabled}
config and renders/applies the shared ingress instance's routes.

Use --dry-run to compute and report what would change without writing
anything or reloading Caddy -- this is how it's meant to be run alongside
the live bash version before its cron entry actually gets moved over.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ingress.Reconcile(opts)
			if err != nil {
				return err
			}
			printIngressResult(cmd, result, opts.DryRun)
			return nil
		},
	}
	addIngressFlags(cmd, &opts)
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "compute and report what would change without applying it")
	return cmd
}

func newIngressStatusCmd() *cobra.Command {
	opts := ingress.DefaultOptions()
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show what's currently registered, without changing anything",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := ingress.Status(opts)
			if err != nil {
				return err
			}
			printIngressResult(cmd, result, true)
			return nil
		},
	}
	addIngressFlags(cmd, &opts)
	return cmd
}

func addIngressFlags(cmd *cobra.Command, opts *ingress.Options) {
	cmd.Flags().StringVar(&opts.Socket, "socket", opts.Socket, "Incus daemon unix socket path")
	cmd.Flags().StringVar(&opts.RoutesDir, "routes-dir", opts.RoutesDir, "generated ingress routes directory")
	cmd.Flags().StringVar(&opts.IngressInstance, "ingress-instance", opts.IngressInstance, "name of the ingress instance to reload")
}

func printIngressResult(cmd *cobra.Command, result *ingress.Result, dryRun bool) {
	out := cmd.OutOrStdout()
	for _, w := range result.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARN: %s\n", w)
	}

	fmt.Fprintf(out, "%d instance(s) registered:\n", len(result.Registrations))
	for _, r := range result.Registrations {
		fmt.Fprintf(out, "  %s -> https://%s (proxying to %s:%s)\n", r.Name, r.Domain, r.Address, r.Port)
	}

	if result.Diff.Empty() {
		fmt.Fprintln(out, "no changes")
		return
	}

	verb := "would apply"
	if result.Applied {
		verb = "applied"
	}
	fmt.Fprintf(out, "%s: +%d -%d ~%d\n", verb, len(result.Diff.Added), len(result.Diff.Removed), len(result.Diff.Changed))
	for _, f := range result.Diff.Added {
		fmt.Fprintf(out, "  add %s\n", f)
	}
	for _, f := range result.Diff.Removed {
		fmt.Fprintf(out, "  remove %s\n", f)
	}
	for _, f := range result.Diff.Changed {
		fmt.Fprintf(out, "  change %s\n", f)
	}
}

func newMongoCmd() *cobra.Command {
	mongoCmd := &cobra.Command{
		Use:   "mongo",
		Short: "Mongo backup and recovery",
	}

	mongoCmd.AddCommand(&cobra.Command{
		Use:   "snapshot",
		Short: "Trigger a mongo backup",
		Long:  `snapshot is not yet designed — see the repo README for the open discussion.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return backup.Snapshot()
		},
	})

	return mongoCmd
}
