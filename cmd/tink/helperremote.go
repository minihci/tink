package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
)

func newHelperRemoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remote",
		Short: "Give the helper other Incus servers to copy backups to, by name",
		Long: `A backup target that names an Incus remote (remote: nas2) is copied to by connecting to that server, using the remote of that name in the Incus
client configuration of whoever runs the copy. For the helper that is the helper's own configuration, which has only its own host: so a copy to
another server fails, every time, until the helper is given the remote. These commands do that.

The name matters: it is the name the backup target's remote: uses, so give the helper the same name the stack does.

The helper adds a remote itself, from inside, with its own client certificate (the one the host already trusts): the other server is told to trust that
certificate, and can revoke it on its own ("incus config trust remove"). The helper's private key does not leave it.`,
	}
	cmd.AddCommand(newHelperRemoteAddCmd(), newHelperRemoteListCmd(), newHelperRemoteRemoveCmd())
	return cmd
}

func bindHelperSelector(cmd *cobra.Command, socket, project, name *string) {
	cmd.Flags().StringVar(socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(project, "helper-project", "", "the helper's project (default: wherever the helper is found)")
	cmd.Flags().StringVar(name, "helper-name", "", "the helper instance's name (default: the helper found)")
}

func newHelperRemoteAddCmd() *cobra.Command {
	var socket string
	var opts helper.RemoteAddOptions
	var tokenFile string
	cmd := &cobra.Command{
		Use:   "add NAME [ADDRESS]",
		Short: "Add an Incus server to the helper, and get the helper trusted by it",
		Long: `add makes the helper know the server NAME, by running "tink remote add" inside it: the server's certificate is verified before anything secret is
sent, and the server is told to trust the helper's client certificate. It takes what "tink remote add" takes:

  - a trust token, made ON THE SERVER BEING ADDED (incus config trust add helper), which carries the server's certificate fingerprint and its addresses, so
    ADDRESS can be left out. Give it with --token-file (- for standard input) or $TINK_REMOTE_TOKEN; it goes to the helper on standard input and is never put on a
    command line. It is single-use.
  - --fingerprint, the fingerprint you were given for the server;
  - --accept-certificate, to trust whatever the server presents on first contact. (There is no one to ask inside the helper.)

The server must be reachable from the helper (its network, not this machine's). A server that already trusts the helper's certificate needs no token.

The helper is the one that connects, so what it needs is the address the HELPER can reach; a name that only resolves on your machine will fail there.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.Remote = args[0]
			if len(args) == 2 {
				opts.Addr = args[1]
			}
			var err error
			if opts.Token, err = readToken(tokenFile, os.Getenv("TINK_REMOTE_TOKEN"), cmd.InOrStdin()); err != nil {
				return err
			}
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			return (&helper.Installer{Server: server, Out: cmd.OutOrStdout()}).AddRemote(opts)
		},
	}
	bindHelperSelector(cmd, &socket, &opts.Project, &opts.Name)
	cmd.Flags().StringVar(&tokenFile, "token-file", "", "file holding a trust token made on the server being added (- for standard input); or set $TINK_REMOTE_TOKEN")
	cmd.Flags().StringVar(&opts.Fingerprint, "fingerprint", "", "the server certificate's SHA-256 fingerprint to expect")
	cmd.Flags().BoolVar(&opts.AcceptCertificate, "accept-certificate", false, "trust the server's certificate on first contact, without verifying it")
	cmd.Flags().StringVar(&opts.RemoteProject, "project", "", "the project on that server the remote defaults to (default: chosen like incus remote add does)")
	return cmd
}

func newHelperRemoteListCmd() *cobra.Command {
	var socket, project, name string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the servers the helper can reach by name, as it last reported them",
		Long: `list reads the remotes out of the helper's status document, so it costs no exec and works when the helper has stopped. It is what the helper last
said, which is at most a heartbeat old.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			list, age, label, err := (&helper.Installer{Server: server}).Remotes(project, name, time.Now())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tADDRESS\tPROJECT")
			for _, r := range list {
				n := r.Name
				if n == helper.RemoteName {
					n += " (the helper's own host)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", n, r.Addr, orDash(r.Project))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "as %s reported them %s ago\n", label, roundAge(age))
			return nil
		},
	}
	bindHelperSelector(cmd, &socket, &project, &name)
	return cmd
}

func newHelperRemoteRemoveCmd() *cobra.Command {
	var socket, project, name string
	cmd := &cobra.Command{
		Use:   "remove NAME",
		Short: "Make the helper forget a server (which still trusts the helper's certificate until told otherwise)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			in := &helper.Installer{Server: server, Out: cmd.OutOrStdout()}
			if err := in.RemoveRemote(project, name, args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "the helper no longer knows %s\n", args[0])
			return nil
		},
	}
	bindHelperSelector(cmd, &socket, &project, &name)
	return cmd
}
