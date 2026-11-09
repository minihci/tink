package main

import (
	"bytes"
	"testing"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/resolve"
)

// What a plan prints for one resource is: the change lines, then drift, then what blocks, then warnings, then notes, each group in the order its findings
// were raised. The order is part of the output, so a reader's eye and a diff of two plans keep working; the findings themselves are one list.
func TestPrintPlanned_OrderIsChangesDriftBlockedWarningsNotes(t *testing.T) {
	var out bytes.Buffer
	printPlanned(&out, resolve.PlannedResource{
		Resource: resolve.Resource{Kind: resolve.KindInstance, Name: "web"},
		Action:   resolve.ActionBlocked,
		Changes:  []string{"config.a: 1 -> 2", "device eth0: added"},
		// raised in an order that is not the printed one: the printer groups them
		Findings: []finding.Finding{
			finding.Notef("a note", "", "note one"),
			finding.Warnf("a warning", "", "warning one"),
			finding.Errorf("a blocker", "", "first blocker"),
			finding.Warnf(resolve.ReasonImageDrift, "", "image: alias \"x\" now resolves to aaa, but the instance was built from bbb"),
			finding.Warnf("another warning", "", "warning two"),
			finding.Errorf("another blocker", "", "second blocker"),
			finding.Notef("another note", "", "note two"),
			finding.Warnf("a warning", "", "warning three"),
		},
	})
	want := "  instance/web: BLOCKED\n" +
		"      config.a: 1 -> 2\n" +
		"      device eth0: added\n" +
		"      drift: image: alias \"x\" now resolves to aaa, but the instance was built from bbb\n" +
		"      blocked: first blocker\n" +
		"      blocked: second blocker\n" +
		"      warning: warning one\n" +
		"      warning: warning two\n" +
		"      warning: warning three\n" +
		"      note: note one\n" +
		"      note: note two\n"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
}
