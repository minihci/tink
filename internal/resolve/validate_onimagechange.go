package resolve

import (
	"fmt"
	"strings"
)

// validateOnImageChange rejects on_image_change configurations that could not be
// carried out safely, at load time -- before anything touches Incus.
func validateOnImageChange(r Resource) error {
	switch r.OnImageChange {
	case "", OnImageChangeReport, OnImageChangeIgnore, OnImageChangeRebuild:
	default:
		return fmt.Errorf("resource %q: on_image_change must be %q, %q or %q, got %q", r.Name, OnImageChangeReport, OnImageChangeIgnore, OnImageChangeRebuild, r.OnImageChange)
	}
	if r.SnapshotVolumes && r.OnImageChange != OnImageChangeRebuild {
		return fmt.Errorf("resource %q: snapshot_volumes only applies with on_image_change: rebuild", r.Name)
	}
	if r.OnImageChange != OnImageChangeRebuild {
		return nil
	}
	if r.VM {
		return fmt.Errorf("resource %q: on_image_change: rebuild does not support VMs -- a rebuild replaces the root disk, which for a VM is the whole operating system and its data", r.Name)
	}
	if r.Image == "" {
		return fmt.Errorf("resource %q: on_image_change: rebuild requires an image", r.Name)
	}
	if !strings.Contains(r.Image, "@sha256:") {
		return fmt.Errorf("resource %q: on_image_change: rebuild requires a digest-pinned image (remote:repo:tag@sha256:...), got %q -- a tag can move, a digest cannot", r.Name, r.Image)
	}
	for k := range r.Config {
		if strings.HasPrefix(k, "snapshots.") {
			return fmt.Errorf("resource %q: on_image_change: rebuild cannot be combined with instance-level %s -- Incus refuses to rebuild an instance that has snapshots; snapshot its data volumes instead", r.Name, k)
		}
	}
	return nil
}
