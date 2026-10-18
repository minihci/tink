package main

import (
	"testing"

	"github.com/spf13/cobra"
)

// --socket is for the user who has Incus somewhere unusual. Every command's default must be empty, which is Incus's own resolution
// ($INCUS_SOCKET, then $INCUS_DIR, then /run/incus/unix.socket if it exists, else /var/lib/incus/unix.socket). A command that fills in a
// path of its own (run, ingress and deploy once did) ignores $INCUS_SOCKET and cannot find a daemon whose package keeps its socket under /run.
func TestEverySocketFlagDefaultsToIncusOwnResolution(t *testing.T) {
	seen := 0
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if f := c.Flags().Lookup("socket"); f != nil {
			seen++
			if f.DefValue != "" {
				t.Errorf("%s --socket defaults to %q: leave it empty so Incus resolves the socket", c.CommandPath(), f.DefValue)
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd())
	if seen == 0 {
		t.Fatal("found no --socket flag at all: the walk is not reaching the commands")
	}
}
