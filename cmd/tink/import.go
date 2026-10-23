package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/compose"
)

func newImportCmd() *cobra.Command {
	var opts compose.Options

	cmd := &cobra.Command{
		Use:   "import [flags] COMPOSE-FILE",
		Short: "Write a stack from a compose file, once",
		Long: `import reads a compose file, the install instructions most self-hosted projects publish, and writes the
stack that describes the same app to stdout. It is "tink run" for a compose file: the translation of ports, volumes,
environment, restart and user is the same one, so a port means the same thing in both.

What it writes is a starting point for you to keep and edit. Nothing reads the compose file again.
It carries what the compose file says about the app and nothing about your site: no network, address, project,
published name or backup policy. A comment marks each volume's missing backup answer; "tink plan" warns until you give one.

It supports the keys the install instructions of popular projects use (image, restart, volumes, ports, environment,
env_file, depends_on, user, entrypoint, privileged, devices, tmpfs, shm_size) and refuses the rest, naming the key and
the service, and printing every refusal rather than the first. Host networking (network_mode: host) is refused with
what Incus does instead; docs/import.md has the whole table. A key that tink cannot express but the app may not need (healthcheck, cap_add) is
dropped with a warning, and the warning is also a comment in the output. A relative path (./data) becomes a managed
volume. A $VAR or ${VAR} is read from a .env file beside the compose file, never from the shell.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			opts.Dir = filepath.Dir(args[0])
			res, err := compose.Import(src, opts)
			if err != nil {
				return err
			}
			for _, n := range res.Notes {
				fmt.Fprintln(cmd.ErrOrStderr(), "note:", n)
			}
			for _, w := range res.Warnings {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", w)
			}
			if len(res.Refused) > 0 {
				for _, r := range res.Refused {
					fmt.Fprintln(cmd.ErrOrStderr(), "refused:", r)
				}
				return fmt.Errorf("%d thing(s) in %s cannot be imported: nothing written", len(res.Refused), args[0])
			}
			fmt.Fprint(cmd.OutOrStdout(), res.YAML)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Stack, "stack", "", "the stack's name (default: the compose file's name:, else its directory's name)")
	cmd.Flags().StringVar(&opts.Pool, "pool", "default", "the storage pool managed volumes are made in")
	return cmd
}
