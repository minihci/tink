package main

import (
	"fmt"
	"io"
	"slices"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/incusapi"
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

join itself only prints. It reads the files you name and nothing else (no server, no credential, no secret), so it changes nothing. Writing a
rendering to the helper that serves it is "tink join apply" (only dns so far). The output is for comparing with the pieces that are written by hand today, and --only prints one section alone so a
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
	cmd.AddCommand(newJoinApplyCmd())
	return cmd
}

// runJoin loads each argument as one stack, joins them, and prints.
func runJoin(out, errw io.Writer, args []string, only string) error {
	if only != "" && !slices.Contains(joinSections, only) {
		return fmt.Errorf("--only %q: the sections are %s", only, strings.Join(joinSections, ", "))
	}
	stacks, err := loadJoinStacks(args)
	if err != nil {
		return err
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

// loadJoinStacks loads each argument as one stack: a file, or files joined by commas.
func loadJoinStacks(args []string) ([]join.Stack, error) {
	var stacks []join.Stack
	for _, arg := range args {
		rs, err := resolve.LoadFiles(strings.Split(arg, ","))
		if err != nil {
			return nil, err
		}
		name, err := resolve.StackName(rs)
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = arg
		}
		stacks = append(stacks, join.Stack{Name: name, Resources: rs})
	}
	return stacks, nil
}

func newJoinApplyCmd() *cobra.Command {
	var socket string
	var o join.ApplyOptions
	cmd := &cobra.Command{
		Use:   "apply [flags] SECTION FILE...",
		Short: "Write one rendering to the helper that serves it (only dns so far)",
		Long: `apply makes one helper's generated file equal to what the join renders for it. Only the dns section is built:

  tink join apply dns platform/platform.yaml canary/canary.yaml

It writes /hosts on the dns helper, which CoreDNS's hosts plugin answers from first and re-reads every few seconds, so a change needs no
restart. That file holds ONLY generated records; the platform's own static names (the sign-on portal, the directory's UI) belong in the zone
file, /db.<zone>, written by hand. The file is rewritten whole, so a name that is no longer published disappears. docs/join.md has the layout
and the reasons (CoreDNS accepts only one hosts plugin per server block, and where a name is in both files the generated one wins).

It refuses, and writes nothing, when the join has any error; when the files name no published name (applying would empty the records: that
usually means the wrong files were given; --allow-empty says it is meant); when the file exists and tink did not write it (it may hold hand-written
records; --adopt says to overwrite it, once); when a published name is already a record in the zone file (it would
take the platform's own name over); and when a read fails (a failed read is an error, never an empty file). After a write it reads the file back.

  --dry-run   compute and show what would change, write nothing

It acts through the Incus API with the certificate of whoever runs it (--remote works). The same code is meant to be run by the daemon helper
on a timer; nothing here is specific to the person running it.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			return runJoinApply(cmd.OutOrStdout(), cmd.ErrOrStderr(), incusFiles{server}, args[0], args[1:], o)
		},
	}
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "show what would change and write nothing")
	cmd.Flags().BoolVar(&o.Adopt, "adopt", false, "overwrite a /hosts that tink did not write (once, when moving to generated records; move the platform's own names to the zone file first)")
	cmd.Flags().BoolVar(&o.AllowEmpty, "allow-empty", false, "accept a rendering with no published name (it empties the generated records)")
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	return cmd
}

// runJoinApply joins the stacks and applies one section. Findings go to errw, as for the print-only command.
func runJoinApply(out, errw io.Writer, files join.Files, section string, stackArgs []string, o join.ApplyOptions) error {
	switch {
	case section == "dns":
	case slices.Contains(joinSections, section):
		return fmt.Errorf("%s cannot be applied yet: only dns is built (print the others with `tink join --only %s`)", section, section)
	default:
		return fmt.Errorf("%q is not a section: the sections are %s, and only dns can be applied", section, strings.Join(joinSections, ", "))
	}
	stacks, err := loadJoinStacks(stackArgs)
	if err != nil {
		return err
	}
	res := join.Build(stacks)
	for _, f := range res.Findings {
		fmt.Fprintln(errw, f)
	}
	rep, err := join.ApplyDNS(files, res, o)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "dns: %s on %s (project %s)\n", rep.Path, rep.Instance, rep.Project)
	for _, l := range rep.Removed {
		fmt.Fprintf(out, "  - %s\n", l)
	}
	for _, l := range rep.Added {
		fmt.Fprintf(out, "  + %s\n", l)
	}
	switch {
	case !rep.Changed:
		fmt.Fprintln(out, "no changes")
	case o.DryRun && rep.Created:
		fmt.Fprintln(out, "would create the file (dry run: nothing was written)")
	case o.DryRun:
		fmt.Fprintln(out, "would rewrite the file (dry run: nothing was written)")
	case rep.Created:
		fmt.Fprintln(out, "created, and read back")
	default:
		fmt.Fprintln(out, "rewritten, and read back")
	}
	return nil
}

// maxJoinFile bounds what is read from an instance: a hosts file or a zone file is small, and anything larger is not what the join expects.
const maxJoinFile = 1 << 20

// incusFiles is the join's file API on a real server: the instance file API of whichever project the file is in.
type incusFiles struct{ server incus.InstanceServer }

func (f incusFiles) Read(project, instance, path string) (string, bool, error) {
	rc, _, found, err := incusapi.LookupInstanceFile(f.server.UseProject(project), instance, path)
	if err != nil || !found {
		return "", false, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, maxJoinFile+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > maxJoinFile {
		return "", false, fmt.Errorf("%s is larger than %d bytes, which is not a file the join expects", path, maxJoinFile)
	}
	return string(b), true, nil
}

func (f incusFiles) Write(project, instance, path, content string) error {
	return f.server.UseProject(project).CreateInstanceFile(instance, path, incus.InstanceFileArgs{
		Content:   strings.NewReader(content),
		Type:      "file",
		WriteMode: "overwrite",
		Mode:      0o644,
	})
}
