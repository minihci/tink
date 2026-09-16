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
	return &cobra.Command{
		Use:   "apply",
		Short: "Converge this host to its declared platform state (capability zero)",
		Long: `apply provisions and reconciles the platform's own infrastructure —
storage volumes, profiles, the ingress/authelia/incus-ui instances, and the
daemon's OIDC/authorization config — the same job
incus-host/scripts/deploy.sh does today. Not yet ported.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return bootstrap.Run()
		},
	}
}

func newIngressCmd() *cobra.Command {
	ingressCmd := &cobra.Command{
		Use:   "ingress",
		Short: "Manage self-registered ingress routes",
	}

	ingressCmd.AddCommand(&cobra.Command{
		Use:   "reconcile",
		Short: "Discover registered instances and converge ingress routes to match",
		Long: `reconcile is tink's port of incus-host/reconciler/reconcile.sh: it
discovers instances that opt in via user.ingress.{domain,port,enabled}
config and renders/applies the shared ingress instance's routes. Not yet
ported.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return ingress.Reconcile()
		},
	})

	ingressCmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show what's currently registered, without changing anything",
		RunE: func(cmd *cobra.Command, args []string) error {
			return ingress.Status()
		},
	})

	return ingressCmd
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
