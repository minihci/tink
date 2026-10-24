package main

import (
	"fmt"
	"io"

	"github.com/minihci/tink/internal/resolve"
)

// noteEgress ends `plan` and `plan apply` with one note when the stack asks for a bridge of its own and says nothing about egress. The default is none, so its apps
// cannot open outbound connections; the bridge's DNS still answers, so a name resolves and the connection is then refused, which looks like a broken app. The note is
// about the stack's stance and not about any app's need, so it needs no knowledge of the app, and writing the field either way, open or none, silences it (#225).
func noteEgress(out io.Writer, resources []resolve.Resource) {
	for _, r := range resources {
		if r.Kind != resolve.KindStack || r.Network != resolve.StackNetworkOwn || r.EgressMode != "" {
			continue
		}
		fmt.Fprintf(out, "note: the stack %q has network: %s and says nothing about egress, so its apps cannot open outbound connections (a name still resolves, and the connection is then refused).\n"+
			"      Write `egress: %s` if one needs to, or `egress: %s` to say it is meant.\n", r.Name, resolve.StackNetworkOwn, resolve.EgressOpen, resolve.EgressNone)
	}
}
