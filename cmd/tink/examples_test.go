package main

import (
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

// The example stacks are documentation that has to keep loading: a field that is renamed or a kind that changes fails here, not in a reader's hands.
func TestTheExampleStacksLoad(t *testing.T) {
	for _, files := range [][]string{
		{"../../examples/edge/edge.yaml", "../../examples/edge/apps.yaml"},
		{"../../examples/opinions/demo.yaml"},
	} {
		resources, err := resolve.LoadFiles(files)
		if err != nil {
			t.Fatalf("%v: %v", files, err)
		}
		if _, err := resolve.Levels(resources); err != nil {
			t.Errorf("%v: %v", files, err)
		}
	}
}
