package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/jobs"
)

// The local commands that put work into, and look at, the directories `tink daemon run --stacks --jobs` uses. They
// work on files, so they run on the machine that has the directories.

func newDaemonSyncCmd() *cobra.Command {
	var stacksDir string
	cmd := &cobra.Command{
		Use:   "sync NAME [FILE...]",
		Short: "Store a stack (and every file it reads) for the scheduler, as the active version of NAME",
		Long: `sync loads the stack files (default: ./tink.yaml) as an operator would, packs them with every file they read
(source_path) with their relative layout intact, and makes that the active version of the stack NAME in --stacks.
It only becomes active if it loads there, so a stack that does not parse never replaces one that does. The scheduler
picks it up on its next tick.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if stacksDir == "" {
				return fmt.Errorf("--stacks is required")
			}
			b, err := jobs.BuildBundle(args[1:])
			if err != nil {
				return err
			}
			if err := (jobs.Stacks{Dir: stacksDir}).Sync(args[0], b.Files, b.Entries, time.Now()); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "stack %s synced: %d file(s) (%s)\n", args[0], len(b.Files), strings.Join(b.Names(), ", "))
			return nil
		},
	}
	cmd.Flags().StringVar(&stacksDir, "stacks", "", "the stacks directory the daemon reads")
	return cmd
}

func newDaemonEnqueueCmd() *cobra.Command {
	var jobsDir, stack string
	var files []string
	var due, dryRun bool
	cmd := &cobra.Command{
		Use:   "enqueue [VOLUME...]",
		Short: "Queue a backup run for the executor, from a synced stack or from stack files sent with the job",
		Long: `enqueue creates a backup-run job in --jobs: the same thing "tink backup run" does, done by the daemon's
executor, which survives this command exiting. Name the stack to run with --stack (one synced with "tink daemon
sync"), or send stack files with -f, which are bundled with the job and used for this job only. Volume names limit the
run; --due runs only what is due.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if jobsDir == "" {
				return fmt.Errorf("--jobs is required")
			}
			if (stack == "") == (len(files) == 0) {
				return fmt.Errorf("give exactly one of --stack NAME and -f FILE")
			}
			rargs, _ := json.Marshal(daemon.BackupRunArgs{Volumes: args, Due: due, DryRun: dryRun})
			req := jobs.Request{Kind: daemon.KindBackupRun, Origin: jobs.OriginTrigger, Stack: stack, Args: rargs}
			var bundle map[string][]byte
			if len(files) > 0 {
				b, err := jobs.BuildBundle(files)
				if err != nil {
					return err
				}
				bundle, req.Entries = b.Files, b.Entries
			}
			id, err := (jobs.Store{Dir: jobsDir}).Enqueue(req, bundle, time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "queued job %s\nfollow it with: tink daemon jobs --jobs %s --log %s\n", id, jobsDir, id)
			return nil
		},
	}
	cmd.Flags().StringVar(&jobsDir, "jobs", "", "the jobs directory the daemon runs from")
	cmd.Flags().StringVar(&stack, "stack", "", "the name of a synced stack")
	cmd.Flags().StringArrayVarP(&files, "file", "f", nil, "stack file(s) to send with the job")
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
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATE\tKIND\tORIGIN\tAGE\tERROR")
			for i := len(list) - 1; i >= 0; i-- { // newest first
				st := list[i]
				msg := st.Error
				if len(msg) > 70 {
					msg = msg[:70] + "..."
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", st.ID, st.State, st.Kind, st.Origin, jobAge(st.Created), msg)
			}
			return w.Flush()
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

// jobAge is how long ago t was, or "-" when it is not known (a job whose request could not be read has no creation time,
// and time.Since of the zero time overflows into nonsense).
func jobAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}
