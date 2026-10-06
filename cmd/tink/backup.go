package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

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
	cmd.Flags().StringVar(&f.snapshot, "snapshot", "", "snapshot to use (default: the most recent)")
	cmd.Flags().StringVar(&f.from, "from", "", "restore from a backup target instead of a local snapshot (not implemented yet)")
	cmd.Flags().StringArrayVarP(&f.files, "file", "f", nil, "stack YAML to read the volume's pool, project and verify check from (default: ./tink.yaml if it exists)")
}

// resolveVolume finds the volume's pool/project, and its declared verify check, in the
// stack file(s) when it is declared there; flags override what the stack says.
func (f *volumeFlags) resolveVolume(name string) (volbackup.Volume, *resolve.VerifyCheck, error) {
	if f.from != "" {
		return volbackup.Volume{}, nil, fmt.Errorf("--from %s: restoring from a backup target needs the copy engine, which does not exist yet; only local snapshots are supported so far", f.from)
	}
	v := volbackup.Volume{Name: name, Pool: f.pool, Project: f.project}

	files := f.files
	if len(files) == 0 {
		if _, err := os.Stat(resolve.DefaultFile); err != nil {
			return v, nil, nil // no stack to consult
		}
		files = []string{resolve.DefaultFile}
	}
	resources, err := resolve.LoadFiles(files)
	if err != nil {
		return v, nil, err
	}
	for _, r := range resources {
		if r.Kind != resolve.KindStorageVolume || r.Name != name {
			continue
		}
		if v.Pool == "" {
			v.Pool = r.Pool
		}
		if v.Project == "" {
			v.Project = r.Project
		}
		var check *resolve.VerifyCheck
		if r.Backup != nil {
			check = r.Backup.VerifyCheck
		}
		return v, check, nil
	}
	return v, nil, nil // not declared in the stack: flags alone describe it
}

func newVolBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Restore and verify volumes from their snapshots",
		Long: `Restore and verify custom storage volumes from their snapshots: the restore half of the
backup story (docs/volume-backup.md). Local snapshots only for now.`,
	}
	cmd.AddCommand(newBackupRestoreCmd(), newBackupVerifyCmd())
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
			v, _, err := f.resolveVolume(args[0])
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(f.socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			res, err := volbackup.Restore(server, v, volbackup.RestoreOptions{Snapshot: f.snapshot, As: as})
			if err != nil {
				return err
			}
			pool := v.Pool
			if pool == "" {
				pool = "default"
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "restored %s/%s@%s -> %s/%s\n", pool, v.Name, res.Snapshot, pool, res.Volume)
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
			v, check, err := f.resolveVolume(args[0])
			if err != nil {
				return err
			}
			server, err := incusapi.Connect(f.socket)
			if err != nil {
				return fmt.Errorf("connecting to incus: %w", err)
			}
			out := cmd.OutOrStdout()
			res, err := volbackup.Verify(server, v, volbackup.VerifyOptions{Snapshot: f.snapshot, Check: check, Progress: out})
			if err != nil {
				var failed *volbackup.CheckFailedError
				if errors.As(err, &failed) {
					fmt.Fprintf(out, "FAILED: restored %s@%s, but the check did not pass (exit %d). Nothing was recorded on the volume.\n", v.Name, res.Snapshot, failed.ExitCode)
				}
				return err
			}
			if res.With == "check" {
				if o := strings.TrimRight(res.Output, "\n"); o != "" {
					fmt.Fprintf(out, "check output:\n  %s\n", strings.ReplaceAll(o, "\n", "\n  "))
				}
				fmt.Fprintf(out, "verified %s@%s: restored, and the check passed (%s). Recorded on the volume.\n", v.Name, res.Snapshot, res.Duration.Round(100_000_000))
			} else {
				fmt.Fprintf(out, "verified %s@%s: the snapshot restores (%s). No check is declared, so the contents were not examined; add backup.verify.check to test them. Recorded on the volume.\n",
					v.Name, res.Snapshot, res.Duration.Round(100_000_000))
			}
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}
