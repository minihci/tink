package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/minihci/tink/internal/incusconf"
	"github.com/minihci/tink/internal/remote"
)

// newRemoteCmd is `tink remote`: it manages the Incus client configuration entries that --remote uses, so a
// machine without the Incus client installed can be set up. It edits the same files `incus remote` does.
func newRemoteCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "remote",
		Short: "Add, list and remove the Incus servers --remote can manage",
		Long: `remote manages the entries of the Incus client configuration (the file the incus CLI keeps; $INCUS_CONF
overrides where) that tink's --remote refers to. It writes the same files incus does, so the two are
interchangeable, and a machine with no Incus client installed can be set up with tink alone.

TLS only: a server that also offers OIDC is still added with TLS (incus remote add would pick OIDC, an interactive
browser login).`,
	}
	c.AddCommand(newRemoteAddCmd(), newRemoteListCmd(), newRemoteRemoveCmd())
	return c
}

func newRemoteAddCmd() *cobra.Command {
	var tokenFile, fingerprint, project string
	var accept bool
	cmd := &cobra.Command{
		Use:   "add NAME [ADDRESS]",
		Short: "Add a TLS remote, and get this machine trusted by it",
		Long: `add stores a remote and makes the server trust this machine's client certificate (generated if there is none).

The server's certificate is verified BEFORE anything secret is sent to it, one of three ways:
  - a trust token (incus config trust add NAME, on the server) carries the server's certificate fingerprint, and its addresses,
    so ADDRESS can be left out;
  - --fingerprint FP, the fingerprint you were given for the server;
  - --accept-certificate, which trusts whatever the server presents on first contact (or, at a terminal, you are shown the
    fingerprint and asked).
The server's certificate is then stored, and every later connection is pinned to it.

The trust token is read from --token-file (use - for standard input) or $TINK_REMOTE_TOKEN, never from the command line, where
it would sit in shell history and the process list. A server that already trusts this machine's certificate needs no token.

Nothing is saved unless the whole thing succeeds.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			conf, err := cliconfig.LoadConfig("")
			if err != nil {
				return fmt.Errorf("loading the Incus client configuration: %w", err)
			}
			opts := remote.AddOptions{Name: args[0], Fingerprint: fingerprint, AcceptCertificate: accept, Project: project, Out: cmd.ErrOrStderr()}
			if len(args) == 2 {
				opts.Addr = args[1]
			}
			if opts.Token, err = readToken(tokenFile, os.Getenv("TINK_REMOTE_TOKEN"), cmd.InOrStdin()); err != nil {
				return err
			}
			if term.IsTerminal(int(os.Stdin.Fd())) && tokenFile != "-" {
				opts.Confirm = confirmFingerprint(cmd.InOrStdin(), cmd.ErrOrStderr())
			}
			res, err := remote.Add(conf, opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added remote %s: %s (server certificate %s, verified by %s; this machine is trusted", opts.Name, res.Addr, res.Fingerprint, res.Verified)
			if res.Project != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "; project %s", res.Project)
			}
			fmt.Fprintln(cmd.OutOrStdout(), ")")
			fmt.Fprintf(cmd.OutOrStdout(), "use it with: tink --remote %s plan FILE\n", opts.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding a trust token (- for standard input); or set $TINK_REMOTE_TOKEN")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "the server certificate's SHA-256 fingerprint to expect")
	cmd.Flags().BoolVar(&accept, "accept-certificate", false, "trust the server's certificate on first contact, without verifying it")
	cmd.Flags().StringVar(&project, "project", "", "the project this remote defaults to (default: chosen like incus remote add does)")
	return cmd
}

func newRemoteListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the remotes in the Incus client configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			conf, err := cliconfig.LoadConfig("")
			if err != nil {
				return fmt.Errorf("loading the Incus client configuration: %w", err)
			}
			builtin := make([]string, 0, len(incusconf.Builtin))
			for n := range incusconf.Builtin {
				builtin = append(builtin, n)
			}
			sort.Strings(builtin)
			remote.List(conf, builtin, cmd.OutOrStdout())
			return nil
		},
	}
}

func newRemoteRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a remote and its stored server certificate (the server keeps trusting this machine until told otherwise)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			conf, err := cliconfig.LoadConfig("")
			if err != nil {
				return fmt.Errorf("loading the Incus client configuration: %w", err)
			}
			if err := remote.Remove(conf, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed remote %s\n", args[0])
			fmt.Fprintln(cmd.ErrOrStderr(), "the server still trusts this machine's certificate: remove it there with `incus config trust remove`")
			return nil
		},
	}
}

// readToken finds the trust token, if any: a file (or standard input for "-"), else the environment. The
// token is never taken from the command line.
func readToken(file, env string, stdin io.Reader) (string, error) {
	var raw string
	switch {
	case file == "-":
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading the token from standard input: %w", err)
		}
		raw = string(b)
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading the token file: %w", err)
		}
		raw = string(b)
	default:
		raw = env
	}
	tok := strings.TrimSpace(raw)
	if file != "" && tok == "" {
		return "", errors.New("the token file is empty")
	}
	return tok, nil
}

// confirmFingerprint asks, at a terminal, whether to trust a server certificate; "y" or the fingerprint itself
// accepts, so a fingerprint copied from the server can be pasted.
func confirmFingerprint(in io.Reader, out io.Writer) func(string) (bool, error) {
	return func(fingerprint string) (bool, error) {
		fmt.Fprintf(out, "Server certificate fingerprint: %s\nTrust it? (y/n/[fingerprint]) ", fingerprint)
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		line = strings.TrimSpace(line)
		return strings.EqualFold(line, "y") || strings.EqualFold(line, "yes") || strings.EqualFold(line, fingerprint), nil
	}
}
