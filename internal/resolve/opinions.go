package resolve

import (
	"fmt"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/opinion"
)

// validateAccept checks an `accept:` block: each key names an opinion that applies to this kind, and each reason says something.
func validateAccept(r Resource) error {
	for name, reason := range r.Accept {
		switch {
		case !opinion.Known(name):
			return fmt.Errorf("resource %q: accept: %q is not an opinion tink has (they are: %s)", r.Name, name, strings.Join(opinion.Names(), ", "))
		case name == string(opinion.Backup):
			return fmt.Errorf("resource %q: accept: the backup opinion is answered in the volume's own backup block (backup: none: REASON), not here", r.Name)
		case name == string(opinion.Storage) && r.Kind != KindStorageVolume:
			return fmt.Errorf("resource %q: accept: storage applies to a storage-volume, not a %s", r.Name, r.Kind)
		case name == string(opinion.Identity) && r.Kind != KindInstance:
			return fmt.Errorf("resource %q: accept: identity applies to an instance, not a %s", r.Name, r.Kind)
		case strings.TrimSpace(reason) == "":
			return fmt.Errorf("resource %q: accept: %s needs a reason (an empty one is the same as not saying)", r.Name, name)
		}
	}
	return nil
}

// Opinions judges every resource in the stack against every opinion that applies to it. It reads the server only for the driver of each
// pool a volume names. Nothing is written, and a departure is a finding and not an error.
func Opinions(server incus.InstanceServer, resources []Resource) ([]opinion.Finding, error) {
	targets := map[string]Resource{}
	for _, r := range resources {
		if r.Kind == KindBackupTarget {
			targets[r.Name] = r
		}
	}
	drivers := map[string]string{}
	driverOf := func(pool string) (string, error) {
		if d, ok := drivers[pool]; ok {
			return d, nil
		}
		p, _, err := server.GetStoragePool(pool)
		switch {
		case err == nil:
			drivers[pool] = p.Driver
		case incusapi.IsNotFound(err):
			drivers[pool] = "" // a pool that is not there: the finding says its driver could not be read
		default:
			return "", fmt.Errorf("reading storage pool %q: %w", pool, err)
		}
		return drivers[pool], nil
	}

	var out []opinion.Finding
	for _, r := range resources {
		ref := fmt.Sprintf("%s/%s", r.Kind, r.Name)
		switch r.Kind {
		case KindStorageVolume:
			pool := r.Pool
			if pool == "" {
				pool = "default"
			}
			driver, err := driverOf(pool)
			if err != nil {
				return nil, err
			}
			out = append(out, opinion.CheckStorage(ref, pool, driver, r.Accept[string(opinion.Storage)]))
			out = append(out, backupFinding(ref, r, targets))
		case KindInstance:
			if f, applies := opinion.CheckIdentity(ref, r.Config, r.Accept[string(opinion.Identity)]); applies {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// backupFinding is the backup opinion read off what plan already knows: an unanswered volume and one that misses 3-2-1 depart, and an
// explicit none: carries its reason.
func backupFinding(ref string, r Resource, targets map[string]Resource) opinion.Finding {
	f := opinion.Finding{Opinion: opinion.Backup, Resource: ref}
	switch {
	case r.Backup == nil:
		f.State, f.Message = opinion.Departs, "it does not say how it is backed up: add backup: with snapshots and copies, or backup: none: REASON"
	case r.Backup.None != "":
		f.State, f.Reason = opinion.Accepted, r.Backup.None
	default:
		if w := backupWarnings(r, targets); len(w) > 0 {
			f.State, f.Message = opinion.Departs, strings.Join(w, "; ")
		} else {
			f.State = opinion.Met
		}
	}
	return f
}

// Summary counts findings by opinion and state, in the order opinion.All lists them.
type Summary struct {
	Opinion                   opinion.Opinion
	Met, Accepted, Departs, N int
}

func Summarise(findings []opinion.Finding) []Summary {
	by := map[opinion.Name]*Summary{}
	for _, o := range opinion.All {
		by[o.Name] = &Summary{Opinion: o}
	}
	for _, f := range findings {
		s := by[f.Opinion]
		s.N++
		switch f.State {
		case opinion.Met:
			s.Met++
		case opinion.Accepted:
			s.Accepted++
		default:
			s.Departs++
		}
	}
	var out []Summary
	for _, o := range opinion.All {
		out = append(out, *by[o.Name])
	}
	return out
}
