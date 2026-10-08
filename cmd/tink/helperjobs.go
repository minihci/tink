package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	incus "github.com/lxc/incus/v7/client"
	"github.com/spf13/cobra"

	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/jobs"
)

// The helper's jobs, from wherever tink runs: the same jobs directory `tink daemon jobs|cancel` read on the host, reached through the
// helper instance's file API.

type helperTarget struct{ socket, instance, project string }

func (h *helperTarget) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&h.socket, "socket", "", "Incus daemon unix socket path (default: Incus's own resolution)")
	cmd.Flags().StringVar(&h.instance, "instance", "", "the helper instance, when there is more than one")
	cmd.Flags().StringVar(&h.project, "project", "", "the helper's project, when there is more than one")
}

// open finds the helper and its jobs directory.
func (h *helperTarget) open() (jobs.Store, helper.Found, error) {
	server, err := incusapi.Connect(h.socket)
	if err != nil {
		return jobs.Store{}, helper.Found{}, fmt.Errorf("connecting to incus: %w", err)
	}
	return openHelperJobs(server, h.instance, h.project)
}

func openHelperJobs(server incus.InstanceServer, instance, project string) (jobs.Store, helper.Found, error) {
	all, err := helper.Find(server)
	if err != nil {
		return jobs.Store{}, helper.Found{}, err
	}
	f, err := helper.Pick(all, instance, project)
	if err != nil {
		return jobs.Store{}, helper.Found{}, err
	}
	store, err := helper.JobsStore(server, f)
	return store, f, err
}

func newHelperJobsCmd() *cobra.Command {
	var t helperTarget
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List the helper's queued, running and recent jobs",
		Long: `jobs lists what the helper has run and is running, newest first: the copies its scheduler queued, and the runs handed to it.
The list is read through the helper instance's file API, so it works from any machine that can reach the server, and only while the
helper's instance is running. Each job is one request to the server, and a finished job is one read.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := t.open()
			if err != nil {
				return err
			}
			list, err := store.List()
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no jobs")
				return nil
			}
			return printJobList(cmd.OutOrStdout(), list)
		},
	}
	t.bind(cmd)
	return cmd
}

func newHelperLogCmd() *cobra.Command {
	var t helperTarget
	var follow bool
	cmd := &cobra.Command{
		Use:   "log ID",
		Short: "Show a helper job's status and log; --follow waits for it to finish",
		Long: `log shows a job's status and its log. With --follow it keeps printing the log as it grows until the job is over, and exits
non-zero if the job failed or was cancelled. Ctrl-C stops following and leaves the job running; "tink helper cancel ID" stops the job.
Following reads the job every 2 to 30 seconds, backing off while nothing changes: each read is a request to the server.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := t.open()
			if err != nil {
				return err
			}
			return showHelperJob(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), store, args[0], follow, jobs.FollowOptions{})
		},
	}
	t.bind(cmd)
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing the log until the job is over")
	return cmd
}

func showHelperJob(ctx context.Context, out, errOut io.Writer, store jobs.Store, id string, follow bool, opt jobs.FollowOptions) error {
	if !follow {
		st, err := store.Status(id)
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintf(out, "%s\n", b)
		log, err := store.Log(id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "--- log\n%s", log)
		return nil
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	st, err := jobs.Follow(ctx, store, id, out, opt)
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(errOut, "\nstopped following; job %s carries on. Follow it again with: tink helper log -f %s\n", id, id)
		return nil
	case err != nil:
		return err
	}
	return jobOutcome(st)
}

// jobOutcome is the error a finished job stands for: none when it succeeded.
func jobOutcome(st jobs.Status) error {
	switch st.State {
	case jobs.Succeeded:
		return nil
	case jobs.Cancelled:
		return fmt.Errorf("job %s was cancelled", st.ID)
	default:
		if st.Error != "" {
			return fmt.Errorf("job %s %s: %s", st.ID, st.State, st.Error)
		}
		return fmt.Errorf("job %s %s", st.ID, st.State)
	}
}

func newHelperCancelCmd() *cobra.Command {
	var t helperTarget
	cmd := &cobra.Command{
		Use:   "cancel ID",
		Short: "Ask a helper job to stop (it takes effect between its copies; a copy already under way finishes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, _, err := t.open()
			if err != nil {
				return err
			}
			if err := store.Cancel(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "asked job %s to stop\n", args[0])
			return nil
		},
	}
	t.bind(cmd)
	return cmd
}
