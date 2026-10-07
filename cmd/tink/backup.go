package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

// volumeFlags are shared by `backup restore` and `backup verify`.
type volumeFlags struct {
	socket   string
	pool     string
	project  string
	snapshot string
	from     string
	files    []string
}

func (f *volumeFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&f.pool, "pool", "", "storage pool of the volume (default: from the stack file, else \"default\")")
	cmd.Flags().StringVar(&f.project, "project", "", "Incus project of the volume (default: from the stack file, else the default project)")
	cmd.Flags().StringVar(&f.snapshot, "snapshot", "", "snapshot to use (default: the most recent); with --from, the restore point (its volume name, or the timestamp in it)")
	cmd.Flags().StringVar(&f.from, "from", "", "use a restore point on this backup target (a kind: backup-target in the stack) instead of a local snapshot")
	cmd.Flags().StringArrayVarP(&f.files, "file", "f", nil, "stack YAML to read the volume's pool, project and verify check from (default: ./tink.yaml if it exists)")
}

// resolveVolume finds the volume's pool/project, and its declared verify check, in the stack file(s)
// when it is declared there; flags override what the stack says. With --from it also resolves the
// named backup target from the stack.
func (f *volumeFlags) resolveVolume(name string) (volbackup.Volume, *resolve.VerifyCheck, *volbackup.Target, error) {
	v := volbackup.Volume{Name: name, Pool: f.pool, Project: f.project}

	resources, err := f.loadStack()
	if err != nil {
		return v, nil, nil, err
	}
	var check *resolve.VerifyCheck
	var vol *resolve.Resource
	for i, r := range resources {
		if r.Kind == resolve.KindStorageVolume && r.Name == name {
			vol = &resources[i]
			break
		}
	}
	if vol != nil {
		if v.Pool == "" {
			v.Pool = vol.Pool
		}
		if v.Project == "" {
			v.Project = vol.Project
		}
		if vol.Backup != nil {
			check = vol.Backup.VerifyCheck
		}
	}

	var target *volbackup.Target
	if f.from != "" {
		for _, r := range resources {
			if r.Kind == resolve.KindBackupTarget && r.Name == f.from {
				t := volbackup.TargetFrom(r)
				target = &t
			}
		}
		if target == nil {
			return v, nil, nil, fmt.Errorf("--from %s: no kind: backup-target with that name in the stack (give the stack with -f, or run from the directory holding tink.yaml)", f.from)
		}
		if vol != nil && vol.Backup != nil {
			copied := false
			for _, c := range vol.Backup.Copies {
				copied = copied || c.Target == f.from
			}
			if !copied {
				return v, nil, nil, fmt.Errorf("--from %s: %s does not declare a copy to that target", f.from, name)
			}
		}
	}
	return v, check, target, nil
}

// loadStack reads the stack files (-f, else ./tink.yaml when it exists); no stack is not an error.
func (f *volumeFlags) loadStack() ([]resolve.Resource, error) {
	files := f.files
	if len(files) == 0 {
		if _, err := os.Stat(resolve.DefaultFile); err != nil {
			return nil, nil
		}
		files = []string{resolve.DefaultFile}
	}
	return resolve.LoadFiles(files)
}

func newVolBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Restore and verify volumes from their snapshots",
		Long: `Restore and verify custom storage volumes from their snapshots: the restore half of the
backup story (docs/volume-backup.md), and run the copies a stack declares to its backup targets.`,
	}
	cmd.AddCommand(newBackupRunCmd(), newBackupRestoreCmd(), newBackupVerifyCmd())
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var f volumeFlags
	var as string
	cmd := &cobra.Command{
		Use:   "restore VOLUME [flags]",
		Short: "Copy a volume's snapshot to a new volume",
		Long: `restore copies one of VOLUME's snapshots (the most recent unless --snapshot says otherwise)
to a NEW volume, named VOLUME-restore-<UTC time> unless --as is given. It is a copy-on-write
clone on btrfs and ZFS, so it is fast and takes little space.

It never restores in place and never overwrites a volume: replacing live data is the one
destructive step in the whole feature, so it is left to you. Attach the restored volume to an
instance, or repoint the instance's disk device at it, when you are ready.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, _, target, err := f.resolveVolume(args[0])
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(f.socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			res, err := volbackup.Restore(server, v, volbackup.RestoreOptions{Snapshot: f.snapshot, As: as, From: target})
			if err != nil {
				return err
			}
			pool := v.Pool
			if pool == "" {
				pool = "default"
			}
			out := cmd.OutOrStdout()
			if target != nil {
				fmt.Fprintf(out, "restored restore point %s (from %s) -> %s/%s\n", res.Snapshot, target.Name, pool, res.Volume)
			} else {
				fmt.Fprintf(out, "restored %s/%s@%s -> %s/%s\n", pool, v.Name, res.Snapshot, pool, res.Volume)
			}
			fmt.Fprintln(out, "It is an independent copy; tink never restores in place. Attach it to an instance, or swap it in yourself.")
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().StringVar(&as, "as", "", "name of the new volume (default: VOLUME-restore-<UTC time>)")
	return cmd
}

func newBackupVerifyCmd() *cobra.Command {
	var f volumeFlags
	cmd := &cobra.Command{
		Use:   "verify VOLUME [flags]",
		Short: "Prove a volume's backup restores, and that the restored data is good",
		Long: `verify restores VOLUME's latest snapshot (or --snapshot) to a scratch volume, runs the check
declared under backup.verify.check in the stack file against it in a throwaway instance (volume
mounted read-only), deletes both, and records the result on VOLUME so "tink plan" can warn when a
declared verify cadence has lapsed.

With no check declared it only proves the snapshot can be restored, and says so: that is a weaker
claim than "the data is good". A failing check leaves the volume's record untouched, so a failed
verification never looks fresh.

  backup:
    snapshots: {schedule: "0 3 * * *", retain: 14d}
    verify:
      every: weekly
      check:
        image: docker-oci:library/alpine:3      # an OCI image with sleep
        command: [sh, -c, "test -s /data/important.db"]`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			v, check, target, err := f.resolveVolume(args[0])
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(f.socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			out := cmd.OutOrStdout()
			res, err := volbackup.Verify(server, v, volbackup.VerifyOptions{Snapshot: f.snapshot, Check: check, From: target, Progress: out})
			if err != nil {
				var failed *volbackup.CheckFailedError
				if errors.As(err, &failed) {
					fmt.Fprintf(out, "FAILED: restored %s@%s, but the check did not pass (exit %d). Nothing was recorded on the volume.\n", v.Name, res.Snapshot, failed.ExitCode)
				}
				return err
			}
			recorded := "Recorded on the volume."
			if !res.Recorded {
				recorded = "NOT recorded: the source volume does not exist."
			}
			if res.With == "check" {
				if o := strings.TrimRight(res.Output, "\n"); o != "" {
					fmt.Fprintf(out, "check output:\n  %s\n", strings.ReplaceAll(o, "\n", "\n  "))
				}
				fmt.Fprintf(out, "verified %s@%s: restored, and the check passed (%s). %s\n", v.Name, res.Snapshot, res.Duration.Round(100_000_000), recorded)
			} else {
				fmt.Fprintf(out, "verified %s@%s: the snapshot restores (%s). No check is declared, so the contents were not examined; add backup.verify.check to test them. %s\n",
					v.Name, res.Snapshot, res.Duration.Round(100_000_000), recorded)
			}
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

func newBackupRunCmd() *cobra.Command {
	var f volumeFlags
	var due, dryRun bool
	cmd := &cobra.Command{
		Use:   "run [VOLUME...] [flags]",
		Short: "Copy volumes to the backup targets their stack declares",
		Long: `run performs the copies declared under backup.copies: for each, it takes a snapshot of the
volume (a consistent point in time), copies that snapshot into a NEW volume on the target named
VOLUME-bk-<UTC time> (a restore point), removes the temporary snapshot, and prunes restore points
older than the copy's retain, always keeping the newest. With no VOLUME it does every volume that
declares copies.

Each run is a full copy into a separate volume, not a refresh of one target volume: a refresh makes
the target mirror the source's snapshots, so a snapshot deleted (or damaged) on the source would be
deleted from the backup at the next refresh, and the backup could never keep longer history than the
source. The price is the space and time of a full copy per run; incremental transfer is future work.

A target is another storage pool on this server (e.g. the Incus truenas driver) or an Incus remote:
another server, by the name "incus remote add" gave it, in the project that remote is configured
with. Tink reads the Incus client configuration of the user running it (root's, under sudo) and keeps
no credentials of its own. A remote's data is relayed through tink, so the remote only has to be
reachable from the machine running it (an SSH tunnel is enough).

--due runs only the copies whose schedule has come round since their last success, so cron or a
timer can call "tink backup run --due" every few minutes. Tink does not schedule them itself yet.
--dry-run says what would happen and changes nothing.

Restore from a restore point with: tink backup restore VOLUME --from TARGET`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resources, err := f.loadStack()
			if err != nil {
				return err
			}
			if len(resources) == 0 {
				return fmt.Errorf("no stack: give one with -f, or run from the directory holding %s", resolve.DefaultFile)
			}
			if _, err := resolve.Levels(resources); err != nil { // validates references, e.g. an unknown copy target
				return err
			}
			targets := map[string]resolve.Resource{}
			for _, r := range resources {
				if r.Kind == resolve.KindBackupTarget {
					targets[r.Name] = r
				}
			}
			selected, unknown := selectCopyVolumes(resources, args)

			server, err := incusapi.Connect(f.socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			out := cmd.OutOrStdout()
			var tried, failed, skipped int
			for _, r := range selected {
				v := volbackup.Volume{Project: r.Project, Pool: r.Pool, Name: r.Name}
				live, err := volbackup.LiveConfig(server, v)
				if err != nil {
					fmt.Fprintf(out, "%s: %v\n", r.Name, err)
					failed++
					continue
				}
				for _, c := range r.Backup.Copies {
					if due {
						isDue, err := resolve.CopyIsDue(c.Schedule, live, c.Target, time.Now())
						if err != nil {
							fmt.Fprintf(out, "%s -> %s: %v\n", r.Name, c.Target, err)
							failed++
							continue
						}
						if !isDue {
							fmt.Fprintf(out, "%s -> %s: not due\n", r.Name, c.Target)
							skipped++
							continue
						}
					}
					tried++
					res, err := volbackup.Copy(server, v, volbackup.TargetFrom(targets[c.Target]),
						volbackup.CopyOptions{Retain: c.Retain, DryRun: dryRun, Progress: out})
					if err != nil {
						fmt.Fprintf(out, "FAILED %s -> %s: %v\n", r.Name, c.Target, err)
						failed++
						continue
					}
					if dryRun {
						for _, p := range res.Planned {
							fmt.Fprintf(out, "%s -> %s: would %s\n", r.Name, c.Target, p)
						}
						continue
					}
					fmt.Fprintf(out, "copied %s -> %s: restore point %s", r.Name, c.Target, res.Volume)
					if len(res.Pruned) > 0 {
						fmt.Fprintf(out, " (pruned %d older: %s)", len(res.Pruned), strings.Join(res.Pruned, ", "))
					}
					fmt.Fprintln(out)
				}
			}
			for _, name := range unknown {
				fmt.Fprintf(out, "%s: not a storage-volume with copies in the stack\n", name)
				failed++
			}
			if tried == 0 && skipped == 0 && failed == 0 {
				fmt.Fprintln(out, "nothing to do: no volume in the stack declares copies")
			}
			if failed > 0 {
				return fmt.Errorf("%d copy operation(s) failed", failed)
			}
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&due, "due", false, "only the copies whose schedule has come round since their last success")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "say what would happen; change nothing")
	return cmd
}

// selectCopyVolumes picks the volumes `tink backup run` acts on: every storage volume that declares
// copies, or with names, only those. Names that are not such a volume come back in unknown, in the order
// given, so that asking for a volume that cannot be backed up is an error and not silently nothing.
func selectCopyVolumes(resources []resolve.Resource, names []string) (selected []resolve.Resource, unknown []string) {
	asked := map[string]bool{}
	for _, n := range names {
		asked[n] = true
	}
	found := map[string]bool{}
	for _, r := range resources {
		if r.Kind != resolve.KindStorageVolume || r.Backup == nil || len(r.Backup.Copies) == 0 {
			continue
		}
		if len(names) > 0 && !asked[r.Name] {
			continue
		}
		found[r.Name] = true
		selected = append(selected, r)
	}
	for _, n := range names {
		if !found[n] {
			unknown = append(unknown, n)
			found[n] = true // a name given twice is reported once
		}
	}
	return selected, unknown
}
