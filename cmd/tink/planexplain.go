package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/explain"
	"github.com/minihci/tink/internal/resolve"
)

// newPlanExplainCmd takes the flags it shares with `plan`, which declares them as persistent flags and so owns them.
func newPlanExplainCmd(flags *stackFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "explain [flags] [FILE...]",
		Short: "Show what the passes made of a stack, and which pass made each part",
		Long: fmt.Sprintf(`explain runs the passes that "tink plan" runs before it plans anything and says what each one made or
changed: the stack's defaults, the bridge, ACL and addresses of a stack that says "network: own", the ingress rules of an ACL
that says "derive_ingress", and the region of every file that says "derive:" (the proxy's routes, the sign-on helper's rules,
the dns helper's records, which are printed so that you can read them). It writes nothing, anywhere, and plans nothing.

The passes that work out the rest from the site read the server. With none to reach, explain still runs: what does not need
one is shown, and a pass that does says so.

It takes the same files and flags as "tink plan" (with no FILE it reads %s). A secret is never printed: the
secrets pass is a count, and a value whose key looks like a secret, or came from one, is shown as hidden.`, resolve.DefaultFile),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rec := &explain.Recorder{}
			req := flags.request(cmd, args)
			req.Trace = rec
			req.NoServerOK = true
			// Each pass's own lines are in the recorder and printed with its changes; only what belongs to no pass is said here.
			req.Notice = func(line string) {
				if strings.HasPrefix(line, "no server:") {
					fmt.Fprintln(cmd.ErrOrStderr(), line)
				}
			}
			stack, err := prepareStack(req)
			if err != nil {
				// what the passes before the one that stopped made is still worth reading
				if len(rec.Passes) > 0 && !rec.Empty() {
					fmt.Fprint(cmd.OutOrStdout(), redactor.Redact(rec.Render("")))
					fmt.Fprintln(cmd.OutOrStdout(), "\n(the passes above ran; the next one stopped, below)")
				}
				return err
			}
			name, _ := resolve.StackName(stack.Resources)
			fmt.Fprint(cmd.OutOrStdout(), redactor.Redact(rec.Render(name)))
			return nil
		},
	}
}
