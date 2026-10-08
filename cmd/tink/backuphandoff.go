package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/jobs"
)

// Handing a `tink backup run` to the helper. The run then happens next to the data, under the helper's supervision, and the machine that
// asked (a laptop that may sleep) only watches: nothing it does or fails to do interrupts a copy, and no volume data passes through it.
//
// The helper has no stack. It runs the copy policies the volumes carry, so a hand-off is "run these volumes", named the way every helper reads names (the label: a bare name in the default project, project/name elsewhere) and is refused when what
// the stack declares for them is not what the volumes carry.

// handoff is a run that can go to the helper: where, and the volumes to name.
type handoff struct {
	Found helper.Found
	Store jobs.Store
	Names []string
}

// prepareHandoff decides where a run goes. It returns the helper to hand it to; or nil and a note saying why the run is to happen here
// instead (empty when there is simply no helper, which is the ordinary case on a host without one); or an error when the run must not
// happen at all. require makes "here instead" an error too, for a caller that must not copy through this machine.
func prepareHandoff(server incus.InstanceServer, items []backuprun.Item, names []string, require bool, now time.Time) (*handoff, string, error) {
	selected, unknown := backuprun.Select(items, names)
	if len(unknown) > 0 {
		return nil, "", fmt.Errorf("%s: not a storage-volume with copies in the stack", strings.Join(unknown, ", "))
	}
	if len(selected) == 0 {
		return nil, "", nil // nothing to run; the local run says so
	}
	elsewhere := func(why string) (*handoff, string, error) {
		if require {
			return nil, "", errors.New(why)
		}
		return nil, why, nil
	}

	found, err := helper.Find(server)
	if err != nil {
		return elsewhere(fmt.Sprintf("could not look for a helper: %v", err))
	}
	switch len(found) {
	case 0:
		if require {
			return nil, "", errors.New("--helper: there is no helper on this server (`tink helper install` adds one)")
		}
		return nil, "", nil
	case 1:
	default:
		labels := make([]string, len(found))
		for i, f := range found {
			labels[i] = f.Label()
		}
		return nil, "", fmt.Errorf("more than one helper on this server (%s): `tink backup run --local` runs it here", strings.Join(labels, ", "))
	}

	report := judgeHelpers(server, found, now)[0]
	if why := helperCannotTake(report); why != "" {
		return elsewhere(fmt.Sprintf("the helper %s cannot take the run (%s)", report.Found.Label(), why))
	}

	eng := backuprun.ServerEngine{Server: server}
	applied, problems, err := backuprun.Discover(eng)
	if err != nil {
		return nil, "", fmt.Errorf("reading the copy policies on the volumes: %w", err)
	}
	if diff := backuprun.Unapplied(selected, applied, problems); len(diff) > 0 {
		return nil, "", fmt.Errorf("the helper runs the copy policies on the volumes, and for these they are not what the stack declares; run `tink plan apply` first (or `--local` to run the stack as it is, from here):\n  - %s",
			strings.Join(diff, "\n  - "))
	}
	if missing := remotesMissing(selected, report.Status); len(missing) > 0 {
		return nil, "", fmt.Errorf("the helper %s has no remote named %s, so it cannot make those copies; add it with a trust token made on that server (incus config trust add helper -q): tink helper remote add NAME --token-file -",
			report.Found.Label(), strings.Join(missing, ", "))
	}

	store, err := helper.JobsStore(server, report.Found)
	if err != nil {
		return elsewhere(err.Error())
	}
	h := &handoff{Found: report.Found, Store: store}
	for _, it := range selected {
		h.Names = append(h.Names, it.Label)
	}
	return h, "", nil
}

// helperCannotTake says why a run cannot go to a helper that is there, or "" when it can. A helper that is merely degraded (a copy is
// failing, a volume is skipped) can take a run: that is when a run by hand is most likely wanted.
func helperCannotTake(r helper.Report) string {
	switch {
	case r.Health == helper.Down:
		return strings.Join(r.Reasons, "; ")
	case r.Status == nil:
		return "it has not published a status document, so it is just starting or too old to take a run"
	case r.Status.JobProto != jobs.Proto:
		return fmt.Sprintf("it speaks job protocol %d and this tink speaks %d; upgrade whichever is older", r.Status.JobProto, jobs.Proto)
	case r.Status.Draining:
		return "it is draining: an upgrade is in progress"
	}
	return ""
}

// remotesMissing are the remotes the copies go to that the helper does not have. A helper that has not said which it has (nil) is not held to it.
func remotesMissing(items []backuprun.Item, st *helper.Status) []string {
	if st == nil || st.Remotes == nil {
		return nil
	}
	have := map[string]bool{}
	for _, r := range st.Remotes {
		have[r.Name] = true
	}
	var missing []string
	said := map[string]bool{}
	for _, it := range items {
		for _, c := range it.Copies {
			if r := c.Target.Remote; r != "" && !have[r] && !said[r] {
				said[r] = true
				missing = append(missing, fmt.Sprintf("%q", r))
			}
		}
	}
	return missing
}

// run queues the job, then follows it until it ends, or until ctx ends, which leaves it running.
func (h *handoff) run(ctx context.Context, due, dryRun bool, out, errOut io.Writer, now time.Time, follow jobs.FollowOptions) error {
	args, err := json.Marshal(daemon.BackupRunArgs{Volumes: h.Names, Due: due, DryRun: dryRun})
	if err != nil {
		return err
	}
	var ahead int
	if list, err := h.Store.List(); err == nil {
		for _, st := range list {
			if !st.State.Finished() {
				ahead++
			}
		}
	}
	id, err := h.Store.Enqueue(jobs.Request{Kind: daemon.KindBackupRun, Origin: jobs.OriginTrigger, Args: args}, now)
	if err != nil {
		return fmt.Errorf("handing the run to the helper %s: %w", h.Found.Label(), err)
	}
	fmt.Fprintf(out, "handed to the helper %s as job %s; it runs there, and nothing here is in its way (Ctrl-C stops following, not the job)\n", h.Found.Label(), id)
	if ahead > 0 {
		fmt.Fprintf(out, "the helper runs one job at a time and has %d ahead of it, so this one starts when they finish\n", ahead)
	}

	ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
	defer stop()
	st, err := jobs.Follow(ctx, h.Store, id, out, follow)
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Fprintf(errOut, "\nstopped following; job %s carries on on the helper.\n  follow it again: tink helper log -f %s\n  stop it:         tink helper cancel %s\n", id, id, id)
		return nil
	case err != nil:
		return err
	}
	return jobOutcome(st)
}
