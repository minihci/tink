package resolve

import (
	"fmt"
	"strings"

	"github.com/minihci/tink/internal/remote"
)

// Where a backup target lives relative to the volumes copied to it. Tink cannot
// verify these claims; they are the user's declaration, and the 3-2-1 check is
// only as honest as they are.
const (
	LocationSameHost  = "same-host"  // another pool or disk on this server
	LocationOtherHost = "other-host" // another machine, same site
	LocationOffsite   = "offsite"    // another site
)

// EngineIncus replicates with Incus itself (`storage volume copy --refresh`),
// to another Incus server (remote) or another storage pool on this one (pool).
const EngineIncus = "incus"

// validateBackupTarget rejects a malformed `kind: backup-target` at load time.
func validateBackupTarget(r Resource) error {
	if r.Kind != KindBackupTarget {
		return nil
	}
	switch r.Location {
	case LocationSameHost, LocationOtherHost, LocationOffsite:
	case "":
		return fmt.Errorf("resource %q: backup-target needs location: %s, %s or %s -- it is how the 3-2-1 check knows which failure domain this is",
			r.Name, LocationSameHost, LocationOtherHost, LocationOffsite)
	default:
		return fmt.Errorf("resource %q: location must be %q, %q or %q, got %q",
			r.Name, LocationSameHost, LocationOtherHost, LocationOffsite, r.Location)
	}
	switch r.Engine {
	case EngineIncus:
	case "":
		return fmt.Errorf("resource %q: backup-target needs engine (only %q is supported so far)", r.Name, EngineIncus)
	default:
		return fmt.Errorf("resource %q: engine %q is not supported yet (only %q)", r.Name, r.Engine, EngineIncus)
	}
	if r.Remote == "" && r.Pool == "" {
		return fmt.Errorf("resource %q: engine incus needs remote (another Incus server) or pool (another storage pool on this server)", r.Name)
	}
	if strings.ContainsAny(r.Remote, ":/ ") {
		return fmt.Errorf("resource %q: remote must be a bare Incus remote name, got %q", r.Name, r.Remote)
	}
	if strings.ContainsAny(r.Pool, "/ ") {
		return fmt.Errorf("resource %q: pool must be a bare storage pool name, got %q", r.Name, r.Pool)
	}
	return validateDeclaredRemote(r)
}

// validateDeclaredRemote checks the opt-in description of a target's remote (address and fingerprint). Both or neither: an address with no
// fingerprint would invite trusting whatever answers on it the first time, which is what the fingerprint is there to prevent.
func validateDeclaredRemote(r Resource) error {
	if r.Address == "" && r.Fingerprint == "" {
		return nil
	}
	if r.Remote == "" {
		return fmt.Errorf("resource %q: address and fingerprint describe the Incus server that remote: names, and this target has no remote (it copies to a pool on this server)", r.Name)
	}
	if r.Address == "" {
		return fmt.Errorf("resource %q: fingerprint needs address: say where %q is, or leave both out and let each machine that runs the copy have a remote of that name", r.Name, r.Remote)
	}
	if r.Fingerprint == "" {
		return fmt.Errorf("resource %q: address needs fingerprint (the SHA-256 of the server's certificate), so nobody has to trust %q on first use", r.Name, r.Address)
	}
	if _, err := remote.NormalizeAddr(r.Address); err != nil {
		return fmt.Errorf("resource %q: %w", r.Name, err)
	}
	if _, err := remote.NormalizeFingerprint(r.Fingerprint); err != nil {
		return fmt.Errorf("resource %q: %w", r.Name, err)
	}
	return nil
}

// DeclaredRemote returns the address and certificate fingerprint a backup-target declares for its remote, in the forms `tink remote add` and
// `tink helper remote add` take (an https URL with a port; lower-case hex), and whether it declares them. A target that does not is the
// default: only the name is written down, and each machine that runs the copy has to have a remote of that name.
func (r Resource) DeclaredRemote() (address, fingerprint string, ok bool) {
	if r.Remote == "" || r.Address == "" || r.Fingerprint == "" {
		return "", "", false
	}
	a, err := remote.NormalizeAddr(r.Address)
	if err != nil {
		return "", "", false
	}
	f, err := remote.NormalizeFingerprint(r.Fingerprint)
	if err != nil {
		return "", "", false
	}
	return a, f, true
}

// validateBackupReferences checks that every copy names a backup-target that is
// in the same set of resources. It has to be a hard error: the graph builder
// ignores references to names it does not know, so a typo would silently drop
// a backup leg and the 3-2-1 check would be judging a stack nobody wrote.
func validateBackupReferences(resources []Resource) error {
	targets := backupTargets(resources)
	for _, r := range resources {
		if r.Kind != KindStorageVolume || r.Backup == nil {
			continue
		}
		for _, c := range r.Backup.Copies {
			if _, ok := targets[c.Target]; !ok {
				return fmt.Errorf("resource %q: backup.copies names target %q, which is not a kind: backup-target in this stack", r.Name, c.Target)
			}
		}
	}
	return nil
}

// backupTargets indexes the backup-target resources by name.
func backupTargets(resources []Resource) map[string]Resource {
	targets := map[string]Resource{}
	for _, r := range resources {
		if r.Kind == KindBackupTarget {
			targets[r.Name] = r
		}
	}
	return targets
}

// failureDomain names what a copy shares fate with. The live volume's domain is
// its own pool on this server; a target's is its remote server (and pool), or
// the other pool on this server. Two things in the same domain die together.
func failureDomain(remote, pool string) string {
	if pool == "" && remote == "" {
		pool = "default"
	}
	if remote != "" {
		if pool == "" {
			pool = "default" // a remote target's unset pool is its default pool
		}
		return "remote:" + remote + "/" + pool
	}
	return "local:" + pool
}

func volumeDomain(v Resource) string { return failureDomain("", v.Pool) }

func targetDomain(t Resource) string { return failureDomain(t.Remote, t.Pool) }

// backupWarnings is everything `plan` has to say about a volume's backup
// beyond what decideVolume already does: whether the declared copies add up to
// 3-2-1, and a note that declared copies are not executed yet.
//
// Rules (docs/volume-backup-design.md): 3 copies (the live volume plus two
// copies; snapshots on the live pool are a rollback aid and do not count), in at
// least 2 distinct failure domains none of which is the live volume's own, at
// least one of them off-site.
//
// A volume with no backup block (already warned about elsewhere) or an explicit
// `none` is not evaluated.
func backupWarnings(v Resource, targets map[string]Resource) []string {
	if v.Kind != KindStorageVolume || v.Backup == nil || v.Backup.None != "" {
		return nil
	}

	var warnings []string
	var counted []Resource
	for _, c := range v.Backup.Copies {
		t, ok := targets[c.Target]
		if !ok {
			// Unreachable from a loaded stack (validateBackupReferences rejects it), so this is the
			// planner being handed the wrong set of targets. Say so rather than judge 3-2-1 on a
			// stack with legs missing: a silent skip here once reported "1 of 3" for a complete stack.
			warnings = append(warnings, fmt.Sprintf(
				"copy target %q is not known to the planner, so 3-2-1 cannot be judged (this is a tink bug)", c.Target))
			continue
		}
		if targetDomain(t) == volumeDomain(v) {
			warnings = append(warnings, fmt.Sprintf(
				"copy to %q is on the live volume's own pool (%s), so it is not a separate failure domain and is not counted",
				t.Name, volumeDomain(v)))
			continue
		}
		counted = append(counted, t)
	}

	domains := map[string]bool{}
	offsite := false
	for _, t := range counted {
		domains[targetDomain(t)] = true
		offsite = offsite || t.Location == LocationOffsite
	}

	var missing []string
	switch {
	case len(counted) < 2:
		if len(counted) == 1 {
			missing = append(missing, "1 more copy in another failure domain")
		} else {
			missing = append(missing, "2 copies in other failure domains")
		}
	case len(domains) < 2:
		missing = append(missing, "the copies share one failure domain; put them on different hosts or pools")
	}
	if !offsite {
		missing = append(missing, "an off-site copy (no copy's target is declared location: offsite)")
	}
	if len(missing) > 0 {
		have := 1 + len(counted)
		if have > 3 {
			have = 3
		}
		warnings = append(warnings, fmt.Sprintf(
			"3-2-1 not met (%d of 3 copies; snapshots on the live volume's pool do not count). Missing: %s",
			have, strings.Join(missing, "; ")))
	}

	return warnings
}
