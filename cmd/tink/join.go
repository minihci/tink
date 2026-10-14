package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/join"
	"github.com/minihci/tink/internal/resolve"
)

// joinSections are the names `tink join --only` accepts, in the order the sections print.
var joinSections = []string{"dns", "mesh", "caddy", "sso", "acl"}

func newJoinCmd() *cobra.Command {
	var only string
	cmd := &cobra.Command{
		Use:   "join [flags] FILE...",
		Short: "Print what the site's published names need from its helpers: dns records, mesh view, proxy routes, sign-on rules, ACL rules",
		Long: `join reads stack files and prints, for every kind: publish in them, the pieces the site's other helpers would need to match it:

  dns    the dns helper's hosts-file records
  mesh   the mesh control plane's extra records (Headscale's JSON)
  caddy  the proxy's routes, as handle blocks for the wildcard site
  sso    the sign-on helper's access rules (Authelia)
  acl    the ACL rules on the apps' bridges

An app states what it provides (user.tink.offer.<offer>.port and .proto on its instance). A publish gives one offer a name in the site's zone
and says which groups may use it. The helpers state the rest of what the join needs: user.tink.role on each, user.tink.dns.zone on the dns
helper, user.tink.leg.<nic>: serves|mesh|discovery on the ingress helper's NICs. docs/join.md has a complete example.

join only prints. It reads the files you name and nothing else (no server, no credential, no secret), so it changes nothing; applying any of
this is not built yet. Its output is for comparing with the pieces that are written by hand today, and --only prints one section alone so a
diff is easy:

  tink join platform/platform.yaml canary/canary.yaml
  diff <(tink join --only dns platform/platform.yaml canary/canary.yaml) hosts.txt

Each FILE is one stack, which is what gives its resources the stack's default project. A stack split across several files is given as one
argument with the files joined by commas (a.yaml,b.yaml).

Findings go to standard error. Any error means NOTHING is printed and the exit status is 1: a partial rendering (a mesh view missing one
name) looks exactly like a complete one. Warnings are printed beside a complete rendering.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runJoin(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, only)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "print one section's body alone, with no heading: "+strings.Join(joinSections, ", "))
	return cmd
}

// runJoin loads each argument as one stack, joins them, and prints.
func runJoin(out, errw io.Writer, args []string, only string) error {
	if only != "" && !slices.Contains(joinSections, only) {
		return fmt.Errorf("--only %q: the sections are %s", only, strings.Join(joinSections, ", "))
	}
	var stacks []join.Stack
	for _, arg := range args {
		files := strings.Split(arg, ",")
		rs, err := resolve.LoadFiles(files)
		if err != nil {
			return err
		}
		name, err := resolve.StackName(rs)
		if err != nil {
			return err
		}
		if name == "" {
			name = arg
		}
		stacks = append(stacks, join.Stack{Name: name, Resources: rs})
	}

	res := join.Build(stacks)
	for _, f := range res.Findings {
		fmt.Fprintln(errw, f)
	}
	if n := res.Errors(); n > 0 {
		return fmt.Errorf("%d error(s): nothing was rendered", n)
	}

	sections := res.Sections()
	if only != "" {
		for _, s := range sections {
			if s.Name == only {
				fmt.Fprint(out, s.Body)
				return nil
			}
		}
		return fmt.Errorf("nothing to print for --only %s (see the warnings above, if any)", only)
	}
	for i, s := range sections {
		if i > 0 {
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "== %s: %s\n%s", s.Name, s.Title, s.Body)
	}
	return nil
}
