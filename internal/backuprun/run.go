// Package backuprun performs `tink backup run`: for the volumes of a stack that declare copies, decide which copies
// to make, make them, and report what happened. The CLI and the helper's job executor both call it, so a copy made
// from either behaves identically.
package backuprun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

// Engine is what Run needs from Incus: a volume's live configuration (where the stamps are) and a copy. A seam, so
// the decisions here are tested without a server.
type Engine interface {
	LiveConfig(v volbackup.Volume) (map[string]string, error)
	Copy(v volbackup.Volume, t volbackup.Target, opts volbackup.CopyOptions) (volbackup.CopyResult, error)
}

// ServerEngine is the Engine over a connection to an Incus server.
type ServerEngine struct{ Server incus.InstanceServer }

func (e ServerEngine) LiveConfig(v volbackup.Volume) (map[string]string, error) {
	return volbackup.LiveConfig(e.Server, v)
}

func (e ServerEngine) Copy(v volbackup.Volume, t volbackup.Target, opts volbackup.CopyOptions) (volbackup.CopyResult, error) {
	return volbackup.Copy(e.Server, v, t, opts)
}

// Options say what to run.
type Options struct {
	// Volumes limits the run to these volumes; empty means every volume that declares copies.
	Volumes []string
	// Due runs only the copies whose schedule has come round since their last success, and that are not backing off
	// after failures.
	Due    bool
	DryRun bool
	Now    func() time.Time
	// Remote is the Incus remote tink is pointed at, if any: a copy to ANOTHER server is relayed through the
	// machine running tink, which from a remote context may not be near the data, and Run says so.
	Remote string
}

// Outcome is what happened to one copy.
type Outcome string

const (
	Copied  Outcome = "copied"
	Failed  Outcome = "failed"
	Skipped Outcome = "skipped" // not due, backing off, or already running
	Planned Outcome = "planned" // a dry run
)

// CopyReport is one volume-to-target copy.
type CopyReport struct {
	Volume  string  `json:"volume"`
	Target  string  `json:"target,omitempty"`
	Outcome Outcome `json:"outcome"`
	// Detail is the reason for a skip, or the error of a failure, as printed.
	Detail       string   `json:"detail,omitempty"`
	RestorePoint string   `json:"restore_point,omitempty"`
	Pruned       []string `json:"pruned,omitempty"`
	OtherServers []string `json:"other_servers,omitempty"`
}

// Report is everything a run did.
type Report struct {
	Copies []CopyReport `json:"copies"`
	// Unknown are the volumes asked for that are not a storage-volume with copies in the stack.
	Unknown []string `json:"unknown,omitempty"`
	Failed  int      `json:"failed"`
	Tried   int      `json:"tried"`
	Skipped int      `json:"skipped"`
}

// Err is the error a run with failures should end with, or nil.
func (r Report) Err() error {
	if r.Failed > 0 {
		return fmt.Errorf("%d copy operation(s) failed", r.Failed)
	}
	return nil
}

// Check says whether a stack can be run at all: it is not empty and its references (a copy's target) resolve. Callers
// run it before connecting to anything, so a bad stack is reported as a bad stack.
func Check(resources []resolve.Resource) error {
	if len(resources) == 0 {
		return fmt.Errorf("no stack: give one with -f, or run from the directory holding %s", resolve.DefaultFile)
	}
	if _, err := resolve.Levels(resources); err != nil { // validates references, e.g. an unknown copy target
		return err
	}
	return nil
}

// Select picks the volumes a run acts on: every storage volume that declares copies, or with names, only those.
// Names that are not such a volume come back in unknown, once each, in the order given, so asking for a volume
// that cannot be backed up is an error and not silently nothing.
func Select(resources []resolve.Resource, names []string) (selected []resolve.Resource, unknown []string) {
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

// Run makes the copies. Everything it would print goes to out as it happens. A copy that fails does not stop the
// others; ctx cancels between copies (a copy already in flight finishes). The error is for what stops the run
// altogether (a stack that does not validate); failed copies are in the Report.
func Run(ctx context.Context, eng Engine, resources []resolve.Resource, opts Options, out io.Writer) (Report, error) {
	if err := Check(resources); err != nil {
		return Report{}, err
	}
	targets := map[string]resolve.Resource{}
	for _, r := range resources {
		if r.Kind == resolve.KindBackupTarget {
			targets[r.Name] = r
		}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	selected, unknown := Select(resources, opts.Volumes)

	var rep Report
	rep.Unknown = unknown
	warnedRelay := map[string]bool{}
	add := func(c CopyReport) { rep.Copies = append(rep.Copies, c) }

	for _, r := range selected {
		v := volbackup.Volume{Project: r.Project, Pool: r.Pool, Name: r.Name}
		live, err := eng.LiveConfig(v)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", r.Name, err)
			rep.Failed++
			add(CopyReport{Volume: r.Name, Outcome: Failed, Detail: err.Error()})
			continue
		}
		for _, c := range r.Backup.Copies {
			if err := ctx.Err(); err != nil {
				fmt.Fprintf(out, "%s -> %s: stopped (%v)\n", r.Name, c.Target, err)
				rep.Skipped++
				add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Skipped, Detail: "stopped: " + err.Error()})
				continue
			}
			if opts.Due {
				decision, err := resolve.CopyDue(c.Schedule, live, c.Target, now())
				if err != nil {
					fmt.Fprintf(out, "%s -> %s: %v\n", r.Name, c.Target, err)
					rep.Failed++
					add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Failed, Detail: err.Error()})
					continue
				}
				if !decision.Due {
					fmt.Fprintf(out, "%s -> %s: %s\n", r.Name, c.Target, decision.Reason)
					rep.Skipped++
					add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Skipped, Detail: decision.Reason})
					continue
				}
			}
			rep.Tried++
			// A copy to ANOTHER server is relayed through the process that runs it.
			if t := targets[c.Target]; opts.Remote != "" && t.Remote != "" && !warnedRelay[c.Target] {
				warnedRelay[c.Target] = true
				fmt.Fprintf(out, "note: tink is pointed at the remote %q, and the copy to %q (remote %q) is relayed through this machine, so the volume's data passes through it\n", opts.Remote, c.Target, t.Remote)
			}
			res, err := eng.Copy(v, volbackup.TargetFrom(targets[c.Target]), volbackup.CopyOptions{Retain: c.Retain, DryRun: opts.DryRun, Now: now, Progress: out})
			if errors.Is(err, volbackup.ErrBusy) {
				rep.Tried--
				rep.Skipped++
				fmt.Fprintf(out, "%s -> %s: already running\n", r.Name, c.Target)
				add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Skipped, Detail: "already running"})
				continue
			}
			if err != nil {
				fmt.Fprintf(out, "FAILED %s -> %s: %v\n", r.Name, c.Target, err)
				rep.Failed++
				add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Failed, Detail: err.Error()})
				continue
			}
			if opts.DryRun {
				for _, p := range res.Planned {
					fmt.Fprintf(out, "%s -> %s: would %s\n", r.Name, c.Target, p)
				}
				add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Planned})
				continue
			}
			if len(res.OtherServers) > 0 {
				fmt.Fprintf(out, "note: %s -> %s also holds restore points of a volume with this name made by other server(s) (%s); tink leaves them alone\n", r.Name, c.Target, strings.Join(res.OtherServers, ", "))
			}
			fmt.Fprintf(out, "copied %s -> %s: restore point %s", r.Name, c.Target, res.Volume)
			if len(res.Pruned) > 0 {
				fmt.Fprintf(out, " (pruned %d older: %s)", len(res.Pruned), strings.Join(res.Pruned, ", "))
			}
			fmt.Fprintln(out)
			add(CopyReport{Volume: r.Name, Target: c.Target, Outcome: Copied, RestorePoint: res.Volume, Pruned: res.Pruned, OtherServers: res.OtherServers})
		}
	}
	for _, name := range unknown {
		fmt.Fprintf(out, "%s: not a storage-volume with copies in the stack\n", name)
		rep.Failed++
	}
	if rep.Tried == 0 && rep.Skipped == 0 && rep.Failed == 0 {
		fmt.Fprintln(out, "nothing to do: no volume in the stack declares copies")
	}
	return rep, nil
}

// DueCopy is a copy that should run now.
type DueCopy struct{ Volume, Target string }

// Due lists the copies a `Due` run would make now, without making any: what a scheduler asks before it queues work, so
// it queues a job only when there is something to do. It applies the same decision as Run (the schedule, and the
// backoff after failures). A volume whose live configuration cannot be read is reported in problems, not skipped
// silently, and is not "due" (nothing can be decided about it).
func Due(eng Engine, resources []resolve.Resource, now time.Time) (due []DueCopy, problems map[string]error, err error) {
	if err := Check(resources); err != nil {
		return nil, nil, err
	}
	selected, _ := Select(resources, nil)
	problems = map[string]error{}
	for _, r := range selected {
		live, lerr := eng.LiveConfig(volbackup.Volume{Project: r.Project, Pool: r.Pool, Name: r.Name})
		if lerr != nil {
			problems[r.Name] = lerr
			continue
		}
		for _, c := range r.Backup.Copies {
			d, derr := resolve.CopyDue(c.Schedule, live, c.Target, now)
			if derr != nil {
				problems[r.Name+" -> "+c.Target] = derr
				continue
			}
			if d.Due {
				due = append(due, DueCopy{Volume: r.Name, Target: c.Target})
			}
		}
	}
	return due, problems, nil
}
