package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/jobs"
)

// The local commands that put work into, and look at, the directory `tink daemon run --jobs` uses. They work on files,
// so they run on the machine that has the directory.

func newDaemonEnqueueCmd() *cobra.Command {
	var jobsDir string
	var due, dryRun bool
	cmd := &cobra.Command{
		Use:   "enqueue [VOLUME...]",
		Short: "Queue a backup run for the executor, from the volumes' copy policies",
		Long: `enqueue creates a backup-run job in --jobs: the same thing "tink backup run" does, done by the daemon's
executor, which survives this command exiting. The job copies the volumes that carry a copy policy (written by
"tink plan apply"): a stack is never sent with it, so what a job does cannot differ from what the volumes say.
Volume names (or project/name) limit the run; --due runs only what is due. To run one stack's copies, use
"tink backup run -f FILE".`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobsDir == "" {
				return fmt.Errorf("--jobs is required")
			}
			rargs, _ := json.Marshal(daemon.BackupRunArgs{Volumes: args, Due: due, DryRun: dryRun})
			req := jobs.Request{Kind: daemon.KindBackupRun, Origin: jobs.OriginTrigger, Args: rargs}
			id, err := (jobs.Store{Dir: jobsDir}).Enqueue(req, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queued job %s\nfollow it with: tink daemon jobs --jobs %s --log %s\n", id, jobsDir, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&jobsDir, "jobs", "", "the jobs directory the daemon runs from")
	cmd.Flags().BoolVar(&due, "due", false, "only the copies that are due")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "say what would happen; change nothing")
	return cmd
}

func newDaemonJobsCmd() *cobra.Command {
	var jobsDir, logOf string
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List the queued, running and recent jobs, and whether the daemon is alive; --log ID shows one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobsDir == "" {
				return fmt.Errorf("--jobs is required")
			}
			store, out := jobs.Store{Dir: jobsDir}, cmd.OutOrStdout()
			if logOf != "" {
				st, err := store.Status(logOf)
				if err != nil {
					return err
				}
				b, _ := json.MarshalIndent(st, "", "  ")
				fmt.Fprintf(out, "%s\n", b)
				log, err := store.Log(logOf)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "--- log\n%s", log)
				return nil
			}
			if hb, err := store.LastBeat(); err == nil {
				fmt.Fprintf(out, "daemon: last heartbeat %s ago (pid %d, tink %s, schedules in %s)\n\n", time.Since(hb.Time).Round(time.Second), hb.PID, hb.Version, hb.Zone)
			} else if os.IsNotExist(err) {
				fmt.Fprintf(out, "daemon: no heartbeat in %s: it has never run here, or is not running\n\n", jobsDir)
			}
			list, err := store.List()
			if err != nil {
				return err
			}
			return printJobList(out, list)
		},
	}
	cmd.Flags().StringVar(&jobsDir, "jobs", "", "the jobs directory the daemon runs from")
	cmd.Flags().StringVar(&logOf, "log", "", "show this job's status and log")
	return cmd
}

func newDaemonCancelCmd() *cobra.Command {
	var jobsDir string
	cmd := &cobra.Command{
		Use:   "cancel ID",
		Short: "Ask a job to stop (it takes effect between its copies; a copy already under way finishes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobsDir == "" {
				return fmt.Errorf("--jobs is required")
			}
			if err := (jobs.Store{Dir: jobsDir}).Cancel(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "asked job %s to stop\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&jobsDir, "jobs", "", "the jobs directory the daemon runs from")
	return cmd
}

// printJobList prints jobs, newest first, as a table.
func printJobList(out io.Writer, list []jobs.Status) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tKIND\tORIGIN\tAGE\tERROR")
	for i := len(list) - 1; i >= 0; i-- {
		st := list[i]
		msg := st.Error
		if len(msg) > 70 {
			msg = msg[:70] + "..."
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", st.ID, st.State, st.Kind, st.Origin, jobAge(st.Created), msg)
	}
	return w.Flush()
}

// jobAge is how long ago t was, or "-" when it is not known (a job whose request could not be read has no creation time,
// and time.Since of the zero time overflows into nonsense).
func jobAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}
