package main

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/join"
	"github.com/minihci/tink/internal/prepare"
	"github.com/minihci/tink/internal/resolve"
)

// joinSections are the names `tink join --only` accepts, in the order the sections print.
var joinSections = []string{"dns", "mesh", "caddy", "sso", "acl"}

func newJoinCmd() *cobra.Command {
	var only, socket string
	var fromServer bool
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

join itself only prints. Given files it reads those files and nothing else (no server, no credential, no secret). With --from-server it reads the
SERVER instead and the files not at all: the instances, profiles and networks as they are, and the publishes as the stamps (user.tink.publish.<name>)
that "tink plan apply" wrote onto the instances. That is what the daemon helper will read. It is read-only either way. Writing a rendering to the
helper that serves it is "tink join apply" (only dns so far). The output is for comparing with the pieces that are written by hand today, and --only
prints one section alone so a diff is easy:

  tink join platform/platform.yaml canary/canary.yaml
  tink join --from-server                                  # the same, from what the server holds (--remote and --socket work)
  diff <(tink join --only dns platform/platform.yaml canary/canary.yaml) hosts.txt

Each FILE is one stack, which is what gives its resources the stack's default project. A stack split across several files is given as one
argument with the files joined by commas (a.yaml,b.yaml). FILE and --from-server are alternatives: give one or the other.

Findings go to standard error. Any error means NOTHING is printed and the exit status is 1: a partial rendering (a mesh view missing one
name) looks exactly like a complete one. Warnings are printed beside a complete rendering. With --from-server a stamp that cannot be read is an
error naming its instance.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !fromServer {
				if len(args) == 0 {
					return fmt.Errorf("join needs at least one FILE, or --from-server to read the server instead")
				}
				if socket != "" {
					return fmt.Errorf("--socket is for --from-server: join reads files and no server otherwise")
				}
				return runJoin(cmd.OutOrStdout(), cmd.ErrOrStderr(), args, only)
			}
			if err := noFilesWithFromServer(args); err != nil {
				return err
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			return runJoinFromServer(cmd.OutOrStdout(), cmd.ErrOrStderr(), server, only)
		},
	}
	cmd.Flags().StringVar(&only, "only", "", "print one section's body alone, with no heading: "+strings.Join(joinSections, ", "))
	cmd.Flags().BoolVar(&fromServer, "from-server", false, "read the instances, profiles, networks and publish stamps from the server instead of from FILEs (read-only)")
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path, with --from-server (default: Incus's own resolution)")
	cmd.AddCommand(newJoinApplyCmd())
	return cmd
}

// noFilesWithFromServer refuses FILE arguments beside --from-server: two sources would leave it unclear which one the rendering came from.
func noFilesWithFromServer(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("--from-server reads the server and takes no FILE (got %s): give files, or --from-server, not both", strings.Join(args, " "))
	}
	return nil
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
	return printJoin(out, errw, stacks, only)
}

// runJoinFromServer joins what the server holds and prints, as runJoin does for files. A stamp that cannot be read is reported as the join's own errors are.
func runJoinFromServer(out, errw io.Writer, server incus.InstanceServer, only string) error {
	if only != "" && !slices.Contains(joinSections, only) {
		return fmt.Errorf("--only %q: the sections are %s", only, strings.Join(joinSections, ", "))
	}
	stacks, err := readServerStacks(errw, server)
	if err != nil {
		return err
	}
	return printJoin(out, errw, stacks, only)
}

// readServerStacks is join.FromServer, with the stamps it could not read printed as findings; the error it returns then only says nothing was rendered.
func readServerStacks(errw io.Writer, server incus.InstanceServer) ([]join.Stack, error) {
	stacks, err := join.FromServer(server)
	var se *join.StampError
	if errors.As(err, &se) {
		for _, f := range se.Findings() {
			fmt.Fprintln(errw, f)
		}
		return nil, fmt.Errorf("%d error(s): nothing was rendered", len(se.Problems))
	}
	return stacks, err
}

// printJoin joins the stacks and prints the sections, findings to errw.
func printJoin(out, errw io.Writer, stacks []join.Stack, only string) error {
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
		rs, err := prepare.Load(strings.Split(arg, ",")) // the stack's defaults filled in, as plan sees it
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
	var fromServer bool
	var o join.ApplyOptions
	cmd := &cobra.Command{
		Use:   "apply [flags] SECTION [FILE...]",
		Short: "Write one rendering to the helper that serves it (only dns so far)",
		Long: `apply makes one helper's generated file equal to what the join renders for it. Only the dns section is built:

  tink join apply dns platform/platform.yaml canary/canary.yaml
  tink join apply dns --from-server                   # from the server's own state, with no files: what the daemon helper will run

It writes /hosts on the dns helper, which CoreDNS's hosts plugin answers from first and re-reads every few seconds, so a change needs no
restart. That file holds ONLY generated records; the platform's own static names (the sign-on portal, the directory's UI) belong in the zone
file, /db.<zone>, written by hand. The file is rewritten whole, so a name that is no longer published disappears. docs/join.md has the layout
and the reasons (CoreDNS accepts only one hosts plugin per server block, and where a name is in both files the generated one wins).

It refuses, and writes nothing, when the join has any error; when no published name is found (applying would empty the records: that
usually means the wrong files were given, or that nothing is stamped on the server yet; --allow-empty says it is meant); when the file exists and tink did not write it (it may hold hand-written
records; --adopt says to overwrite it, once); when a published name is already a record in the zone file (it would
take the platform's own name over); and when a read fails (a failed read is an error, never an empty file). After a write it reads the file back.

  --dry-run   compute and show what would change, write nothing

It acts through the Incus API with the certificate of whoever runs it (--remote works). The same code is meant to be run by the daemon helper
on a timer; nothing here is specific to the person running it.

With --from-server the join is built from the server (see "tink join --help") and no FILE is given; the files and --from-server are alternatives.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkApplySection(args[0]); err != nil {
				return err
			}
			if fromServer {
				if err := noFilesWithFromServer(args[1:]); err != nil {
					return err
				}
			} else if len(args) < 2 {
				return fmt.Errorf("apply needs SECTION and at least one FILE, or --from-server to join from the server instead")
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			if fromServer {
				return runJoinApplyFromServer(cmd.OutOrStdout(), cmd.ErrOrStderr(), server, incusFiles{server}, args[0], o)
			}
			return runJoinApply(cmd.OutOrStdout(), cmd.ErrOrStderr(), incusFiles{server}, args[0], args[1:], o)
		},
	}
	cmd.Flags().BoolVar(&fromServer, "from-server", false, "join from the instances, profiles, networks and publish stamps on the server instead of from FILEs")
	cmd.Flags().BoolVar(&o.DryRun, "dry-run", false, "show what would change and write nothing")
	cmd.Flags().BoolVar(&o.Adopt, "adopt", false, "overwrite a /hosts that tink did not write (once, when moving to generated records; move the platform's own names to the zone file first)")
	cmd.Flags().BoolVar(&o.AllowEmpty, "allow-empty", false, "accept a rendering with no published name (it empties the generated records)")
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	return cmd
}

// runJoinApply joins the stacks and applies one section. Findings go to errw, as for the print-only command.
func runJoinApply(out, errw io.Writer, files join.Files, section string, stackArgs []string, o join.ApplyOptions) error {
	if err := checkApplySection(section); err != nil {
		return err
	}
	stacks, err := loadJoinStacks(stackArgs)
	if err != nil {
		return err
	}
	return applyJoin(out, errw, files, stacks, o)
}

// runJoinApplyFromServer is runJoinApply with the join built from the server.
func runJoinApplyFromServer(out, errw io.Writer, server incus.InstanceServer, files join.Files, section string, o join.ApplyOptions) error {
	if err := checkApplySection(section); err != nil {
		return err
	}
	stacks, err := readServerStacks(errw, server)
	if err != nil {
		return err
	}
	return applyJoin(out, errw, files, stacks, o)
}

func checkApplySection(section string) error {
	switch {
	case section == "dns":
		return nil
	case slices.Contains(joinSections, section):
		return fmt.Errorf("%s cannot be applied yet: only dns is built (print the others with `tink join --only %s`)", section, section)
	default:
		return fmt.Errorf("%q is not a section: the sections are %s, and only dns can be applied", section, strings.Join(joinSections, ", "))
	}
}

// applyJoin joins the stacks and applies the dns section, the only one built (checkApplySection has already refused any other). Findings go to errw, as for
// the print-only command.
func applyJoin(out, errw io.Writer, files join.Files, stacks []join.Stack, o join.ApplyOptions) error {
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
