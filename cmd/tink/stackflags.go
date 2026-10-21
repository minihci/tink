package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/prepare"
)

// stackFlags are the flags every command that works on a stack shares. They are declared once, as persistent flags of `plan`, so
// `plan apply` (and the `plan explain` to come) inherit them rather than copy them.
type stackFlags struct {
	socket  string
	offline bool
	secrets stackSecretFlags
}

func (f *stackFlags) bind(cmd *cobra.Command) {
	pf := cmd.PersistentFlags()
	pf.StringVar(&f.socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	pf.BoolVar(&f.offline, "offline", false, "do not consult OCI registries; images that cannot then be verified are blocked under on_image_change: rebuild and only warned about otherwise")
	pf.StringVar(&f.secrets.store, "secrets", "", "the secret store (default: secrets.yaml beside the first stack file)")
	pf.StringVar(&f.secrets.identity, "identity", "", "age identity file that decrypts it (default: $TINK_AGE_IDENTITY, else ~/.config/tink/identity.txt)")
}

// prepareStack is what turns the stack files into a stack ready to plan or apply. A variable so a test can watch what each command asks for.
var prepareStack = prepare.Prepare

// prepare is the only way `plan` and `plan apply` get their stack: the passes and their order are internal/prepare's.
func (f *stackFlags) prepare(cmd *cobra.Command, files []string) (*prepare.Prepared, error) {
	return prepareStack(prepare.Request{
		Files:       files,
		Socket:      f.socket,
		SecretStore: f.secrets.store,
		Identity:    f.secrets.identity,
		Redactor:    redactor,
		Notice:      func(line string) { fmt.Fprintln(cmd.ErrOrStderr(), line) },
	})
}
