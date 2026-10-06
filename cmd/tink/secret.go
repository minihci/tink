package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/minihci/tink/internal/secrets"
)

// secretFlags are shared by every `tink secret` subcommand.
type secretFlags struct {
	store    string
	identity string
}

func (f *secretFlags) bind(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.store, "store", secrets.DefaultFile, "the secret store file")
	cmd.PersistentFlags().StringVar(&f.identity, "identity", "", "age identity file (default: $TINK_AGE_IDENTITY, else ~/.config/tink/identity.txt)")
}

func (f *secretFlags) open() (*secrets.Store, error) { return secrets.Open(f.store) }

func loadIdentities(explicit string) ([]age.Identity, error) {
	return secrets.LoadIdentity(secrets.IdentityPath(explicit))
}

func newSecretCmd() *cobra.Command {
	var f secretFlags
	cmd := &cobra.Command{
		Use:   "secret",
		Short: "Manage the stack's encrypted secrets",
		Long: `Manage the stack's secrets: values encrypted with age and kept in secrets.yaml beside the
stack, safe to commit (docs/secrets-design.md). The private key (the identity) is never in the
repo.

Setting a value needs only the public keys in the store. Reading one needs the identity.

First time on a machine:

  tink secret keygen --add          make an identity, and add its public key to the store
  tink secret set immich-db-password

On a new host (so no private key ever travels): run keygen there, then on the machine where you
edit the repo, "tink secret recipients add <the public key it printed>", commit, and pull.`,
	}
	f.bind(cmd)
	cmd.AddCommand(
		newSecretKeygenCmd(&f), newSecretRecipientsCmd(&f), newSecretSetCmd(&f),
		newSecretListCmd(&f), newSecretRevealCmd(&f), newSecretRmCmd(&f),
	)
	return cmd
}

func newSecretKeygenCmd(f *secretFlags) *cobra.Command {
	var add bool
	cmd := &cobra.Command{
		Use:   "keygen",
		Short: "Create an age identity and print its public key",
		Long: `keygen creates an identity file (mode 0600) and prints its public key. It refuses to overwrite
an existing identity: replacing one would lock you out of everything encrypted to it.

--add also adds the new public key to the store as a recipient.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := secrets.IdentityPath(f.identity)
			pub, err := secrets.GenerateIdentity(path)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "identity: %s\npublic key: %s\n", path, pub)
			fmt.Fprintln(out, "Back this identity file up somewhere other than this machine and the repo: without it, the secrets cannot be read.")
			if !add {
				fmt.Fprintf(out, "Add it to a store with: tink secret recipients add %s\n", pub)
				return nil
			}
			s, err := f.open()
			if err != nil {
				return err
			}
			ids, _ := secrets.LoadIdentity(path)
			if err := s.SetRecipients(append(s.Recipients(), pub), ids); err != nil {
				return err
			}
			if err := s.Save(); err != nil {
				return err
			}
			fmt.Fprintf(out, "added to %s\n", s.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&add, "add", false, "also add the public key to the store as a recipient")
	return cmd
}

func newSecretRecipientsCmd(f *secretFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recipients",
		Short: "List, add or remove the public keys values are encrypted to",
		Long: `recipients lists the store's public keys. "add" and "rm" change the list and re-encrypt every
value, which needs an identity that can decrypt them.

Removing a recipient does not take back what they could already read: anything they could decrypt
before, they can still decrypt from the repository's history. Rotate the secrets as well.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.open()
			if err != nil {
				return err
			}
			mine := map[string]bool{}
			if ids, err := secrets.LoadIdentity(secrets.IdentityPath(f.identity)); err == nil {
				for _, k := range secrets.PublicKeys(ids) {
					mine[k] = true
				}
			}
			for _, r := range s.Recipients() {
				note := ""
				if mine[r] {
					note = "   (this machine's identity)"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s%s\n", r, note)
			}
			return nil
		},
	}
	change := func(use, short string, mutate func(current []string, key string) ([]string, error)) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				s, err := f.open()
				if err != nil {
					return err
				}
				next, err := mutate(s.Recipients(), args[0])
				if err != nil {
					return err
				}
				var ids []age.Identity
				if len(s.Names()) > 0 {
					if ids, err = loadIdentities(f.identity); err != nil {
						return err
					}
				}
				if err := s.SetRecipients(next, ids); err != nil {
					return err
				}
				if err := s.Save(); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s now has %d recipient(s); %d secret(s) re-encrypted\n", s.Path(), len(next), len(s.Names()))
				return nil
			},
		}
	}
	cmd.AddCommand(
		change("add KEY", "Add a recipient and re-encrypt", func(cur []string, key string) ([]string, error) {
			for _, c := range cur {
				if c == key {
					return nil, fmt.Errorf("%s is already a recipient", key)
				}
			}
			return append(cur, key), nil
		}),
		change("rm KEY", "Remove a recipient and re-encrypt", func(cur []string, key string) ([]string, error) {
			var out []string
			for _, c := range cur {
				if c != key {
					out = append(out, c)
				}
			}
			if len(out) == len(cur) {
				return nil, fmt.Errorf("%s is not a recipient", key)
			}
			return out, nil
		}),
	)
	return cmd
}

func newSecretSetCmd(f *secretFlags) *cobra.Command {
	var generate bool
	var length int
	var charset string
	cmd := &cobra.Command{
		Use:   "set NAME",
		Short: "Set a secret from stdin (or a prompt), or generate one",
		Long: `set encrypts a value to the store's recipients and writes it to the store. The value is read
from stdin (a prompt, with echo off, when stdin is a terminal) and never from a flag: a flag lands
in shell history and the process list.

--generate makes a random value instead, and does not print it. Setting an existing secret
replaces it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.open()
			if err != nil {
				return err
			}
			var value string
			if generate {
				if value, err = secrets.Generate(length, charset); err != nil {
					return err
				}
			} else if value, err = readSecretValue(cmd.InOrStdin(), os.Stderr); err != nil {
				return err
			}
			existed := s.Has(args[0])
			if err := s.Set(args[0], value); err != nil {
				return err
			}
			if err := s.Save(); err != nil {
				return err
			}
			verb := "set"
			if existed {
				verb = "replaced"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s in %s\n", verb, args[0], s.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&generate, "generate", false, "generate a random value instead of reading one")
	cmd.Flags().IntVar(&length, "length", 0, "length of a generated value (default 32)")
	cmd.Flags().StringVar(&charset, "charset", "", "alnum (default) or hex, for a generated value")
	return cmd
}

func newSecretListCmd(f *secretFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the secrets in the store (names only)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.open()
			if err != nil {
				return err
			}
			for _, n := range s.Names() {
				fmt.Fprintln(cmd.OutOrStdout(), n)
			}
			return nil
		},
	}
}

func newSecretRevealCmd(f *secretFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "reveal NAME",
		Short: "Print a secret's value (deliberate; for recovery)",
		Long: `reveal decrypts a secret and prints it. Nothing in tink needs this: stacks use ${secret:NAME}.
It exists for recovery, and is a separate, plainly named command so it is never done by accident.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.open()
			if err != nil {
				return err
			}
			ids, err := loadIdentities(f.identity)
			if err != nil {
				return err
			}
			v, err := s.Get(args[0], ids)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "revealing %s\n", args[0])
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}
}

func newSecretRmCmd(f *secretFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "rm NAME",
		Short: "Remove a secret from the store",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.open()
			if err != nil {
				return err
			}
			if !s.Has(args[0]) {
				return fmt.Errorf("secret %q is not in %s", args[0], s.Path())
			}
			s.Remove(args[0])
			if err := s.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s from %s (it stays in git history; rotate it if it was ever used)\n", args[0], s.Path())
			return nil
		},
	}
}

// readSecretValue reads a value from a terminal (twice, echo off) or, when stdin is not one, from
// stdin, dropping a single trailing newline.
func readSecretValue(in io.Reader, prompts io.Writer) (string, error) {
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(prompts, "value: ")
		a, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompts)
		if err != nil {
			return "", err
		}
		fmt.Fprint(prompts, "again: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(prompts)
		if err != nil {
			return "", err
		}
		if string(a) != string(b) {
			return "", errors.New("the two entries differ")
		}
		return string(a), nil
	}
	data, err := io.ReadAll(bufio.NewReader(in))
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), nil
}
