package main

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// What matters end to end: a secret that tink has decrypted must not reach the terminal by ANY route
// a command can take: stdout, stderr, cobra's own error line, or the error main prints at exit.
func TestExecuteScrubsDecryptedSecretsFromEveryOutputPath(t *testing.T) {
	const secret = "s3cr3t-value-that-leaks"
	redactor.Add(secret)

	root := &cobra.Command{Use: "tink", SilenceUsage: true}
	root.AddCommand(&cobra.Command{
		Use: "leaky",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "to stdout: %s\n", secret)
			fmt.Fprintf(cmd.ErrOrStderr(), "to stderr: %s\n", secret)
			fmt.Fprintf(cmd.OutOrStdout(), "a final line with no newline: %s", secret)
			// what an Incus driver does: echo a credential back inside an error
			return errors.New("driver said: --api-key " + secret + " failed")
		},
	})
	root.SetArgs([]string{"leaky"})

	var out, errw bytes.Buffer
	err := execute(root, &out, &errw)
	if err == nil {
		t.Fatal("the command's error must still be returned")
	}
	for name, got := range map[string]string{"stdout": out.String(), "stderr": errw.String()} {
		if strings.Contains(got, secret) {
			t.Errorf("%s leaked the secret:\n%s", name, got)
		}
		if !strings.Contains(got, "***") {
			t.Errorf("%s should show the redaction marker:\n%s", name, got)
		}
	}
	if !strings.Contains(out.String(), "a final line with no newline: ***") {
		t.Errorf("a last line without a newline must be flushed (and redacted), got %q", out.String())
	}
	if !strings.Contains(errw.String(), "driver said: --api-key *** failed") {
		t.Errorf("the final error must be printed, redacted, got %q", errw.String())
	}
}
