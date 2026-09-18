// Command tink is the companion CLI for the Mini HCI / Tink homelab
// platform. See the repo README for what that means; this file is
// intentionally just argument wiring — every capability's real logic
// lives in its own internal package under internal/.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/backup"
	"github.com/minihci/tink/internal/bootstrap"
	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/ingress"
	"github.com/minihci/tink/internal/run"
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

	root.AddCommand(newDeployCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newIngressCmd())
	root.AddCommand(newMongoCmd())
	root.AddCommand(newDaemonCmd())
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

func newDeployCmd() *cobra.Command {
	var repoRoot, deployEnvPath, socket string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Converge this host to its declared platform state (capability zero)",
		Long: `deploy provisions and reconciles the platform's own infrastructure —
storage volumes, profiles, the ingress/authelia/incus-ui instances, and the
daemon's OIDC/authorization config — the same job incus-host/scripts/deploy.sh
originally did, ported faithfully (including its existing behavior of
unconditionally recreating incus-ui/authelia/ingress on every run, not just
the first). Its config templates now live in this repo's own configs/
directory rather than a separate incus-host checkout -- see configs/README.md.

Profiles, storage volumes, and instance existence/deletion go through
Incus's own Go client; registries and instance launch still shell out to
the incus CLI -- remotes are a client-config concept with no daemon API to
call instead, and launch means resolving an OCI image from a named remote,
which isn't a single clean API call.

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
				Socket:   socket,
			})
			for _, action := range result.Actions {
				fmt.Fprintln(cmd.OutOrStdout(), action)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&repoRoot, "repo-root", "configs", "path to the config templates (this repo's own configs/ by default)")
	cmd.Flags().StringVar(&deployEnvPath, "deploy-env", "", "path to deploy.env (default: <repo-root>/deploy.env)")
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "compute and report every action without applying anything")
	return cmd
}

func newRunCmd() *cobra.Command {
	opts := run.DefaultOptions()

	cmd := &cobra.Command{
		Use:   "run [flags] IMAGE [CMD...]",
		Short: "Launch an instance from docker-run-shaped flags, translated onto Incus primitives",
		Long: `run translates a docker-run-shaped invocation onto real Incus
primitives -- launch the image, then set the config keys and add the
devices the given flags correspond to -- instead of the manual "mental
docker-run image+flags into incus launch plus a sequence of incus config
set/incus config device add calls" dance every tenant app on this
platform has been built with so far. See internal/run/DESIGN.md for the
full flag-mapping table and its deliberate non-goals -- this is not a
Docker CLI clone: incus's own ps/exec/logs/stop/rm are already just as
short as their Docker equivalents, and docker logs specifically has no
Incus equivalent to translate to at all.

Use --dry-run to compute and print the plan without launching anything
or touching the daemon, matching tink deploy's and tink ingress
reconcile's existing convention.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Image = args[0]
			opts.Cmd = args[1:]

			result, err := run.Run(opts)
			for _, action := range result.Actions {
				fmt.Fprintln(cmd.OutOrStdout(), action)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&opts.Socket, "socket", opts.Socket, "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Incus project to create the instance in (default: the daemon's own default project)")
	cmd.Flags().StringVar(&opts.Name, "name", "", "instance name (required)")
	cmd.Flags().StringArrayVarP(&opts.Env, "env", "e", nil, "set an environment variable (KEY=VALUE, repeatable)")
	cmd.Flags().StringArrayVarP(&opts.Publish, "publish", "p", nil, "publish a port via a proxy device (HOST:CONTAINER, repeatable)")
	cmd.Flags().StringArrayVarP(&opts.Volume, "volume", "v", nil, "bind-mount a host path or attach a managed volume (SRC:DST, repeatable)")
	cmd.Flags().StringVar(&opts.Network, "network", "", "NIC device's network")
	cmd.Flags().StringVar(&opts.IP, "ip", "", "static ipv4.address on the NIC device (requires --network)")
	cmd.Flags().StringVar(&opts.Restart, "restart", "", "always|unless-stopped|on-failure|no (boot.autorestart is a plain boolean -- retry counts aren't supported)")
	cmd.Flags().StringVar(&opts.Pool, "pool", opts.Pool, "storage pool a bare -v name:path managed-volume mount attaches in")
	cmd.Flags().StringArrayVar(&opts.Profiles, "profile", nil, "an existing Incus profile to layer in addition (repeatable)")
	cmd.Flags().BoolVar(&opts.Rm, "rm", false, "delete the instance automatically once it stops, for any reason (Incus's own ephemeral flag)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "compute and print the plan without applying it")
	// Flags stop being recognized once the first positional arg (IMAGE) is
	// seen -- without this, pflag's default interspersed scanning would
	// try to parse a CMD arg that happens to look like "--enable-app" as
	// an unknown flag on `run` itself, rather than passing it through.
	cmd.Flags().SetInterspersed(false)
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

func newDaemonCmd() *cobra.Command {
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run tink's periodic actions as a persistent process, instead of via cron",
	}

	daemonCmd.AddCommand(newDaemonRunCmd())
	daemonCmd.AddCommand(newDaemonInstallCmd())
	return daemonCmd
}

func newDaemonRunCmd() *cobra.Command {
	opts := ingress.DefaultOptions()
	var interval time.Duration

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the ingress reconciler loop until stopped",
		Long: `run reconciles ingress registrations immediately, then again every
--interval, until it receives SIGTERM or SIGINT -- the mode an init
system's unit file (see "tink daemon install") actually invokes.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx, cmd.OutOrStdout(), daemon.RunOptions{
				Interval:       interval,
				IngressOptions: opts,
			})
		},
	}

	cmd.Flags().StringVar(&opts.Socket, "socket", opts.Socket, "Incus daemon unix socket path")
	cmd.Flags().StringVar(&opts.RoutesDir, "routes-dir", opts.RoutesDir, "generated ingress routes directory")
	cmd.Flags().StringVar(&opts.IngressInstance, "ingress-instance", opts.IngressInstance, "name of the ingress instance to reload")
	cmd.Flags().DurationVar(&interval, "interval", time.Minute, "how often to reconcile")
	return cmd
}

func newDaemonInstallCmd() *cobra.Command {
	unitOpts := daemon.DefaultUnitOptions()
	var initSystem string

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Print a unit/init file for supervising \"tink daemon run\", for review before installing",
		Long: `install prints (to stdout, for you to review and redirect yourself) the
unit or init script content for running "tink daemon run" under the given
init system. It does not write, enable, or start anything -- generating
and applying are kept separate, the same way "kubectl create" and
"kubectl apply" are two different steps.

  tink daemon install --init=systemd > /etc/systemd/system/tink-daemon.service
  tink daemon install --init=openrc  > /etc/init.d/tink-daemon

Defaults to auto-detecting the running init system if --init is omitted;
fails rather than guessing if detection is inconclusive.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if initSystem == "" {
				initSystem = daemon.DetectInit()
				if initSystem == "" {
					return fmt.Errorf("could not detect the running init system -- pass --init explicitly (one of: %v)", daemon.InitSystems)
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "detected init system: %s\n", initSystem)
			}

			out, err := daemon.Generate(initSystem, unitOpts)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}

	cmd.Flags().StringVar(&initSystem, "init", "", fmt.Sprintf("init system to generate for (one of: %v; default: auto-detect)", daemon.InitSystems))
	cmd.Flags().StringVar(&unitOpts.ExecPath, "exec-path", unitOpts.ExecPath, "path to the tink binary on the target host")
	return cmd
}
