package volbackup

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

// Target is where copies of a volume go: a storage pool on THIS server (a second disk, or the
// Incus `truenas` driver), or one on a remote Incus server named by Remote.
type Target struct {
	Name   string // the kind: backup-target's name
	Pool   string
	Remote string
	// Address and Fingerprint are what the stack declared for the remote's server, when it opted in (see backuprun.TargetFrom). They
	// are only used to say how to add a remote that is missing: connecting always goes by Remote's name. A target read from a volume's
	// copy policy has neither, because the policy does not carry them.
	Address     string
	Fingerprint string
}

// RestorePoint is one backup of a volume on a target: a volume of its own.
type RestorePoint struct {
	Volume   string
	At       time.Time
	Snapshot string // the source snapshot it was copied from
	Server   string // the server the source volume lived on when it was made; empty for points made before this was recorded
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
	// Swept are abandoned in-progress copies removed (see PartialGrace).
	Swept []string
	// OtherServers names the other servers that have restore points of a volume with this name on the target. They
	// are left alone: only the server that made a restore point prunes it.
	OtherServers []string
	Planned      []string // with DryRun: what would have happened
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
func Copy(server incus.InstanceServer, v Volume, t Target, opts CopyOptions) (CopyResult, error) {
	// One copy of a volume to a target at a time in this process. Not a failure: nothing was tried.
	key := copyKey(v, t)
	if _, busy := running.LoadOrStore(key, struct{}{}); busy {
		return CopyResult{}, fmt.Errorf("%s: %w", key, ErrBusy)
	}
	defer running.Delete(key)

	res, err := copyTo(server, v, t, opts)
	if err == nil || opts.DryRun || errors.Is(err, errPrune) {
		return res, err
	}
	// The copy itself failed: leave a mark on the volume, so a scheduler backs off instead of retrying every tick
	// and `plan` can say the copy is failing. A copy that succeeded but could not prune is not a failed copy.
	if serr := recordFailure(server, v, t.Name, nowOr(opts.Now)()); serr != nil {
		err = fmt.Errorf("%w (and the failure could not be recorded on the volume: %v)", err, serr)
	}
	return res, err
}

// errPrune marks the one failure after a successful copy: the restore point exists and the source is stamped,
// but older restore points could not be removed.
var errPrune = errors.New("pruning failed")

func copyTo(server incus.InstanceServer, v Volume, t Target, opts CopyOptions) (res CopyResult, err error) {
	now := nowOr(opts.Now)
	s := v.scoped(server)
	say := func(format string, args ...any) {
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, format+"\n", args...)
		}
	}

	if t.Remote == "" {
		if t.Pool == "" {
			return res, fmt.Errorf("target %q has no pool", t.Name)
		}
		if t.Pool == v.pool() {
			return res, fmt.Errorf("target %q is pool %q, the volume's own pool: that is the same failure domain, not a copy", t.Name, t.Pool)
		}
	}
	dst, err := t.dest(server, v)
	if err != nil {
		return res, err
	}
	if _, _, err := dst.GetStoragePool(t.pool()); err != nil {
		return res, fmt.Errorf("target %q: pool %q: %w", t.Name, t.where(), err)
	}
	src, _, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return res, fmt.Errorf("volume %s/%s: %w", v.pool(), v.Name, err)
	}

	me, err := serverName(server)
	if err != nil {
		return res, err
	}

	start := now()
	res.Snapshot = "tink-copy-" + stamped(start)
	res.Volume = v.Name + "-bk-" + stamped(start)
	exists, err := volumeExists(dst, t.pool(), res.Volume)
	if err != nil {
		return res, fmt.Errorf("checking whether %s/%s already exists: %w", t.where(), res.Volume, err)
	}
	if exists {
		return res, fmt.Errorf("%s/%s already exists (a second run in the same second?)", t.where(), res.Volume)
	}
	copyOf := backupmeta.CopyOf(v.Project, v.pool(), v.Name)

	if opts.DryRun {
		res.Planned = []string{
			fmt.Sprintf("snapshot %s/%s@%s", v.pool(), v.Name, res.Snapshot),
			fmt.Sprintf("copy it to %s/%s", t.where(), res.Volume),
			fmt.Sprintf("remove the temporary snapshot, and stamp %s", backupmeta.CopyStampAt(t.Name)),
		}
		if opts.Retain != "" {
			pruned, others, perr := prune(dst, v, t, opts.Retain, start, "", true, me)
			if perr != nil {
				return res, perr
			}
			res.OtherServers = others
			for _, p := range pruned {
				res.Planned = append(res.Planned, fmt.Sprintf("prune restore point %s/%s (older than %s)", t.where(), p, opts.Retain))
			}
		}
		swept, serr := sweepPartials(dst, v, t, start, "", true, me)
		if serr != nil {
			return res, serr
		}
		for _, p := range swept {
			res.Planned = append(res.Planned, fmt.Sprintf("remove abandoned partial copy %s/%s (started more than %d days ago and never finished)", t.where(), p, int(PartialGrace/(24*time.Hour))))
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

	say("copying it to %s/%s", t.where(), res.Volume)
	// The restore point's markers are applied only AFTER the copy has completed. Until then the new volume carries an
	// in-progress mark instead (a different key), so a copy that is cut off part way (a tunnel that drops, a full
	// disk, tink killed) is never listed as a restore point, so it can never be restored from, verified, or counted
	// as the newest backup, and yet tink can recognise it later and remove it (sweepPartials).
	inProgress := map[string]string{
		backupmeta.MarkerPartialOf:  copyOf,
		backupmeta.MarkerPartialAt:  start.UTC().Format(time.RFC3339),
		backupmeta.MarkerCopyTarget: t.Name,
		backupmeta.MarkerCopyServer: me,
	}
	markers := map[string]string{
		backupmeta.MarkerCopyOf:     copyOf,
		backupmeta.MarkerCopyAt:     start.UTC().Format(time.RFC3339),
		backupmeta.MarkerCopyTarget: t.Name,
		backupmeta.MarkerCopySnap:   res.Snapshot,
		backupmeta.MarkerCopyServer: me,
	}
	from := api.StorageVolume{Name: v.Name + "/" + res.Snapshot, Type: "custom", ContentType: src.ContentType,
		StorageVolumePut: api.StorageVolumePut{Config: inProgress}}
	cop, cerr := dst.CopyStoragePoolVolume(t.pool(), s, v.pool(), from, &incus.StoragePoolVolumeCopyArgs{Name: res.Volume, Mode: t.transferMode()})
	if cerr == nil {
		cerr = cop.Wait()
	}
	if cerr != nil {
		// the name is unique to this run, so anything under it is ours: do not leave a half-made restore point
		note := ""
		if left, lerr := volumeExists(dst, t.pool(), res.Volume); lerr != nil {
			note = fmt.Sprintf(" (and could not check whether a partial volume %s/%s was left: %v; tink will never use it, and removes it itself once it is %d days old if the target kept its in-progress mark, otherwise delete it by hand)", t.where(), res.Volume, lerr, int(PartialGrace/(24*time.Hour)))
		} else if left {
			if derr := dst.DeleteStoragePoolVolume(t.pool(), "custom", res.Volume); derr != nil {
				note = fmt.Sprintf(" (and the partial volume %s/%s could not be removed: %v; tink will never use it, and removes it itself once it is %d days old if the target kept its in-progress mark, otherwise delete it by hand)", t.where(), res.Volume, derr, int(PartialGrace/(24*time.Hour)))
			}
		}
		return res, fmt.Errorf("copying %s/%s@%s to %s/%s: %w%s", v.pool(), v.Name, res.Snapshot, t.where(), res.Volume, cerr, note)
	}
	// PolicyKey describes the volume this was copied FROM; a restore point must not carry it, or a scheduler that lists
	// volumes would copy the copy.
	if err := ensureMarkers(dst, t.pool(), res.Volume, markers, []string{backupmeta.MarkerPartialOf, backupmeta.MarkerPartialAt, backupmeta.PolicyKey}); err != nil {
		return res, err
	}

	if err := stampCopy(s, v, t.Name, res.Volume, start); err != nil {
		return res, err
	}
	// Tidy up after older runs: restore points past their retention, and copies another run started and never finished.
	// A failure here is not a failed copy (the restore point exists and the source is stamped), so it is reported as
	// its own kind of error.
	var tidy []error
	if opts.Retain != "" {
		say("pruning restore points older than %s", opts.Retain)
		var perr error
		if res.Pruned, res.OtherServers, perr = prune(dst, v, t, opts.Retain, start, res.Volume, false, me); perr != nil {
			tidy = append(tidy, perr)
		}
	}
	var serr error
	if res.Swept, serr = sweepPartials(dst, v, t, start, res.Volume, false, me); serr != nil {
		tidy = append(tidy, serr)
	}
	if len(tidy) > 0 {
		return res, fmt.Errorf("the copy succeeded, but %w: %w", errPrune, errors.Join(tidy...))
	}
	return res, nil
}

// ensureMarkers makes sure the restore point carries its markers: Incus may not apply the config
// given on a copy from a snapshot, and the markers are what lets pruning and restore trust the volume.
func ensureMarkers(s incus.InstanceServer, pool, name string, want map[string]string, remove []string) error {
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
	for _, k := range remove {
		delete(put.Config, k)
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
	put.Config[backupmeta.CopyStampAt(target)] = at.UTC().Format(time.RFC3339)
	put.Config[backupmeta.CopyStampVolume(target)] = restorePoint
	// a success ends any run of failures
	delete(put.Config, backupmeta.CopyFailAt(target))
	delete(put.Config, backupmeta.CopyFailCount(target))
	if err := s.UpdateStoragePoolVolume(v.pool(), "custom", v.Name, put, etag); err != nil {
		return fmt.Errorf("recording the copy on %s/%s: %w", v.pool(), v.Name, err)
	}
	return nil
}

// recordFailure notes on the source volume that the copy to target just failed: when, and how many attempts in
// a row (one more than the run of failures already recorded, if any). No message is stored.
func recordFailure(server incus.InstanceServer, v Volume, target string, at time.Time) error {
	s := v.scoped(server)
	vol, etag, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return fmt.Errorf("reading %s/%s: %w", v.pool(), v.Name, err)
	}
	n := 1
	if prior, failing := backupmeta.FailureOf(vol.Config, target); failing {
		n = prior.N + 1
	}
	put := vol.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	put.Config[backupmeta.CopyFailAt(target)] = at.UTC().Format(time.RFC3339)
	put.Config[backupmeta.CopyFailCount(target)] = fmt.Sprintf("%d", n)
	if err := s.UpdateStoragePoolVolume(v.pool(), "custom", v.Name, put, etag); err != nil {
		return fmt.Errorf("recording the failure on %s/%s: %w", v.pool(), v.Name, err)
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
	dst, err := t.dest(s, v)
	if err != nil {
		return nil, err
	}
	return listPoints(dst, v, t)
}

// listPoints is ListRestorePoints on a server already resolved with dest.
func listPoints(dst incus.InstanceServer, v Volume, t Target) ([]RestorePoint, error) {
	vols, err := dst.GetStoragePoolVolumes(t.pool())
	if err != nil {
		return nil, fmt.Errorf("listing volumes in pool %q: %w", t.where(), err)
	}
	return restorePointsOf(vols, backupmeta.CopyOf(v.Project, v.pool(), v.Name)), nil
}

func restorePointsOf(vols []api.StorageVolume, copyOf string) []RestorePoint {
	var out []RestorePoint
	for _, vol := range vols {
		if vol.Type != "custom" || vol.Config[backupmeta.MarkerCopyOf] != copyOf {
			continue
		}
		at, err := time.Parse(time.RFC3339, vol.Config[backupmeta.MarkerCopyAt])
		if err != nil {
			continue // a marker we cannot read: leave the volume alone rather than guess its age
		}
		out = append(out, RestorePoint{Volume: vol.Name, At: at, Snapshot: vol.Config[backupmeta.MarkerCopySnap], Server: vol.Config[backupmeta.MarkerCopyServer]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// prune removes restore points older than retain, never the newest and never `keep` (the one just made). It only
// ever considers restore points `me` made (or that predate the server marker): another server's are left alone,
// and their servers are returned so the caller can say so. "Newest" is the newest of this server's own.
func prune(dst incus.InstanceServer, v Volume, t Target, retain string, now time.Time, keep string, dryRun bool, me string) (pruned, others []string, err error) {
	all, err := listPoints(dst, v, t)
	if err != nil {
		return nil, nil, err
	}
	points, others := splitByServer(all, me)
	victims, err := expired(points, retain, now, keep)
	if err != nil {
		return nil, others, err
	}
	if dryRun {
		return victims, others, nil
	}
	for _, name := range victims {
		if err := dst.DeleteStoragePoolVolume(t.pool(), "custom", name); err != nil {
			return pruned, others, fmt.Errorf("pruning %s/%s: %w", t.where(), name, err)
		}
		pruned = append(pruned, name)
	}
	return pruned, others, nil
}

// expired chooses which restore points to prune: those older than retain, except the newest, and
// except `keep`. points must be newest first. Pure, so the rule that protects the backups is tested.
func expired(points []RestorePoint, retain string, now time.Time, keep string) ([]string, error) {
	var out []string
	for i, p := range points {
		if i == 0 || p.Volume == keep {
			continue
		}
		cutoff, err := backupmeta.ExpiryAfter(p.At, retain)
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
	dst, err := t.dest(s, v)
	if err != nil {
		return err
	}
	src, _, err := dst.GetStoragePoolVolume(t.pool(), "custom", rp.Volume)
	if err != nil {
		return fmt.Errorf("restore point %s/%s: %w", t.where(), rp.Volume, err)
	}
	from := api.StorageVolume{Name: rp.Volume, Type: "custom", ContentType: src.ContentType}
	op, err := s.CopyStoragePoolVolume(v.pool(), dst, t.pool(), from, &incus.StoragePoolVolumeCopyArgs{Name: newName, Mode: t.transferMode()})
	if err == nil {
		err = op.Wait()
	}
	if err != nil {
		note := ""
		if left, lerr := volumeExists(s, v.pool(), newName); lerr != nil {
			note = fmt.Sprintf(" (and could not check whether a partial volume %s/%s was left: %v; delete it by hand if it exists)", v.pool(), newName, lerr)
		} else if left {
			if derr := s.DeleteStoragePoolVolume(v.pool(), "custom", newName); derr != nil {
				note = fmt.Sprintf(" (and the partial volume %s/%s could not be removed: %v; delete it by hand)", v.pool(), newName, derr)
			}
		}
		return fmt.Errorf("copying %s/%s to %s/%s: %w%s", t.where(), rp.Volume, v.pool(), newName, err, note)
	}
	return scrubMarkers(s, v.pool(), newName)
}

// scrubMarkers removes the copy markers, and the copy policy, from a volume made from a restore point.
func scrubMarkers(s incus.InstanceServer, pool, name string) error {
	return scrubConfig(s, pool, name, func(k string) bool {
		return strings.HasPrefix(k, "user.tink.backup.copy-") || k == backupmeta.PolicyKey
	})
}

// scrubPolicy removes the copy policy from a volume made by copying another volume, or one of its snapshots: the
// policy says what is to happen to the volume it was written on, and a copy that inherited it would be scheduled for
// backup in its turn.
func scrubPolicy(s incus.InstanceServer, pool, name string) error {
	return scrubConfig(s, pool, name, func(k string) bool { return k == backupmeta.PolicyKey })
}

func scrubConfig(s incus.InstanceServer, pool, name string, drop func(string) bool) error {
	vol, etag, err := s.GetStoragePoolVolume(pool, "custom", name)
	if err != nil {
		return err
	}
	put := vol.Writable()
	changed := false
	for k := range put.Config {
		if drop(k) {
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

// PartialGrace is how old an in-progress copy must be before it is believed abandoned and swept. It is long on
// purpose: a copy of a large volume over a slow link takes a long time, and removing one that is still going would be
// worse than leaving a dead one for a few days.
var PartialGrace = 7 * 24 * time.Hour

// sweepPartials removes copies that were started and never finished, and returns their names. It is deliberately
// narrow, because it deletes volumes: only a volume carrying THIS tink's in-progress mark for THIS volume qualifies,
// and never one that
//   - also carries a restore point's mark (a finished copy is never removed here, whatever else it carries),
//   - was started by another server,
//   - is the one this run just made,
//   - is younger than PartialGrace, or whose start time cannot be read.
//
// Anything without the in-progress mark, a look-alike name included, is left alone.
func sweepPartials(dst incus.InstanceServer, v Volume, t Target, now time.Time, keep string, dryRun bool, me string) ([]string, error) {
	vols, err := dst.GetStoragePoolVolumes(t.pool())
	if err != nil {
		return nil, fmt.Errorf("listing volumes in pool %q: %w", t.where(), err)
	}
	copyOf := backupmeta.CopyOf(v.Project, v.pool(), v.Name)
	var victims []string
	for _, vol := range vols {
		if vol.Type != "custom" || vol.Name == keep || vol.Config[backupmeta.MarkerPartialOf] != copyOf {
			continue
		}
		if _, isRestorePoint := vol.Config[backupmeta.MarkerCopyOf]; isRestorePoint {
			continue
		}
		if srv := vol.Config[backupmeta.MarkerCopyServer]; srv != "" && srv != me {
			continue
		}
		started, err := time.Parse(time.RFC3339, vol.Config[backupmeta.MarkerPartialAt])
		if err != nil || now.Sub(started) < PartialGrace {
			continue
		}
		victims = append(victims, vol.Name)
	}
	sort.Strings(victims)
	if dryRun {
		return victims, nil
	}
	var swept []string
	var errs []error
	for _, name := range victims {
		if err := dst.DeleteStoragePoolVolume(t.pool(), "custom", name); err != nil {
			errs = append(errs, fmt.Errorf("removing the abandoned partial copy %s/%s: %w", t.where(), name, err))
			continue
		}
		swept = append(swept, name)
	}
	return swept, errors.Join(errs...)
}
