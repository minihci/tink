package main

import (
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/secrets"
)

// redactor scrubs every secret tink decrypts from anything it prints (best effort: see
// secrets.Redactor). The process-wide writers in main() go through it, and so does the final error.
var redactor = secrets.NewRedactor()

// stackSecretFlags are how plan and apply find the secret store and the identity that decrypts it.
type stackSecretFlags struct {
	store    string
	identity string
}

func (f *stackSecretFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.store, "secrets", "", "the secret store (default: secrets.yaml beside the first stack file)")
	cmd.Flags().StringVar(&f.identity, "identity", "", "age identity file that decrypts it (default: $TINK_AGE_IDENTITY, else ~/.config/tink/identity.txt)")
}

// expand replaces ${secret:NAME} references in resources. A stack with no references never touches
// the store or an identity. files are the stack files given on the command line, used only to
// find the store beside them.
func (f *stackSecretFlags) expand(resources []resolve.Resource, files []string) ([]resolve.Resource, error) {
	if !resolve.NeedsSecrets(resources) {
		return resources, nil
	}
	path := f.store
	if path == "" {
		dir := "."
		if len(files) > 0 {
			dir = filepath.Dir(files[0])
		}
		path = filepath.Join(dir, secrets.DefaultFile)
	}
	store, err := secrets.Open(path)
	if err != nil {
		return nil, err
	}
	return resolve.ExpandSecrets(resources, secrets.NewResolver(store, f.identity, redactor)), nil
}
