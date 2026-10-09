// Command tink is the companion CLI for the Mini HCI / Tink homelab
// platform. See the repo README for what that means; this file is
// intentionally just argument wiring — every capability's real logic
// lives in its own internal package under internal/.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
	_ "time/tzdata" // the helper runs in images with no zoneinfo of their own, and its schedules are evaluated in a named zone

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/backup"
	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/bootstrap"
	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/ingress"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/run"
)

func main() {
	if err := execute(newRootCmd(), os.Stdout, os.Stderr); err != nil {
		var ec *exitCodeError
		if errors.As(err, &ec) {
			os.Exit(ec.code)
		}
		os.Exit(1)
	}
}

// exitCodeError ends the process with a particular status, which `tink helper status --check` uses to say 0, 1 or 2 to a monitor.
// A silent one has already said what it had to say on standard output, so it prints nothing more.
type exitCodeError struct {
	code   int
	msg    string
	silent bool
}

func (e *exitCodeError) Error() string { return e.msg }

// execute runs root with everything it prints, and the final error, passed through the redactor,
// which scrubs any secret tink has decrypted. The writers are line-buffered, so they are flushed
// before returning: main's os.Exit would skip a defer.
func execute(root *cobra.Command, stdout, stderr io.Writer) error {
	out, errw := redactor.Writer(stdout), redactor.Writer(stderr)
	root.SetOut(out)
	root.SetErr(errw)
	err := root.Execute()
	if err != nil {
		var ec *exitCodeError
		if !errors.As(err, &ec) || !ec.silent {
			fmt.Fprintln(errw, err)
		}
	}
	out.Flush()
	errw.Flush()
	return err
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "tink",
		Short: "Tink is not Kubernetes",
		Long: `tink is the companion tool for the Mini HCI / Tink homelab platform:
opinionated, Incus-native primitives for running self-hosted projects,
made executable instead of just documented.`,
		SilenceUsage: true,
		// execute prints the error (through the secret redactor); cobra printing it too said everything twice
		SilenceErrors: true,
		// Choose the server once for the whole invocation: --remote, else $TINK_REMOTE, else the local daemon.
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			name, _ := cmd.Flags().GetString("remote")
			if !cmd.Flags().Changed("remote") {
				if env := os.Getenv("TINK_REMOTE"); env != "" {
					name = env
				}
			}
			incusapi.UseRemote(name)
		},
	}
	root.PersistentFlags().String("remote", "", "Incus remote (from the Incus client configuration) to manage instead of the local daemon; also $TINK_REMOTE")

	root.AddCommand(newDeployCmd())
	root.AddCommand(newRunCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newExportCmd())
	root.AddCommand(newSecretCmd())
	root.AddCommand(newVolBackupCmd())
	root.AddCommand(newRemoteCmd())
	root.AddCommand(newHelperCmd())
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
// injectedVersion is the release version, compiled in with `-ldflags "-X main.injectedVersion=v1.2.3"` by the release workflow. A
// build in a container has no .git, so Go's embedded VCS metadata is empty there and two such builds could not be told apart;
// this is what the helper image reports, and what `tink helper install` uses to pick the image that matches the binary.
var injectedVersion string

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

	if injectedVersion != "" {
		version = injectedVersion
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
		PreRunE: refuseUnderRemote("tink deploy"),
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
		Long: `run creates an instance from docker-run-shaped flags, translated
onto Incus primitives: the image is created (not started), the config
keys and devices the flags correspond to are applied, then it starts.
See internal/run/DESIGN.md for the full flag-mapping table and its
deliberate non-goals -- this is not a Docker CLI clone: incus's own
ps/exec/logs/stop/rm are already just as short as their Docker
equivalents, and docker logs specifically has no Incus equivalent to
translate to at all.

Use --dry-run to compute and print the plan without creating or
starting anything, matching tink deploy's and tink ingress reconcile's
existing convention.

--incus-config and --incus-device are the escape hatch: Incus's own vocabulary, for anything
the flags above do not say (memory and cpu limits, sysctls, host devices, a tmpfs, privileged...).
They may add to what the flags decide but not override it. See docs/docker-gap-analysis.md for
which Docker flags map to which key or device.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Image = args[0]
			opts.Cmd = args[1:]

			opts.Out = cmd.OutOrStdout() // lines are printed as they happen: a first image pull is a silent minute otherwise
			_, err := run.Run(opts)
			return err
		},
	}

	cmd.Flags().StringVar(&opts.Socket, "socket", opts.Socket, "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Incus project to create the instance in (default: the daemon's own default project)")
	cmd.Flags().StringVar(&opts.Name, "name", "", "instance name (required)")
	cmd.Flags().StringArrayVarP(&opts.Env, "env", "e", nil, "set an environment variable (KEY=VALUE, repeatable)")
	cmd.Flags().StringArrayVarP(&opts.Publish, "publish", "p", nil, "publish a port via a proxy device: [IP:]HOST[-HOST]:CONTAINER[-CONTAINER][/tcp|/udp] (repeatable)")
	cmd.Flags().StringArrayVarP(&opts.Volume, "volume", "v", nil, "bind-mount an absolute host path (a path on the server, with --remote) or attach a managed volume: SRC:DST[:ro|rw|shift] (repeatable); shift maps ids so the app can write a host path")
	cmd.Flags().StringVar(&opts.Network, "network", "", "NIC device's network")
	cmd.Flags().StringVar(&opts.IP, "ip", "", "static ipv4.address on the NIC device (requires --network)")
	cmd.Flags().StringVar(&opts.Restart, "restart", "", "always|unless-stopped|on-failure|no (boot.autorestart is a plain boolean -- retry counts aren't supported)")
	cmd.Flags().StringVar(&opts.Pool, "pool", opts.Pool, "storage pool a bare -v name:path managed-volume mount attaches in")
	cmd.Flags().StringArrayVar(&opts.Profiles, "profile", nil, "an existing Incus profile to layer in addition (repeatable)")
	cmd.Flags().StringArrayVar(&opts.EnvFile, "env-file", nil, "read KEY=VALUE lines from a file on this machine (repeatable; -e overrides it)")
	cmd.Flags().BoolVar(&opts.Privileged, "privileged", false, "run the container privileged (security.privileged=true): no user-namespace isolation. Not Docker's every-device grant: name devices with --device")
	cmd.Flags().StringVarP(&opts.Memory, "memory", "m", "", "memory limit, as Docker writes it (512m, 1g); becomes limits.memory")
	cmd.Flags().StringVar(&opts.CPUs, "cpus", "", "CPU time limit in CPUs (0.5, 2); becomes a hard limits.cpu.allowance, not the number of CPUs the instance sees")
	cmd.Flags().StringArrayVar(&opts.Device, "device", nil, "pass a host character device through, HOST[:CONTAINER[:rwm]] (a serial adapter, /dev/kvm); for a GPU or a block device use --incus-device (repeatable)")
	cmd.Flags().StringVar(&opts.User, "user", "", "run the process as UID[:GID] (numbers only); volumes created by this run are owned by it")
	cmd.Flags().StringArrayVar(&opts.IncusConfig, "incus-config", nil, "an Incus instance config KEY=VALUE for anything no flag says, e.g. limits.memory=512MiB (repeatable)")
	cmd.Flags().StringArrayVar(&opts.IncusDevice, "incus-device", nil, "an Incus device as 'NAME type=TYPE key=value ...' for anything no flag says, e.g. 'cache type=disk source=tmpfs: path=/cache size=64MiB' (repeatable)")
	cmd.Flags().BoolVar(&opts.Rm, "rm", false, "delete the instance automatically once it stops, for any reason (Incus's own ephemeral flag)")
	cmd.Flags().BoolVar(&opts.VM, "vm", false, "create a virtual machine instead of a container (passes --vm to incus init)")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "compute and print the plan without applying it")
	// Flags stop being recognized once the first positional arg (IMAGE) is
	// seen -- without this, pflag's default interspersed scanning would
	// try to parse a CMD arg that happens to look like "--enable-app" as
	// an unknown flag on `run` itself, rather than passing it through.
	cmd.Flags().SetInterspersed(false)
	return cmd
}

func newPlanCmd() *cobra.Command {
	var socket string
	var offline bool
	var secretFlags stackSecretFlags

	cmd := &cobra.Command{
		Use:   "plan [flags] [FILE...]",
		Short: "Show what would change to converge a set of resources declared in YAML",
		Long: fmt.Sprintf(`plan is tink's resolver (the design is in docs/resolver-architecture.md):
a lightweight, tink-native take on the resolver half of that document's
proposed architecture. It computes a dependency graph from each resource's own
Project/Profiles/device sources plus any explicit depends_on, then
reports what "tink plan apply" would do, level by level -- everything in
one level would run concurrently, since nothing in it depends on
anything else in it.

With no FILE given, reads %s from the current directory.

Deliberately stateless: every check queries the Incus daemon directly,
never a separately stored record of what was created last time.

plan only ever reads -- there's no --dry-run flag here because there's
nothing to opt out of. Run "tink plan apply" on the same files to
actually converge.

plan also compares the image each instance was built from with the
image its YAML names, by asking the registry what the YAML's reference
resolves to now (--offline skips that). What a difference means is
set per instance by on_image_change: report (the default: the instance
is BLOCKED and nothing on it changes), ignore, or rebuild. See
docs/image-updates.md.

An instance's environment.* values may be ${secret:NAME}, read from an
age-encrypted secrets.yaml beside the stack (see "tink secret" and
docs/secrets.md). plan never prints a secret, or the value it replaces.

Every storage-volume should also answer "how is this backed up?" with a
backup: block -- scheduled snapshots, or an explicit none: with a reason.
For now a volume that does not only gets a warning; that will become an
error. A volume can also declare copies to kind: backup-target resources;
plan checks them against 3-2-1 and warns. Copies are run by the helper
(tink helper install): plan notes when a stack declares copies and there is
none, and when the helper is not well. See docs/volume-backup.md and docs/helper.md.`, resolve.DefaultFile),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resources, err := resolve.LoadFiles(args)
			if err != nil {
				return err
			}
			if resources, err = secretFlags.expand(resources, args); err != nil {
				return err
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			// before the graph is built: it rewrites each instance's image: to the form Incus resolves
			notices, err := resolve.QualifyImages(server, resources)
			if err != nil {
				return err
			}
			for _, n := range notices {
				fmt.Fprintln(cmd.ErrOrStderr(), n)
			}
			levels, err := resolve.Levels(resources)
			if err != nil {
				return err
			}
			// Planned one dependency level at a time, but a volume's 3-2-1 check needs the
			// backup targets from an earlier level, so hand the options the whole stack.
			opts := helperPolicyOptions(server, resolve.NewPlanOptions(offline).ForResources(resources))
			for i, level := range levels {
				fmt.Fprintf(cmd.OutOrStdout(), "level %d:\n", i)
				plans, err := resolve.PlanWithOptions(server, level, opts)
				if err != nil {
					return err
				}
				for _, p := range plans {
					printPlanned(cmd.OutOrStdout(), p)
				}
			}
			noteUndeclaredPolicies(cmd.OutOrStdout(), backuprun.ServerEngine{Server: server}, resources)
			noteHelper(cmd.OutOrStdout(), server, time.Now(), resources)
			return nil
		},
	}

	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().BoolVar(&offline, "offline", false, "do not consult OCI registries; images that cannot then be verified are blocked under on_image_change: rebuild and only warned about otherwise")
	secretFlags.bind(cmd)
	cmd.AddCommand(newPlanApplyCmd())
	return cmd
}

func newPlanApplyCmd() *cobra.Command {
	var socket string
	var offline bool
	var secretFlags stackSecretFlags

	cmd := &cobra.Command{
		Use:   "apply [flags] [FILE...]",
		Short: "Converge a set of resources declared in YAML to match",
		Long: fmt.Sprintf(`apply computes the same plan "tink plan" would show, then actually
converges: it runs level by level, every resource within one level
concurrently, and only moves to the next level once the current one
finishes entirely.

With no FILE given, reads %s from the current directory.

Deliberately stateless: every check queries the Incus daemon directly at
apply time -- there's no saved plan file from "tink plan" to feed back
in here, so nothing can go stale between the two. If a real workflow
ever needs to apply exactly what a specific "tink plan" run showed,
possibly later or by someone else, that gap is the reason to add one.

apply ends with a one-line summary and exits non-zero if it left
anything BLOCKED or SKIPPED, so an exit status of 0 means converged.
An instance with image drift is only converged by apply if its YAML
says on_image_change: rebuild; see docs/image-updates.md.`, resolve.DefaultFile),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resources, err := resolve.LoadFiles(args)
			if err != nil {
				return err
			}
			if resources, err = secretFlags.expand(resources, args); err != nil {
				return err
			}
			// what the helper can read is asked once, before anything is written: a policy it would skip is refused, not applied
			opts := resolve.NewPlanOptions(offline)
			server, cerr := incusapi.Connect(socket)
			if cerr == nil {
				opts = helperPolicyOptions(server, opts)
				notices, err := resolve.QualifyImages(server, resources)
				if err != nil {
					return err
				}
				for _, n := range notices {
					fmt.Fprintln(cmd.ErrOrStderr(), n)
				}
			}
			actions, err := resolve.ApplyWithOptions(socket, resources, opts)
			for _, a := range actions {
				fmt.Fprintln(cmd.OutOrStdout(), a)
			}
			if cerr == nil {
				noteUndeclaredPolicies(cmd.OutOrStdout(), backuprun.ServerEngine{Server: server}, resources)
				noteHelper(cmd.OutOrStdout(), server, time.Now(), resources)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().BoolVar(&offline, "offline", false, "do not consult OCI registries; images that cannot then be verified are blocked under on_image_change: rebuild and only warned about otherwise")
	secretFlags.bind(cmd)
	return cmd
}

func actionLabel(a resolve.Action) string {
	switch a {
	case resolve.ActionCreate:
		return "would create"
	case resolve.ActionUpdate:
		return "would update"
	case resolve.ActionRebuild:
		return "would REBUILD"
	case resolve.ActionBlocked:
		return "BLOCKED"
	default:
		return "no changes"
	}
}

func printPlanned(w interface{ Write([]byte) (int, error) }, p resolve.PlannedResource) {
	fmt.Fprintf(w, "  %s/%s: %s\n", p.Resource.Kind, p.Resource.Name, actionLabel(p.Action))
	for _, c := range p.Changes {
		fmt.Fprintf(w, "      %s\n", c)
	}
	for _, d := range p.Drift {
		fmt.Fprintf(w, "      drift: %s\n", d)
	}
	for _, b := range p.Blocked {
		fmt.Fprintf(w, "      blocked: %s\n", b)
	}
	for _, x := range p.Warnings {
		fmt.Fprintf(w, "      warning: %s\n", x)
	}
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
discovers instances that opt in via user.tink.ingress.{domain,port,enabled}
config and renders/applies the shared ingress instance's routes.

Use --dry-run to compute and report what would change without writing
anything or reloading Caddy -- this is how it's meant to be run alongside
the live bash version before its cron entry actually gets moved over.`,
		PreRunE: ingressUnderRemote("tink ingress reconcile", &opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			useInstanceRoutesDir(cmd, &opts)
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
		Use:     "status",
		Short:   "Show what's currently registered, without changing anything",
		PreRunE: ingressUnderRemote("tink ingress status", &opts),
		RunE: func(cmd *cobra.Command, args []string) error {
			useInstanceRoutesDir(cmd, &opts)
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
	cmd.Flags().StringVar(&opts.RoutesDir, "routes-dir", opts.RoutesDir, "generated ingress routes directory (with --via-api: the path inside the ingress instance)")
	cmd.Flags().BoolVar(&opts.ViaAPI, "via-api", false, "read and write the route files through the ingress instance's file API instead of on this host's filesystem: works from anywhere, and under --remote")
	cmd.Flags().StringVar(&opts.IngressInstance, "ingress-instance", opts.IngressInstance, "name of the ingress instance to reload")
}

// ingressUnderRemote is the PreRunE of the ingress commands: they work on a path inside the host's storage pool, which is the wrong place
// on any other machine, unless they go through the ingress instance's file API instead (--via-api), which is the same everywhere.
func ingressUnderRemote(what string, opts *ingress.Options) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if via, _ := cmd.Flags().GetBool("via-api"); via {
			return nil
		}
		return refuseUnderRemote(what)(cmd, args)
	}
}

// useInstanceRoutesDir points the reconcile at the generated directory inside the ingress instance when it goes through the file API and
// the caller did not name another one.
func useInstanceRoutesDir(cmd *cobra.Command, opts *ingress.Options) {
	if opts.ViaAPI && !cmd.Flags().Changed("routes-dir") {
		opts.RoutesDir = ingress.InstanceRoutesDir
	}
}

func printIngressResult(cmd *cobra.Command, result *ingress.Result, dryRun bool) {
	out := cmd.OutOrStdout()
	for _, w := range result.Warnings {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARN: %s\n", w)
	}
	if len(result.Legacy) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "NOTE: %s\n", ingress.LegacyNotice(result.Legacy))
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
	daemonCmd.AddCommand(newDaemonEnqueueCmd(), newDaemonJobsCmd(), newDaemonCancelCmd())
	return daemonCmd
}

func newDaemonRunCmd() *cobra.Command {
	opts := ingress.DefaultOptions()
	var interval, schedulerInterval time.Duration
	var jobsDir, timezone string
	var noIngress bool
	var statusInstance, statusProject string
	var statusHeartbeat time.Duration
	var ingressViaAPI bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run tink's periodic work (ingress reconcile, backup scheduler) until stopped",
		Long: `run reconciles ingress registrations immediately, then again every
--interval, until it receives SIGTERM or SIGINT -- the mode an init
system's unit file (see "tink daemon install") actually invokes.

With --jobs it also runs the helper's work: a scheduler that, every --scheduler-interval,
lists the volumes that carry a copy policy (written by "tink plan apply" from the stack's
backup: block) and queues a backup job in --jobs when one of their copies is due (and none
is already queued or running), and an executor that runs the queued jobs one at a time,
oldest first. There is no stack to keep in step: the volumes say what to copy. Schedules
are evaluated in --timezone (default: this machine's). A failing copy backs off instead of
being retried every tick. Look at the work with "tink daemon jobs".

Each worker is isolated: one that crashes is logged and restarted, and does not stop the
others. --no-ingress runs only the helper's work, beside an ingress daemon that already exists.

With --remote (or $TINK_REMOTE) the daemon manages that server over its API, which is how the
helper runs: a client of its own host. The ingress half reads and writes a directory, and the
default one is a path inside the host's storage pool, so under a remote it needs either
--ingress-via-api (the route files are read and written through the ingress instance's file API, so
nothing depends on this machine), --no-ingress, or a --routes-dir that this process can actually
reach. Anything else is refused rather than act on the wrong machine.`,
		PreRunE: daemonRunUnderRemote,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			opts.ViaAPI = ingressViaAPI
			useInstanceRoutesDir(cmd, &opts)
			ro := daemon.RunOptions{Interval: interval, IngressOptions: opts, NoIngress: noIngress}
			zone := time.Local
			if timezone != "" {
				var err error
				if zone, err = time.LoadLocation(timezone); err != nil {
					return fmt.Errorf("--timezone: %w", err)
				}
			}
			if statusInstance != "" {
				socket := opts.Socket
				project := statusProject
				name := statusInstance
				ro.Status = &daemon.StatusOptions{
					Publisher: &helper.Publisher{
						Heartbeat: statusHeartbeat,
						Patch: func(config map[string]string) error {
							server, err := incusapi.Connect(socket)
							if err != nil {
								return err
							}
							return incusapi.PatchInstanceConfig(server, project, name, config)
						},
					},
					Version: buildVersion(),
					Zone:    zone,
					Remotes: helper.ConfiguredRemotes,
				}
				// this process is the helper: a missing remote is fixed from outside it, not with a CLI that is not in the container
				incusapi.SetRemoteAdvice("this is the tink helper, which has no incus CLI: from a machine that manages the host, make a trust token on that server (`incus config trust add helper -q`) and run `tink helper remote add %s --token-file -`")
				if jobsDir != "" {
					ro.Status.Store = jobs.Store{Dir: jobsDir}
				}
			}
			if jobsDir != "" {
				// a fresh data volume has no jobs directory yet
				if err := os.MkdirAll(jobsDir, 0o700); err != nil {
					return fmt.Errorf("--jobs: %w", err)
				}
				socket := opts.Socket
				ro.Helper = &daemon.Helper{
					Store: jobs.Store{Dir: jobsDir},
					Connect: func() (backuprun.Engine, error) {
						server, err := incusapi.Connect(socket)
						if err != nil {
							return nil, err
						}
						return backuprun.ServerEngine{Server: server}, nil
					},
					Zone:              zone,
					Version:           buildVersion(),
					SchedulerInterval: schedulerInterval,
					Redactor:          redactor,
				}
			} else if noIngress {
				return fmt.Errorf("--no-ingress leaves nothing to run without --jobs")
			}
			return daemon.Run(ctx, cmd.OutOrStdout(), ro)
		},
	}

	cmd.Flags().StringVar(&opts.Socket, "socket", opts.Socket, "Incus daemon unix socket path")
	cmd.Flags().StringVar(&opts.RoutesDir, "routes-dir", opts.RoutesDir, "generated ingress routes directory")
	cmd.Flags().StringVar(&opts.IngressInstance, "ingress-instance", opts.IngressInstance, "name of the ingress instance to reload")
	cmd.Flags().DurationVar(&interval, "interval", time.Minute, "how often to reconcile ingress")
	cmd.Flags().StringVar(&jobsDir, "jobs", "", "directory the backup scheduler queues jobs in and the executor runs them from; giving it turns the backup scheduler on")
	cmd.Flags().StringVar(&timezone, "timezone", "", "time zone schedules are evaluated in, e.g. America/Denver (default: this machine's)")
	cmd.Flags().DurationVar(&schedulerInterval, "scheduler-interval", time.Minute, "how often the scheduler looks for due copies")
	cmd.Flags().BoolVar(&noIngress, "no-ingress", false, "do not run the ingress reconcile loop (only the --jobs work)")
	cmd.Flags().BoolVar(&ingressViaAPI, "ingress-via-api", false, "run the ingress reconcile through the ingress instance's file API instead of on this host's filesystem (the way the helper does, and the only way under --remote)")
	cmd.Flags().StringVar(&statusInstance, "status-instance", "", "publish a status document on this instance's config (user.tink.helper.status), so `tink helper status` and `plan` can see this daemon; the helper sets it to itself")
	cmd.Flags().StringVar(&statusProject, "status-project", "", "the project of --status-instance (default: the connection's own)")
	cmd.Flags().DurationVar(&statusHeartbeat, "status-heartbeat", helper.DefaultHeartbeat, "how often the status document is written when nothing in it has changed")
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

// daemonRunUnderRemote is `daemon run`'s PreRunE. The daemon is a client of whatever server it is pointed at, except for
// the ingress half, which reads and writes a directory: refused under a remote unless that half is off (--no-ingress) or
// the directory was named (--routes-dir), because the default is a path inside the host's own storage pool, which is
// the wrong place, or no place at all, on any machine but the host.
func daemonRunUnderRemote(cmd *cobra.Command, _ []string) error {
	if !incusapi.IsRemote() {
		return nil
	}
	if off, _ := cmd.Flags().GetBool("no-ingress"); off || cmd.Flags().Changed("routes-dir") {
		return nil
	}
	if via, _ := cmd.Flags().GetBool("ingress-via-api"); via {
		return nil
	}
	return fmt.Errorf("tink daemon run is pointed at the remote %q (--remote or $TINK_REMOTE), and its ingress half reads a path inside the host's storage pool, which is not this machine's: "+
		"give --ingress-via-api to run it through the ingress instance's file API, --no-ingress to run only the backup work, or --routes-dir with a directory this process can reach", incusapi.Remote())
}

// refuseUnderRemote is the PreRunE of every command that works on the host's own filesystem or processes (it
// provisions the machine it runs on, or reads a path inside a storage pool). Pointed at a remote server it would
// act on the wrong machine, or on a path that does not exist there, so it says so instead.
func refuseUnderRemote(what string) func(*cobra.Command, []string) error {
	return func(*cobra.Command, []string) error {
		if incusapi.IsRemote() {
			return fmt.Errorf("%s works on the host it runs on, and tink is pointed at the remote %q (--remote or $TINK_REMOTE): run it on that host, or unset the remote", what, incusapi.Remote())
		}
		return nil
	}
}
