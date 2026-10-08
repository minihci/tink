package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/backuprun"
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
func (f *volumeFlags) resolveVolume(name string) (volbackup.Volume, *backupmeta.VerifyCheck, *volbackup.Target, error) {
	v := volbackup.Volume{Name: name, Pool: f.pool, Project: f.project}

	resources, err := f.loadStack()
	if err != nil {
		return v, nil, nil, err
	}
	var check *backupmeta.VerifyCheck
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
				t := backuprun.TargetFrom(r)
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
		Short: "Run copies, restore and verify volumes, and let a volume go",
		Long: `Restore and verify custom storage volumes from their snapshots: the restore half of the
backup story (docs/volume-backup.md), and run the copies a stack declares to its backup targets.`,
	}
	cmd.AddCommand(newBackupRunCmd(), newBackupRestoreCmd(), newBackupVerifyCmd(), newBackupForgetCmd())
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
				if res.MadeBy != "" {
					fmt.Fprintf(out, "note: that restore point was made by another server (%s), not this one\n", res.MadeBy)
				}
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

func newBackupForgetCmd() *cobra.Command {
	var socket, pool string
	cmd := &cobra.Command{
		Use:   "forget [PROJECT/]VOLUME... [flags]",
		Short: "Stop copying a volume: clear the copy policy that apply put on it",
		Long: `forget removes the copy policy (user.tink.backup.policy) from each volume, so the daemon's scheduler stops
copying it. Nothing else changes: the volume's data, its restore points on the targets, and the record of past
copies are left exactly as they are.

It is how a volume that has been taken out of the stack is let go. "tink plan apply" never removes a policy for a
volume it is no longer told about, because it cannot tell a volume that was dropped from this stack from one that
belongs to another, and a backup that stops by mistake is found out at restore time. "tink plan" lists the
volumes in this stack's projects and pools that carry a policy the stack does not declare.

Name a volume outside the default project as PROJECT/VOLUME, and one outside the "default" pool with --pool. If a
stack still declares copies for the volume, the next "tink plan apply" writes the policy back: remove them from the
YAML as well.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			server, err := incusapi.Connect(socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			out := cmd.OutOrStdout()
			failed := 0
			for _, arg := range args {
				v := volbackup.Volume{Pool: pool, Name: arg}
				if project, name, found := strings.Cut(arg, "/"); found {
					v.Project, v.Name = project, name
				}
				label := backuprun.Label(v)
				had, owner, err := volbackup.Forget(server, v)
				switch {
				case err != nil:
					fmt.Fprintf(out, "%s: %v\n", label, err)
					failed++
				case !had:
					fmt.Fprintf(out, "%s: carries no copy policy; nothing to do\n", label)
				default:
					fmt.Fprintf(out, "forgot %s: nothing will copy it any more. Its data, its restore points and the record of past copies are untouched.\n", label)
					if owner != "" {
						fmt.Fprintf(out, "  it was applied by stack %q: if that stack still declares copies for it, the next plan apply writes the policy back\n", owner)
					}
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d volume(s) could not be forgotten", failed)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&pool, "pool", "", "storage pool of the volumes (default \"default\")")
	return cmd
}

// noteUndeclaredPolicies says which volumes are still being copied although the stack no longer declares them. Volumes
// that point back at this stack are certain; the others are only found in its projects and pools and may belong to another
// stack, so they are a note and never a failure.
func noteUndeclaredPolicies(out io.Writer, eng backuprun.Engine, resources []resolve.Resource) {
	orphans, err := backuprun.Undeclared(eng, resources)
	if err != nil {
		fmt.Fprintf(out, "note: could not look for volumes that carry a copy policy this stack does not declare: %v\n", err)
		return
	}
	stack, _ := resolve.StackName(resources)
	var owned, unowned []string
	for _, o := range orphans {
		name := backuprun.Label(o.Volume)
		if o.Volume.Pool != "" && o.Volume.Pool != "default" {
			name += " (pool " + o.Volume.Pool + ")"
		}
		if o.Owned {
			owned = append(owned, name)
		} else {
			unowned = append(unowned, name)
		}
	}
	if len(owned) > 0 {
		fmt.Fprintf(out, "note: %d volume(s) applied by stack %q carry a copy policy but the stack no longer declares them, so they are still being copied: %s\n"+
			"      To stop copying one: tink backup forget [PROJECT/]VOLUME [--pool POOL]\n", len(owned), stack, strings.Join(owned, ", "))
	}
	if len(unowned) > 0 {
		fmt.Fprintf(out, "note: %d volume(s) in this stack's projects and pools carry a copy policy that the stack does not declare, and point at no stack, so they are still being copied: %s\n"+
			"      They may be another stack's; if they are this one's, to stop copying one: tink backup forget [PROJECT/]VOLUME [--pool POOL]\n"+
			"      (Name this stack with a `kind: stack` document and the next apply marks its volumes, so it can tell.)\n", len(unowned), strings.Join(unowned, ", "))
	}
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
timer can call "tink backup run --due" every few minutes, or "tink daemon run --jobs DIR" will, from the copy policies "tink plan apply" puts on the volumes (see docs/daemon-jobs.md).
--dry-run says what would happen and changes nothing.

Restore from a restore point with: tink backup restore VOLUME --from TARGET`,
		RunE: func(cmd *cobra.Command, args []string) error {
			resources, err := f.loadStack()
			if err != nil {
				return err
			}
			items, err := backuprun.FromStack(resources)
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(f.socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			rep, err := backuprun.Run(cmd.Context(), backuprun.ServerEngine{Server: server}, items,
				backuprun.Options{Volumes: args, Due: due, DryRun: dryRun, Remote: incusapi.Remote()}, cmd.OutOrStdout())
			if err != nil {
				return err
			}
			return rep.Err()
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&due, "due", false, "only the copies whose schedule has come round since their last success")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "say what would happen; change nothing")
	return cmd
}
