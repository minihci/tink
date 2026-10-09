package main

import (
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

func TestTheHeadscaleStackLoads(t *testing.T) {
	resources, err := resolve.LoadFiles([]string{"../../examples/headscale/headscale.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolve.Levels(resources); err != nil {
		t.Fatal(err)
	}
}
