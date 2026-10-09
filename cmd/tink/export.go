package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/resolve"
)

func newExportCmd() *cobra.Command {
	var opts resolve.ExportOptions
	var socket string

	cmd := &cobra.Command{
		Use:   "export [flags] INSTANCE...",
		Short: "Write live instances (with their project and volumes) as a stack YAML",
		Long: `export is "plan" run backwards: it reads live Incus objects and writes the
stack YAML that describes them, to stdout. It is the step after "tink run": run is the fast
way to find a stack's shape, and the file this writes is what you keep.

It reads only. What it writes is what a person (or tink run) set; the many keys Incus and the
image add (volatile.*, image.*, and oci.* / environment.* that equal what the image itself says,
which it reads from the registry) are left out, and an override of one is kept.
The image reference is a guess from the instance's record until the registry confirms it is what
the instance was built from; it is then pinned to its digest (--no-pin to keep the bare
reference), and if it does not match, it is written with a warning instead of passed off as right.
A value that looks like a secret is never written: it becomes ${secret:NAME}, and a note says to
add it with "tink secret set". A volume's backup: block is written only when the volume carries
snapshots; otherwise it is left out so "plan" says so, rather than an invented answer.

What a live object cannot say stays unsaid: why, kind: exec steps, depends_on, a volume's copy targets.
Treat the file as a starting point to review.

Finally the file is planned against the live server with the same code "tink plan" runs, and
the result is printed to stderr: "no changes" for every resource is the goal. The command exits
non-zero when it is not.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Instances = args
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			res, err := resolve.Export(server, opts)
			if res != nil && res.YAML != "" {
				fmt.Fprint(cmd.OutOrStdout(), res.YAML)
			}
			if res != nil {
				for _, n := range res.Notes {
					fmt.Fprintln(cmd.ErrOrStderr(), "note:", n)
				}
				fmt.Fprintln(cmd.ErrOrStderr(), "checked against the live server with tink plan:")
				for _, p := range res.Plans {
					printPlanned(cmd.ErrOrStderr(), p)
				}
			}
			if err != nil {
				return err
			}
			if !res.Verified {
				return fmt.Errorf("the export does not plan as \"no changes\": see above")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&opts.Project, "project", "", "Incus project the instances are in (default: the server's default project)")
	cmd.Flags().BoolVar(&opts.Offline, "offline", false, "do not consult OCI registries: the image is not verified or pinned and oci.*/environment.* are not compared with it")
	cmd.Flags().BoolVar(&opts.NoPin, "no-pin", false, "write the image reference as the instance records it instead of pinning it to its digest")
	return cmd
}
