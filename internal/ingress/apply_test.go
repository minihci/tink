package ingress

import "testing"

func TestComputeDiff_NoChange(t *testing.T) {
	current := map[string]string{"a.caddy": "same"}
	desired := map[string]string{"a.caddy": "same"}

	d := computeDiff(current, desired)
	if !d.Empty() {
		t.Fatalf("expected no diff, got %+v", d)
	}
}

func TestComputeDiff_AddedRemovedChanged(t *testing.T) {
	current := map[string]string{
		"stale.caddy":   "old route",
		"changed.caddy": "old content",
	}
	desired := map[string]string{
		"new.caddy":     "new route",
		"changed.caddy": "new content",
	}

	d := computeDiff(current, desired)
	if len(d.Added) != 1 || d.Added[0] != "new.caddy" {
		t.Fatalf("expected new.caddy added, got %+v", d)
	}
	if len(d.Removed) != 1 || d.Removed[0] != "stale.caddy" {
		t.Fatalf("expected stale.caddy removed, got %+v", d)
	}
	if len(d.Changed) != 1 || d.Changed[0] != "changed.caddy" {
		t.Fatalf("expected changed.caddy changed, got %+v", d)
	}
	if d.Empty() {
		t.Fatal("diff with changes should not report Empty")
	}
}
