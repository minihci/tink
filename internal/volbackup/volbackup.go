// Package volbackup restores and verifies custom storage volumes from their
// snapshots: the restore half of the backup story (docs/volume-backup-design.md).
//
// Two operations, both built on Incus's own volume copy, which is a
// copy-on-write clone on btrfs/ZFS and so cheap:
//
//   - Restore copies a snapshot to a NEW volume. It never overwrites the source or
//     any existing volume: overwriting live data is the one destructive step in the
//     whole feature, so the swap-in is left to the person.
//   - Verify restores to a scratch volume, optionally runs a declared check against
//     it in a throwaway instance, deletes both, and stamps the result on the source
//     volume so `tink plan` can tell a tested backup from an untested one.
//
// Both work from the volume's own snapshots (tier 1) or, with From set, from a restore point on a
// backup target that `tink backup run` copied there (copy.go).
package volbackup

import (
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/run"
)

// Volume identifies a custom storage volume.
type Volume struct {
	Project string // empty means the daemon's default project
	Pool    string // empty means "default"
	Name    string
}

func (v Volume) pool() string {
	if v.Pool == "" {
		return "default"
	}
	return v.Pool
}

func (v Volume) scoped(server incus.InstanceServer) incus.InstanceServer {
	if v.Project != "" {
		return server.UseProject(v.Project)
	}
	return server
}

// RestoreOptions control Restore.
type RestoreOptions struct {
	// Snapshot to restore; empty means the most recent.
	Snapshot string
	// As names the new volume; empty means "<volume>-restore-<UTC time>".
	As string
	// From restores from this target's restore point instead of a local snapshot; Snapshot then names
	// the restore point (its volume name, or the UTC stamp in it).
	From *Target
	Now  func() time.Time
}

// RestoreResult says what was created.
type RestoreResult struct {
	Snapshot string // the snapshot, or with From the restore point, that was restored
	Volume   string // the new volume
	// MadeBy is the server that made the restore point, when it was not this one: restoring a backup another server
	// made is allowed (it is the point of a restore on a rebuilt host) but worth saying.
	MadeBy string
}

// Restore copies one of v's snapshots (or, with From, a restore point on a target) to a new volume.
// The new volume is an independent copy: restoring never touches v or any existing volume, and with
// From it does not need v to exist at all.
func Restore(server incus.InstanceServer, v Volume, opts RestoreOptions) (RestoreResult, error) {
	now := nowOr(opts.Now)
	s := v.scoped(server)

	restoreFrom, err := sourceToRestore(s, v, opts.From, opts.Snapshot)
	if err != nil {
		return RestoreResult{}, err
	}
	name := opts.As
	if name == "" {
		name = restoreName(v.Name, now())
	}
	exists, err := volumeExists(s, v.pool(), name)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("checking whether volume %s/%s already exists: %w", v.pool(), name, err)
	}
	if exists {
		return RestoreResult{}, fmt.Errorf("volume %s/%s already exists: restore only ever creates a new volume (pick another --as)", v.pool(), name)
	}
	if err := restoreFrom.run(name); err != nil {
		return RestoreResult{}, err
	}
	res := RestoreResult{Snapshot: restoreFrom.label, Volume: name}
	if restoreFrom.server != "" {
		if me, err := serverName(server); err == nil && me != restoreFrom.server {
			res.MadeBy = restoreFrom.server
		}
	}
	return res, nil
}

// VerifyOptions control Verify.
type VerifyOptions struct {
	// Snapshot to verify; empty means the most recent.
	Snapshot string
	// Check is run against the restored data; nil verifies only that the snapshot
	// can be restored to a volume at all.
	Check *resolve.VerifyCheck
	// From verifies a restore point on this target instead of a local snapshot.
	From *Target
	Now  func() time.Time
	// Progress, if set, gets one line per step.
	Progress io.Writer
}

// VerifyResult says what a passing verification proved.
type VerifyResult struct {
	Snapshot string
	// With is "check" when a declared check ran and passed, "restore" when only the
	// restore itself was proven.
	With     string
	Output   string // the check's output, if one ran
	Duration time.Duration
	// Recorded says whether the result was stamped on the source volume. It is not when the source no
	// longer exists (a restore from a target after losing it).
	Recorded bool
}

// CheckFailedError means the restore worked but the declared check did not pass.
type CheckFailedError struct {
	ExitCode int
	Output   string
}

func (e *CheckFailedError) Error() string {
	return fmt.Sprintf("verification check failed with exit status %d:\n%s", e.ExitCode, strings.TrimRight(e.Output, "\n"))
}

// Verify proves a backup of v is usable: restore the snapshot to a scratch
// volume, run the declared check against it (if any) in a throwaway instance with
// the volume mounted read-only, delete both, and -- only if it all passed -- stamp
// the source volume with the result. Cleanup runs on every path; if it cannot
// finish, the error names what is left behind.
func Verify(server incus.InstanceServer, v Volume, opts VerifyOptions) (res VerifyResult, err error) {
	now := nowOr(opts.Now)
	start := now()
	s := v.scoped(server)
	say := func(format string, args ...any) {
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, format+"\n", args...)
		}
	}

	restoreFrom, err := sourceToRestore(s, v, opts.From, opts.Snapshot)
	if err != nil {
		return res, err
	}
	snap := restoreFrom.label
	res.Snapshot = snap
	from := "local"
	if opts.From != nil {
		from = opts.From.Name
	}

	var cleanups []func() error
	defer func() {
		// Newest first: the instance must go before the volume it mounts.
		var left []string
		for i := len(cleanups) - 1; i >= 0; i-- {
			if cerr := cleanups[i](); cerr != nil {
				left = append(left, cerr.Error())
			}
		}
		if len(left) > 0 {
			leftover := fmt.Errorf("cleanup incomplete, delete by hand: %s", strings.Join(left, "; "))
			if err == nil {
				err = leftover
			} else {
				err = fmt.Errorf("%w (and %v)", err, leftover)
			}
		}
	}()

	scratch := scratchName(v.Name, start)
	say("restoring %s (%s) to scratch volume %s", snap, from, scratch)
	if err := restoreFrom.run(scratch); err != nil {
		return res, err
	}
	cleanups = append(cleanups, func() error {
		if derr := s.DeleteStoragePoolVolume(v.pool(), "custom", scratch); derr != nil {
			return fmt.Errorf("volume %s/%s: %w", v.pool(), scratch, derr)
		}
		return nil
	})

	res.With = "restore"
	if opts.Check != nil {
		res.With = "check"
		instName := instanceName(v.Name, start)
		mount := opts.Check.Mount
		if mount == "" {
			mount = resolve.DefaultVerifyMount
		}
		say("running the check in throwaway instance %s (%s), volume mounted read-only at %s", instName, opts.Check.Image, mount)

		// Registered BEFORE creating, so a half-created instance is still cleaned up.
		cleanups = append(cleanups, func() error { return deleteInstance(s, instName) })

		spec := &run.Spec{
			Name:     instName,
			Image:    opts.Check.Image,
			Profiles: []string{"default"},
			// An OCI container exits when its entrypoint does; keep it alive to exec into.
			Config:  map[string]string{"oci.entrypoint": "sleep 3600"},
			Devices: map[string]map[string]string{"data": {"type": "disk", "pool": v.pool(), "source": scratch, "path": mount, "readonly": "true"}},
		}
		if err := run.Create(server, spec, v.Project); err != nil {
			return res, err
		}
		if err := run.ApplyConfig(s, spec); err != nil {
			return res, err
		}
		if err := run.EnsureRunning(s, instName); err != nil {
			return res, err
		}
		code, out, err := incusapi.ExecInGuest(s, instName, opts.Check.Command)
		if err != nil {
			return res, fmt.Errorf("running the check: %w", err)
		}
		res.Output = out
		if code != 0 {
			return res, &CheckFailedError{ExitCode: code, Output: out}
		}
	}

	// A failed look-up does not fail a verification that passed: the result simply is not recorded, and it says why.
	switch exists, lerr := volumeExists(s, v.pool(), v.Name); {
	case lerr != nil:
		say("could not check whether the source volume %s/%s exists (%v), so the result is not recorded on it", v.pool(), v.Name, lerr)
	case exists:
		if err := stamp(s, v, snap, res.With, from, now()); err != nil {
			return res, err
		}
		res.Recorded = true
	default:
		say("the source volume %s/%s does not exist (lost?), so the result is not recorded on it", v.pool(), v.Name)
	}
	res.Duration = now().Sub(start)
	return res, nil
}

func stamp(s incus.InstanceServer, v Volume, snap, with, from string, at time.Time) error {
	vol, etag, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return fmt.Errorf("reading %s/%s to record the verification: %w", v.pool(), v.Name, err)
	}
	put := vol.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	put.Config[backupmeta.StampVerifiedAt] = at.UTC().Format(time.RFC3339)
	put.Config[backupmeta.StampVerifiedSnapshot] = snap
	put.Config[backupmeta.StampVerifiedWith] = with
	put.Config[backupmeta.StampVerifiedFrom] = from
	if err := s.UpdateStoragePoolVolume(v.pool(), "custom", v.Name, put, etag); err != nil {
		return fmt.Errorf("recording the verification on %s/%s: %w", v.pool(), v.Name, err)
	}
	return nil
}

func snapshotToUse(s incus.InstanceServer, v Volume, wanted string) (string, error) {
	if _, _, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name); err != nil {
		return "", fmt.Errorf("volume %s/%s: %w", v.pool(), v.Name, err)
	}
	snaps, err := s.GetStoragePoolVolumeSnapshots(v.pool(), "custom", v.Name)
	if err != nil {
		return "", fmt.Errorf("listing snapshots of %s/%s: %w", v.pool(), v.Name, err)
	}
	return pickSnapshot(snaps, wanted)
}

// sourceToRestore resolves which backup to restore: a local snapshot, or (with from) a restore point on
// that target. It checks the backup exists before anything is created.
func sourceToRestore(s incus.InstanceServer, v Volume, from *Target, wanted string) (restoreFunc, error) {
	if from == nil {
		snap, err := snapshotToUse(s, v, wanted)
		if err != nil {
			return restoreFunc{}, err
		}
		return restoreFunc{label: snap, run: func(newName string) error { return copySnapshot(s, v, snap, newName) }}, nil
	}
	// The source volume is deliberately NOT required to exist here: restoring from a target is for exactly
	// the case where it is gone. Restore points are found by their marker, which names the volume.
	points, err := ListRestorePoints(s, v, *from)
	if err != nil {
		return restoreFunc{}, err
	}
	rp, err := pickRestorePoint(points, wanted)
	if err != nil {
		return restoreFunc{}, fmt.Errorf("target %q: %w", from.Name, err)
	}
	return restoreFunc{label: rp.Volume, server: rp.Server, run: func(newName string) error { return copyFromTarget(s, v, *from, rp, newName) }}, nil
}

// restoreFunc is a backup that can be materialised as a new local volume (run), and a label saying which.
type restoreFunc struct {
	label  string
	server string // the server that made the restore point, if known
	run    func(newName string) error
}

// volumeExists says whether the custom volume is there. A look-up that failed is an error, never "not there": a guard that
// proceeds on a failed read overwrites or double-creates, and a cleanup that skips on one leaves a partial copy behind.
func volumeExists(s incus.InstanceServer, pool, name string) (bool, error) {
	_, _, found, err := incusapi.LookupVolume(s, pool, "custom", name)
	return found, err
}

func copySnapshot(s incus.InstanceServer, v Volume, snap, newName string) error {
	src, _, err := s.GetStoragePoolVolume(v.pool(), "custom", v.Name)
	if err != nil {
		return fmt.Errorf("volume %s/%s: %w", v.pool(), v.Name, err)
	}
	from := api.StorageVolume{Name: v.Name + "/" + snap, Type: "custom", ContentType: src.ContentType}
	op, err := s.CopyStoragePoolVolume(v.pool(), s, v.pool(), from, &incus.StoragePoolVolumeCopyArgs{Name: newName})
	if err != nil {
		return fmt.Errorf("restoring %s/%s@%s to %s: %w", v.pool(), v.Name, snap, newName, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("restoring %s/%s@%s to %s: %w", v.pool(), v.Name, snap, newName, err)
	}
	// the snapshot carries the volume's config, including its copy policy: the restored volume is not the volume that
	// is copied on a schedule (the operator may swap it in, and applies the stack again, which writes the policy back)
	if err := scrubPolicy(s, v.pool(), newName); err != nil {
		return fmt.Errorf("restored %s/%s@%s to %s, but could not clear its copy policy: %w", v.pool(), v.Name, snap, newName, err)
	}
	return nil
}

func deleteInstance(s incus.InstanceServer, name string) error {
	_, _, found, err := incusapi.LookupInstance(s, name)
	if err != nil {
		// Not "never got created": skipping on a failed read would leave the throwaway instance, and the volume mounted in it, behind
		// with nothing said. The cleanup reports it, naming the instance to remove by hand.
		return fmt.Errorf("instance %s: could not check whether it exists, so it was not removed: %w", name, err)
	}
	if !found {
		return nil // never got created
	}
	if op, err := s.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Force: true, Timeout: 30}, ""); err == nil {
		_ = op.Wait() // already stopped is fine; the delete below is what must work
	}
	op, err := s.DeleteInstance(name)
	if err != nil {
		return fmt.Errorf("instance %s: %w", name, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("instance %s: %w", name, err)
	}
	return nil
}

// pickSnapshot chooses the snapshot to restore: the named one, or else the most
// recently created.
func pickSnapshot(snaps []api.StorageVolumeSnapshot, wanted string) (string, error) {
	if len(snaps) == 0 {
		return "", fmt.Errorf("the volume has no snapshots to restore from (is a backup: snapshots policy applied, and has it run yet?)")
	}
	names := make([]string, 0, len(snaps))
	byName := map[string]api.StorageVolumeSnapshot{}
	for _, sn := range snaps {
		n := path.Base(sn.Name) // listings may or may not carry the "volume/" prefix
		names = append(names, n)
		byName[n] = sn
	}
	if wanted != "" {
		wanted = path.Base(wanted)
		if _, ok := byName[wanted]; !ok {
			sort.Strings(names)
			return "", fmt.Errorf("no snapshot named %q (have: %s)", wanted, strings.Join(names, ", "))
		}
		return wanted, nil
	}
	sort.SliceStable(names, func(i, j int) bool {
		return byName[names[i]].CreatedAt.After(byName[names[j]].CreatedAt)
	})
	return names[0], nil
}

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9-]+`)

func stamped(t time.Time) string { return t.UTC().Format("20060102-150405") }

// restoreName is the default name of a restored volume.
func restoreName(vol string, t time.Time) string { return vol + "-restore-" + stamped(t) }

// scratchName is the throwaway volume a verification restores into.
func scratchName(vol string, t time.Time) string { return vol + "-verify-" + stamped(t) }

// instanceName is the throwaway instance: letters, digits and hyphens only, and
// within Incus's 63-character limit.
func instanceName(vol string, t time.Time) string {
	base := strings.Trim(unsafeName.ReplaceAllString(vol, "-"), "-")
	suffix := "-" + stamped(t)
	const prefix = "tink-verify-"
	if room := 63 - len(prefix) - len(suffix); len(base) > room {
		base = strings.TrimRight(base[:room], "-")
	}
	return prefix + base + suffix
}

func nowOr(f func() time.Time) func() time.Time {
	if f != nil {
		return f
	}
	return time.Now
}
