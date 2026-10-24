package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

// The note is about a stack's stance: it appears for a stack with a bridge of its own that says nothing about egress, and for no other.
func TestNoteEgressSpeaksOnlyForAStackThatSaysNothing(t *testing.T) {
	for name, c := range map[string]struct {
		stack resolve.Resource
		want  bool
	}{
		"own bridge, nothing said":         {resolve.Resource{Kind: resolve.KindStack, Name: "home", Network: resolve.StackNetworkOwn}, true},
		"own bridge, egress: open":         {resolve.Resource{Kind: resolve.KindStack, Name: "home", Network: resolve.StackNetworkOwn, EgressMode: resolve.EgressOpen}, false},
		"own bridge, egress: none (meant)": {resolve.Resource{Kind: resolve.KindStack, Name: "home", Network: resolve.StackNetworkOwn, EgressMode: resolve.EgressNone}, false},
		"no bridge of its own":             {resolve.Resource{Kind: resolve.KindStack, Name: "home"}, false},
		"not a stack document":             {resolve.Resource{Kind: resolve.KindInstance, Name: "home", Network: resolve.StackNetworkOwn}, false},
	} {
		var out bytes.Buffer
		noteEgress(&out, []resolve.Resource{c.stack})
		got := out.String()
		if (got != "") != c.want {
			t.Errorf("%s: output = %q, want a note: %v", name, got, c.want)
		}
		if c.want && (!strings.Contains(got, `"home"`) || !strings.Contains(got, "egress: open") || !strings.Contains(got, "egress: none")) {
			t.Errorf("%s: the note must name the stack and both ways to answer it: %q", name, got)
		}
	}
}
