package main

import (
	"fmt"
	"io"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/opinion"
	"github.com/minihci/tink/internal/resolve"
)

func newOpinionsCmd() *cobra.Command {
	var socket string
	var verbose bool
	cmd := &cobra.Command{
		Use:   "opinions [FILE...]",
		Short: "Say where a stack meets tink's opinions, and where it departs from them",
		Long: fmt.Sprintf(`opinions reads a stack (default %s) and the server it would be applied to, and reports, for each opinion tink holds, which resources meet it,
which depart from it with a reason (accepted), and which depart without one. It changes nothing and always exits 0: opinions warn, they do not block.

A resource departs on purpose with a reason: "accept: {storage: \"regenerable cache\"}" on a storage-volume or an instance, or
"backup: none: REASON" for the backup opinion. The reason is required, and is shown here.

The same departures end "tink plan". See docs/opinion-as-code.md.`, resolve.DefaultFile),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			resources, err := resolve.LoadFiles(args)
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			findings, err := resolve.Opinions(server, resources)
			if err != nil {
				return err
			}
			printOpinions(cmd.OutOrStdout(), findings, verbose)
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also list what meets each opinion")
	return cmd
}

// printOpinions is the audit: one block per opinion, with what it prefers and why, the counts, and each departure and acceptance.
func printOpinions(w io.Writer, findings []opinion.Finding, verbose bool) {
	for i, s := range resolve.Summarise(findings) {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  (%s)\n", s.Opinion.Name, counts(s))
		fmt.Fprintf(w, "  prefers: %s\n", s.Opinion.Default)
		if s.N == 0 {
			continue
		}
		for _, f := range findings {
			if f.Opinion != s.Opinion.Name {
				continue
			}
			switch f.State {
			case opinion.Departs:
				fmt.Fprintf(w, "  departs   %s: %s\n", f.Resource, f.Message)
			case opinion.Accepted:
				fmt.Fprintf(w, "  accepted  %s: %s\n", f.Resource, f.Reason)
			case opinion.Met:
				if verbose {
					fmt.Fprintf(w, "  met       %s\n", f.Resource)
				}
			}
		}
		if s.Departs > 0 {
			fmt.Fprintf(w, "  to put it right: %s\n", s.Opinion.FixCost)
		}
	}
}

func counts(s resolve.Summary) string {
	if s.N == 0 {
		return "nothing in this stack for it to judge"
	}
	parts := []string{fmt.Sprintf("%d met", s.Met)}
	if s.Accepted > 0 {
		parts = append(parts, fmt.Sprintf("%d accepted", s.Accepted))
	}
	if s.Departs > 0 {
		parts = append(parts, fmt.Sprintf("%d depart", s.Departs))
	}
	return fmt.Sprintf("%d judged: %s", s.N, strings.Join(parts, ", "))
}

// noteOpinions ends a plan with the departures nobody has explained. It says nothing when there are none, and never fails the plan.
func noteOpinions(w io.Writer, server incus.InstanceServer, resources []resolve.Resource) {
	findings, err := resolve.Opinions(server, resources)
	if err != nil {
		fmt.Fprintf(w, "note: could not judge the stack against tink's opinions: %v\n", err)
		return
	}
	var departs []opinion.Finding
	for _, f := range findings {
		if f.State == opinion.Departs {
			departs = append(departs, f)
		}
	}
	if len(departs) == 0 {
		return
	}
	fmt.Fprintf(w, "opinions: %d departure(s) nobody has explained (tink opinions says more):\n", len(departs))
	for _, f := range departs {
		fmt.Fprintf(w, "  [%s] %s: %s\n", f.Opinion, f.Resource, f.Message)
	}
}
