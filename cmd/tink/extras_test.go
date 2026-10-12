package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

func TestPrintPlannedShowsWhatIsNotInTheStack(t *testing.T) {
	var out bytes.Buffer
	inst := resolve.Resource{Kind: resolve.KindInstance, Name: "ha"}
	printPlanned(&out, resolve.PlannedResource{Resource: inst, Action: resolve.ActionNone,
		Extras: &resolve.Extras{Devices: []string{"lan (nic)"}, Config: []string{"limits.cpu"}}})
	got := out.String()
	if !strings.Contains(got, "  instance/ha: no changes\n") ||
		!strings.Contains(got, "      not in the stack (left alone): device.lan (nic), config.limits.cpu\n") {
		t.Errorf("got %q", got)
	}
	out.Reset()
	printPlanned(&out, resolve.PlannedResource{Resource: inst, Action: resolve.ActionNone, Extras: &resolve.Extras{}})
	printPlanned(&out, resolve.PlannedResource{Resource: inst, Action: resolve.ActionNone})
	if strings.Contains(out.String(), "not in the stack") {
		t.Errorf("an instance with nothing extra, or one not asked about, gets no line: %q", out.String())
	}
}

func TestNoteExtras(t *testing.T) {
	var out bytes.Buffer
	noteExtras(&out, 0)
	if out.Len() != 0 {
		t.Errorf("silent when there are none, got %q", out.String())
	}
	noteExtras(&out, 1)
	if !strings.Contains(out.String(), "1 instance carries") || !strings.Contains(out.String(), "plan changes nothing") {
		t.Errorf("got %q", out.String())
	}
	out.Reset()
	noteExtras(&out, 3)
	if !strings.Contains(out.String(), "3 instances carry") {
		t.Errorf("got %q", out.String())
	}
}
