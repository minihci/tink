package volbackup

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// Target is where copies of a volume go: a storage pool on THIS server (a second disk, or the
// Incus `truenas` driver). Remote Incus servers are not supported yet.
type Target struct {
	Name   string // the kind: backup-target's name
	Pool   string
	Remote string
}

// TargetFrom builds a Target from a kind: backup-target resource.
func TargetFrom(r resolve.Resource) Target {
	return Target{Name: r.Name, Pool: r.Pool, Remote: r.Remote}
}

// RestorePoint is one backup of a volume on a target: a volume of its own.
type RestorePoint struct {
	Volume   string
	At       time.Time
	Snapshot string // the source snapshot it was copied from
}

// CopyOptions control Copy.
type CopyOptions struct {
	// Retain is how long restore points are kept ("30d"); older ones are pruned, always keeping the newest.
	Retain   string
	DryRun   bool
	Now      func() time.Time
	Progress io.Writer
}

// CopyResult says what a run did.
type CopyResult struct {
	Snapshot string   // the source snapshot that was copied (removed again afterwards)
	Volume   string   // the new restore point on the target
	Pruned   []string // restore points removed for being older than Retain
	Planned  []string // with DryRun: what would have happened
}

// Why a new volume per run instead of refreshing one target volume, as `incus storage volume copy
// --refresh` would: a refresh makes the target MIRROR the source's snapshots. Tested on two
// TrueNAS-backed pools: when the source pruned a snapshot, the next refresh deleted it from the target
// too, even with --refresh-exclude-older, and refreshing from a snapshot deleted the target's own
// snapshots. A mirror cannot keep a longer history than its source, and it propagates a deletion (or
// damage) on the source into the backup. So each run copies a consistent source snapshot into a NEW,
// separately named volume (`<volume>-bk-<UTC time>`): history is independent of the source, and nothing
// that happens to the source can reach an existing restore point. The price is a full copy per run.

// Copy backs v up to t: it takes a snapshot of the source (a consistent point in time), copies that
// snapshot into a new restore-point volume on the target, removes the temporary snapshot, stamps the
// source, and prunes restore points older than opts.Retain.
func Copy(server incus.InstanceServer, v Volume, t Target, opts CopyOptions) (res CopyResult, err error) {
	now := nowOr(opts.Now)
	s := v.scoped(server)
	say := func(format string, args ...any) {
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, format+"\n", args...)
		}
	}

	if t.Remote != "" {
		return res, fmt.Errorf("target %q is a remote Incus server (%s); only pool targets can be copied to so far", t.Name, t.Remote)
	}
	if t.Pool == "" {
		return res, fmt.Errorf("target %q has no pool", t.Name)
	}
	if t.Pool == v.pool() {
		return res, fmt.Errorf("target %q is pool %q, the volume's own pool: that is the same failure domain, not a copy", t.Name, t.Pool)
	}
	if _, _, err := s.GetStoragePool(t.Pool); err != nil {
		return res, fmt.Errorf("target %q: pool %q: %w", t.Name, t.Pool, err)
	}
	src, _, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return res, fmt.Errorf("volume %s/%s: %w", v.pool(), v.Name, err)
	}

	start := now()
	res.Snapshot = "tink-copy-" + stamped(start)
	res.Volume = v.Name + "-bk-" + stamped(start)
	if volumeExists(s, t.Pool, res.Volume) {
		return res, fmt.Errorf("%s/%s already exists (a second run in the same second?)", t.Pool, res.Volume)
	}
	copyOf := resolve.CopyOf(v.Project, v.pool(), v.Name)

	if opts.DryRun {
		res.Planned = []string{
			fmt.Sprintf("snapshot %s/%s@%s", v.pool(), v.Name, res.Snapshot),
			fmt.Sprintf("copy it to %s/%s", t.Pool, res.Volume),
			fmt.Sprintf("remove the temporary snapshot, and stamp %s", resolve.CopyStampAt(t.Name)),
		}
		if opts.Retain != "" {
			pruned, perr := prune(s, v, t, opts.Retain, start, "", true)
			if perr != nil {
				return res, perr
			}
			for _, p := range pruned {
				res.Planned = append(res.Planned, fmt.Sprintf("prune restore point %s/%s (older than %s)", t.Pool, p, opts.Retain))
			}
		}
		return res, nil
	}

	// The temporary snapshot is removed on every path, and carries an expiry as a safety net should tink die.
	say("snapshotting %s/%s as %s", v.pool(), v.Name, res.Snapshot)
	safety := start.Add(24 * time.Hour)
	op, err := s.CreateStoragePoolVolumeSnapshot(v.pool(), "custom", v.Name, api.StorageVolumeSnapshotsPost{Name: res.Snapshot, ExpiresAt: &safety})
	if err != nil {
		return res, fmt.Errorf("snapshotting %s/%s: %w", v.pool(), v.Name, err)
	}
	if err := op.Wait(); err != nil {
		return res, fmt.Errorf("snapshotting %s/%s: %w", v.pool(), v.Name, err)
	}
	defer func() {
		if derr := removeSnapshot(s, v, res.Snapshot); derr != nil {
			leftover := fmt.Errorf("could not remove the temporary snapshot %s/%s@%s (it expires on its own in 24h): %w", v.pool(), v.Name, res.Snapshot, derr)
			if err == nil {
				err = leftover
			} else {
				err = fmt.Errorf("%w (and %v)", err, leftover)
			}
		}
	}()

	say("copying it to %s/%s", t.Pool, res.Volume)
	from := api.StorageVolume{
		Name: v.Name + "/" + res.Snapshot, Type: "custom", ContentType: src.ContentType,
		StorageVolumePut: api.StorageVolumePut{Config: map[string]string{
			resolve.MarkerCopyOf:     copyOf,
			resolve.MarkerCopyAt:     start.UTC().Format(time.RFC3339),
			resolve.MarkerCopyTarget: t.Name,
			resolve.MarkerCopySnap:   res.Snapshot,
		}},
	}
	cop, cerr := s.CopyStoragePoolVolume(t.Pool, s, v.pool(), from, &incus.StoragePoolVolumeCopyArgs{Name: res.Volume})
	if cerr == nil {
		cerr = cop.Wait()
	}
	if cerr != nil {
		// the name is unique to this run, so anything under it is ours: do not leave a half-made restore point
		if volumeExists(s, t.Pool, res.Volume) {
			_ = s.DeleteStoragePoolVolume(t.Pool, "custom", res.Volume)
		}
		return res, fmt.Errorf("copying %s/%s@%s to %s/%s: %w", v.pool(), v.Name, res.Snapshot, t.Pool, res.Volume, cerr)
	}
	if err := ensureMarkers(s, t.Pool, res.Volume, from.Config); err != nil {
		return res, err
	}

	if err := stampCopy(s, v, t.Name, res.Volume, start); err != nil {
		return res, err
	}
	if opts.Retain != "" {
		say("pruning restore points older than %s", opts.Retain)
		if res.Pruned, err = prune(s, v, t, opts.Retain, start, res.Volume, false); err != nil {
			return res, fmt.Errorf("the copy succeeded, but pruning failed: %w", err)
		}
	}
	return res, nil
}

// ensureMarkers makes sure the restore point carries its markers: Incus may not apply the config
// given on a copy from a snapshot, and the markers are what lets pruning and restore trust the volume.
func ensureMarkers(s incus.InstanceServer, pool, name string, want map[string]string) error {
	vol, etag, err := s.GetStoragePoolVolume(pool, "custom", name)
	if err != nil {
		return fmt.Errorf("reading the new restore point %s/%s: %w", pool, name, err)
	}
	put := vol.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	for k, val := range want {
		put.Config[k] = val
	}
	if err := s.UpdateStoragePoolVolume(pool, "custom", name, put, etag); err != nil {
		return fmt.Errorf("marking the new restore point %s/%s: %w", pool, name, err)
	}
	return nil
}

func stampCopy(s incus.InstanceServer, v Volume, target, restorePoint string, at time.Time) error {
	vol, etag, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return fmt.Errorf("reading %s/%s to record the copy: %w", v.pool(), v.Name, err)
	}
	put := vol.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	put.Config[resolve.CopyStampAt(target)] = at.UTC().Format(time.RFC3339)
	put.Config[resolve.CopyStampVolume(target)] = restorePoint
	if err := s.UpdateStoragePoolVolume(v.pool(), "custom", v.Name, put, etag); err != nil {
		return fmt.Errorf("recording the copy on %s/%s: %w", v.pool(), v.Name, err)
	}
	return nil
}

func removeSnapshot(s incus.InstanceServer, v Volume, name string) error {
	op, err := s.DeleteStoragePoolVolumeSnapshot(v.pool(), "custom", v.Name, name)
	if err != nil {
		return err
	}
	return op.Wait()
}

// ListRestorePoints returns the restore points of v on t, newest first. Only volumes carrying the
// marker for exactly this volume count: a volume tink did not make, or one made for another volume, is
// never listed, and so is never pruned or restored from.
func ListRestorePoints(s incus.InstanceServer, v Volume, t Target) ([]RestorePoint, error) {
	vols, err := s.GetStoragePoolVolumes(t.Pool)
	if err != nil {
		return nil, fmt.Errorf("listing volumes in pool %q: %w", t.Pool, err)
	}
	return restorePointsOf(vols, resolve.CopyOf(v.Project, v.pool(), v.Name)), nil
}

func restorePointsOf(vols []api.StorageVolume, copyOf string) []RestorePoint {
	var out []RestorePoint
	for _, vol := range vols {
		if vol.Type != "custom" || vol.Config[resolve.MarkerCopyOf] != copyOf {
			continue
		}
		at, err := time.Parse(time.RFC3339, vol.Config[resolve.MarkerCopyAt])
		if err != nil {
			continue // a marker we cannot read: leave the volume alone rather than guess its age
		}
		out = append(out, RestorePoint{Volume: vol.Name, At: at, Snapshot: vol.Config[resolve.MarkerCopySnap]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// prune removes restore points older than retain, never the newest and never `keep` (the one just made).
func prune(s incus.InstanceServer, v Volume, t Target, retain string, now time.Time, keep string, dryRun bool) ([]string, error) {
	points, err := ListRestorePoints(s, v, t)
	if err != nil {
		return nil, err
	}
	victims, err := expired(points, retain, now, keep)
	if err != nil {
		return nil, err
	}
	if dryRun {
		return victims, nil
	}
	var done []string
	for _, name := range victims {
		if err := s.DeleteStoragePoolVolume(t.Pool, "custom", name); err != nil {
			return done, fmt.Errorf("pruning %s/%s: %w", t.Pool, name, err)
		}
		done = append(done, name)
	}
	return done, nil
}

// expired chooses which restore points to prune: those older than retain, except the newest, and
// except `keep`. points must be newest first. Pure, so the rule that protects the backups is tested.
func expired(points []RestorePoint, retain string, now time.Time, keep string) ([]string, error) {
	var out []string
	for i, p := range points {
		if i == 0 || p.Volume == keep {
			continue
		}
		cutoff, err := resolve.ExpiryAfter(p.At, retain)
		if err != nil {
			return nil, err
		}
		if cutoff.Before(now) {
			out = append(out, p.Volume)
		}
	}
	return out, nil
}

// pickRestorePoint chooses the restore point to use: the one named (by volume name, or by the UTC
// stamp in it), or the newest.
func pickRestorePoint(points []RestorePoint, wanted string) (RestorePoint, error) {
	if len(points) == 0 {
		return RestorePoint{}, fmt.Errorf("there are no restore points on the target (has `tink backup run` copied to it yet?)")
	}
	if wanted == "" {
		return points[0], nil
	}
	names := make([]string, 0, len(points))
	for _, p := range points {
		names = append(names, p.Volume)
		if p.Volume == wanted || strings.HasSuffix(p.Volume, "-bk-"+wanted) {
			return p, nil
		}
	}
	return RestorePoint{}, fmt.Errorf("no restore point %q on the target (have: %s)", wanted, strings.Join(names, ", "))
}

// copyFromTarget materialises a restore point as a new volume in the volume's own pool. The new volume
// is scrubbed of the copy markers, so it can never be mistaken for a restore point.
func copyFromTarget(s incus.InstanceServer, v Volume, t Target, rp RestorePoint, newName string) error {
	src, _, err := s.GetStoragePoolVolume(t.Pool, "custom", rp.Volume)
	if err != nil {
		return fmt.Errorf("restore point %s/%s: %w", t.Pool, rp.Volume, err)
	}
	from := api.StorageVolume{Name: rp.Volume, Type: "custom", ContentType: src.ContentType}
	op, err := s.CopyStoragePoolVolume(v.pool(), s, t.Pool, from, &incus.StoragePoolVolumeCopyArgs{Name: newName})
	if err == nil {
		err = op.Wait()
	}
	if err != nil {
		return fmt.Errorf("copying %s/%s to %s/%s: %w", t.Pool, rp.Volume, v.pool(), newName, err)
	}
	return scrubMarkers(s, v.pool(), newName)
}

// scrubMarkers removes the copy markers from a volume made from a restore point.
func scrubMarkers(s incus.InstanceServer, pool, name string) error {
	vol, etag, err := s.GetStoragePoolVolume(pool, "custom", name)
	if err != nil {
		return err
	}
	put := vol.Writable()
	changed := false
	for k := range put.Config {
		if strings.HasPrefix(k, "user.tink.backup.copy-") {
			delete(put.Config, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.UpdateStoragePoolVolume(pool, "custom", name, put, etag)
}

// LiveConfig returns the volume's live config, where tink keeps its stamps.
func LiveConfig(server incus.InstanceServer, v Volume) (map[string]string, error) {
	vol, _, err := v.scoped(server).GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return nil, fmt.Errorf("volume %s/%s: %w", v.pool(), v.Name, err)
	}
	return vol.Config, nil
}
