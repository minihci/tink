package main

import (
	"bytes"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

// A file left for the next apply is neither "no changes" nor BLOCKED in a plan, and its warning follows the label.
func TestPrintPlanned_ADeferredFile(t *testing.T) {
	var out bytes.Buffer
	printPlanned(&out, resolve.PlannedResource{
		Resource: resolve.Resource{Kind: resolve.KindFile, Name: "playlist"},
		Action:   resolve.ActionDeferred,
		Warnings: []string{"not written, because instance ha is not running (its status is Stopped) and /media/list.txt is at or under /media, a mounted host directory (device media)"},
	})
	want := "  file/playlist: DEFERRED\n      warning: not written, because instance ha is not running (its status is Stopped) and /media/list.txt is at or under /media, a mounted host directory (device media)\n"
	if out.String() != want {
		t.Errorf("got  %q\nwant %q", out.String(), want)
	}
}
