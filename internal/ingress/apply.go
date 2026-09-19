package ingress

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// Diff reports which generated route files would be added, removed, or
// changed to turn what's currently on disk into the desired set.
type Diff struct {
	Added   []string
	Removed []string
	Changed []string
}

// Empty reports whether applying this diff would be a no-op -- the common
// case on every poll but the first one after a real registration change.
func (d Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0
}

// readCurrent reads every *.caddy file in dir into a filename->content map.
func readCurrent(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}

	current := map[string]string{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".caddy" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		current[entry.Name()] = string(content)
	}
	return current, nil
}

func computeDiff(current, desired map[string]string) Diff {
	var d Diff
	for name, content := range desired {
		old, existed := current[name]
		switch {
		case !existed:
			d.Added = append(d.Added, name)
		case old != content:
			d.Changed = append(d.Changed, name)
		}
	}
	for name := range current {
		if _, stillWanted := desired[name]; !stillWanted {
			d.Removed = append(d.Removed, name)
		}
	}
	sort.Strings(d.Added)
	sort.Strings(d.Removed)
	sort.Strings(d.Changed)
	return d
}

// apply performs a full rebuild of dir's *.caddy files to match desired --
// not an incremental patch, so a deregistered or deleted instance's stale
// route actually goes away instead of accumulating -- then gracefully
// reloads Caddy. Deliberate difference from reconcile.sh, called out per
// this package's own doc comment: reconcile.sh and this port's own first
// version both shelled out to the incus CLI (exec.Command("incus", "exec",
// ...)) for this, a "scope beyond what this first port needs" this
// function's own comment used to say -- that scope arrived for real the
// moment resolve's kind: exec needed the exact same guest-side exec
// primitive for the exact same reason (a USB device's driver bind, this
// same Caddy reload), so incusapi.ExecInGuest now lives in the shared
// package both callers already depend on rather than staying duplicated
// or shelled out to a binary that has to be on PATH.
func apply(server incus.InstanceServer, dir, ingressInstance string, desired map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", dir, err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".caddy" {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return fmt.Errorf("removing stale %s: %w", entry.Name(), err)
			}
		}
	}

	for name, content := range desired {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
	}

	code, output, err := incusapi.ExecInGuest(server, ingressInstance, []string{"caddy", "reload", "--config", "/etc/caddy/Caddyfile"})
	if err != nil {
		return fmt.Errorf("reloading caddy: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("reloading caddy: exit %d: %s", code, output)
	}
	return nil
}
