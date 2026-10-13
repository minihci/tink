package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/identity"
	"github.com/minihci/tink/internal/identity/lldap"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/mesh/headscale"
	"github.com/minihci/tink/internal/secrets"
)

func newIdentityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "identity",
		Short: "Read the people and groups in the site's directory",
	}
	cmd.AddCommand(newIdentityGroupsCmd())
	return cmd
}

// identityGroupsOptions are what runIdentityGroups needs besides the reader; the rest of the flags only build the reader.
type identityGroupsOptions struct {
	Format       string   // table, json or headscale
	Groups       []string // restrict to these groups (headscale: the groups to emit)
	MemberFormat string   // headscale only
	Check        bool     // headscale only: run the checks, print no fragment
}

func newIdentityGroupsCmd() *cobra.Command {
	var (
		instance, project, user, passwordSecret, socket string
		o                                               identityGroupsOptions
		sf                                              stackSecretFlags
	)
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "Read the directory's groups and their members",
		Long: `groups reads the people and groups from the site's directory (LLDAP) and prints them, or projects them into the "groups"
object of a Headscale policy.

It does not connect to the directory over the network. It runs a small fixed script inside the LLDAP instance through the Incus API (so it
works from anywhere that can reach the server, including --remote), logs in with a READ-ONLY directory user (a member of lldap_strict_readonly
is enough, and cannot write) and runs one query. The password is a tink secret and travels on standard input only. The image needs curl and
jq, which the upstream LLDAP image has.

  tink identity groups --instance lldap --project id-lab --user reader --password-secret directory-reader
  tink identity groups ... --format headscale --group family --group admins

--format headscale prints a huJSON FRAGMENT for you to merge into the policy, never a whole policy (a policy with no "acls" or "grants" is
allow-all in Headscale). Findings go to standard error; an error means no fragment is printed and the exit status is 1. A failed read is an
error, never an empty list. See docs/identity.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if instance == "" || user == "" || passwordSecret == "" {
				return fmt.Errorf("--instance, --user and --password-secret are required")
			}
			if err := checkIdentityGroupsOptions(o); err != nil {
				return err
			}
			pw, err := identitySecret(sf, passwordSecret)
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			scoped := server
			if project != "" {
				scoped = server.UseProject(project)
			}
			rd := lldap.Reader{
				Instance: instance,
				User:     user,
				Password: pw,
				Exec: func(command []string, stdin io.Reader, env map[string]string) (int, string, error) {
					return incusapi.ExecWithStdin(scoped, instance, command, stdin, env)
				},
			}
			return runIdentityGroups(cmd.OutOrStdout(), cmd.ErrOrStderr(), rd, o)
		},
	}
	cmd.Flags().StringVar(&instance, "instance", "", "the directory's Incus instance (required)")
	cmd.Flags().StringVar(&project, "project", "", "the instance's Incus project (default: the connection's)")
	cmd.Flags().StringVar(&user, "user", "", "a read-only directory user (required)")
	cmd.Flags().StringVar(&passwordSecret, "password-secret", "", "the tink secret that holds that user's password (required)")
	cmd.Flags().StringVar(&o.Format, "format", "table", "table, json or headscale")
	cmd.Flags().StringArrayVar(&o.Groups, "group", nil, "only this group (repeatable); with --format headscale, the groups to emit (default: every group that is not the directory's own)")
	cmd.Flags().StringVar(&o.MemberFormat, "member-format", "", "headscale: how a person is written in the policy, {id} and {email} replaced (default "+headscale.DefaultMemberFormat+")")
	cmd.Flags().BoolVar(&o.Check, "check", false, "headscale: run the checks and print no fragment; the exit status is the answer")
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&sf.store, "secrets", "", "the secret store (default: "+secrets.DefaultFile+" in the current directory)")
	cmd.Flags().StringVar(&sf.identity, "identity", "", "age identity file that decrypts it (default: $TINK_AGE_IDENTITY, else ~/.config/tink/identity.txt)")
	return cmd
}

// identitySecret reads one secret from the store, registering it with the redactor so it cannot be printed by accident.
func identitySecret(sf stackSecretFlags, name string) (string, error) {
	path := sf.store
	if path == "" {
		path = secrets.DefaultFile
	}
	store, err := secrets.Open(path)
	if err != nil {
		return "", err
	}
	res := secrets.NewResolver(store, sf.identity, redactor)
	if !res.Has(name) {
		return "", fmt.Errorf("the secret %q is not in %s", name, res.Where())
	}
	return res.Get(name)
}

func checkIdentityGroupsOptions(o identityGroupsOptions) error {
	switch o.Format {
	case "table", "json", "headscale":
	default:
		return fmt.Errorf("--format must be table, json or headscale, not %q", o.Format)
	}
	if o.Format != "headscale" && (o.Check || o.MemberFormat != "") {
		return fmt.Errorf("--check and --member-format apply to --format headscale only")
	}
	return nil
}

// runIdentityGroups reads the directory and writes the answer. It is separate from the command so it can be tested without Incus.
func runIdentityGroups(out, errw io.Writer, rd identity.Reader, o identityGroupsOptions) error {
	if err := checkIdentityGroupsOptions(o); err != nil {
		return err
	}
	snap, err := rd.Read()
	if err != nil {
		return err
	}

	if o.Format == "headscale" {
		res := headscale.Project(snap, headscale.Options{Groups: o.Groups, MemberFormat: o.MemberFormat})
		for _, f := range res.Findings {
			fmt.Fprintln(errw, f)
		}
		if res.HasErrors() {
			return fmt.Errorf("the projection has errors, so no fragment was produced")
		}
		if !o.Check {
			fmt.Fprint(out, res.Text)
		}
		return nil
	}

	groups, err := selectGroups(snap, o.Groups)
	if err != nil {
		return err
	}
	if o.Format == "json" {
		type person struct {
			ID          string `json:"id"`
			Email       string `json:"email"`
			DisplayName string `json:"displayName"`
		}
		type group struct {
			Name    string   `json:"name"`
			System  bool     `json:"system"`
			Members []person `json:"members"`
		}
		doc := struct {
			Groups []group `json:"groups"`
		}{Groups: []group{}}
		for _, g := range groups {
			jg := group{Name: g.Name, System: g.System, Members: []person{}}
			for _, p := range g.Members {
				jg.Members = append(jg.Members, person{p.ID, p.Email, p.DisplayName})
			}
			doc.Groups = append(doc.Groups, jg)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(doc)
	}

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "GROUP\tMEMBERS")
	for _, g := range groups {
		name := g.Name
		if g.System {
			name += " (system)"
		}
		ids := make([]string, len(g.Members))
		for i, p := range g.Members {
			ids[i] = p.ID
		}
		members := strings.Join(ids, ", ")
		if members == "" {
			members = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\n", name, members)
	}
	return tw.Flush()
}

// selectGroups returns the named groups (in name order), or every group when none is named. A name the directory does not have is an error.
func selectGroups(s identity.Snapshot, names []string) ([]identity.Group, error) {
	s = s.Normalize()
	if len(names) == 0 {
		return s.Groups, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		if _, ok := s.Group(n); !ok {
			return nil, fmt.Errorf("group %q is not in the directory", n)
		}
		want[n] = true
	}
	var out []identity.Group
	for _, g := range s.Groups {
		if want[g.Name] {
			out = append(out, g)
		}
	}
	return out, nil
}
