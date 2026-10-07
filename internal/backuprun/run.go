// Package backuprun performs `tink backup run`: for the volumes that declare copies, decide which copies to make,
// make them, and report what happened. The volumes come from a stack file (the CLI, or a job that carries one) or from
// the copy policies the volumes themselves carry (the helper's scheduler and its jobs); the work is the same either
// way, so a copy made from any of them behaves identically.
package backuprun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

// Engine is what Run needs from Incus: a volume's live configuration (where the stamps are), a copy, and a listing of
// the volumes (where the policies are). A seam, so the decisions here are tested without a server.
type Engine interface {
	LiveConfig(v volbackup.Volume) (map[string]string, error)
	// Volumes lists every custom volume with its config; a pool that could not be listed is in the map, by name.
	Volumes() ([]volbackup.ListedVolume, map[string]error, error)
	Copy(v volbackup.Volume, t volbackup.Target, opts volbackup.CopyOptions) (volbackup.CopyResult, error)
}

// ServerEngine is the Engine over a connection to an Incus server.
type ServerEngine struct{ Server incus.InstanceServer }

func (e ServerEngine) LiveConfig(v volbackup.Volume) (map[string]string, error) {
	return volbackup.LiveConfig(e.Server, v)
}

func (e ServerEngine) Volumes() ([]volbackup.ListedVolume, map[string]error, error) {
	return volbackup.ListVolumes(e.Server)
}

func (e ServerEngine) Copy(v volbackup.Volume, t volbackup.Target, opts volbackup.CopyOptions) (volbackup.CopyResult, error) {
	return volbackup.Copy(e.Server, v, t, opts)
}

// Options say what to run.
type Options struct {
	// Volumes limits the run to these volumes (by name, or project/name); empty means every volume that declares copies.
	Volumes []string
	// Due runs only the copies whose schedule has come round since their last success, and that are not backing off
	// after failures.
	Due    bool
	DryRun bool
	Now    func() time.Time
	// Remote is the Incus remote tink is pointed at, if any: a copy to ANOTHER server is relayed through the
	// machine running tink, which from a remote context may not be near the data, and Run says so.
	Remote string
	// UnknownMsg and EmptyMsg say what a name that is not a volume with copies, and a run with nothing to do, mean in the
	// place the volumes came from. The defaults are for a stack.
	UnknownMsg, EmptyMsg string
}

const (
	stackUnknownMsg = "not a storage-volume with copies in the stack"
	stackEmptyMsg   = "nothing to do: no volume in the stack declares copies"
)

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
	Swept        []string `json:"swept,omitempty"`
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

// Item is one volume with copies to make: where it is, what to call it in messages, and what it is copied to.
type Item struct {
	Volume volbackup.Volume
	// Label names the volume in output and reports: its name, or project/name when it is not in the default project.
	Label  string
	Copies []Copy
}

// Copy is one copy of an Item: where to, how often, and how long restore points are kept there.
type Copy struct {
	Target   volbackup.Target
	Schedule string
	Retain   string
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

// FromStack is the volumes of a stack that declare copies, in stack order, after Check.
func FromStack(resources []resolve.Resource) ([]Item, error) {
	if err := Check(resources); err != nil {
		return nil, err
	}
	targets := map[string]resolve.Resource{}
	for _, r := range resources {
		if r.Kind == resolve.KindBackupTarget {
			targets[r.Name] = r
		}
	}
	var items []Item
	for _, r := range resources {
		if r.Kind != resolve.KindStorageVolume || r.Backup == nil || len(r.Backup.Copies) == 0 {
			continue
		}
		it := Item{Volume: volbackup.Volume{Project: r.Project, Pool: r.Pool, Name: r.Name}, Label: r.Name}
		for _, c := range r.Backup.Copies {
			it.Copies = append(it.Copies, Copy{Target: volbackup.TargetFrom(targets[c.Target]), Schedule: c.Schedule, Retain: c.Retain})
		}
		items = append(items, it)
	}
	return items, nil
}

// Discover is the volumes that carry a copy policy, found by listing the server: what the helper's scheduler and its
// jobs work from, so there is no stack to keep in step with the volumes.
//
// A volume is skipped, and reported in problems under "project/name", when its policy cannot be understood (another
// protocol, a field that is not known, a schedule that cannot be read): acting on half of a policy is worse than not
// acting. A pool that could not be listed is a problem under "pool NAME", and the others are still listed. A restore
// point or an unfinished copy is never an item, whatever config it carries, so a backup is not scheduled for backup.
func Discover(eng Engine) (items []Item, problems map[string]error, err error) {
	vols, poolErrs, err := eng.Volumes()
	if err != nil {
		return nil, nil, err
	}
	problems = map[string]error{}
	for pool, perr := range poolErrs {
		problems["pool "+pool] = perr
	}
	for _, lv := range vols {
		text, has := lv.Config[backupmeta.PolicyKey]
		if !has {
			continue
		}
		if _, isPoint := lv.Config[backupmeta.MarkerCopyOf]; isPoint {
			continue
		}
		if _, isPartial := lv.Config[backupmeta.MarkerPartialOf]; isPartial {
			continue
		}
		label := Label(lv.Volume)
		p, perr := resolve.ParsePolicy(text)
		if perr != nil {
			problems[label] = perr
			continue
		}
		if len(p.Copies) == 0 {
			continue // a policy that only says how to verify has nothing to copy
		}
		it := Item{Volume: lv.Volume, Label: label}
		for _, c := range p.Copies {
			it.Copies = append(it.Copies, Copy{
				Target:   volbackup.Target{Name: c.Target.Name, Pool: c.Target.Pool, Remote: c.Target.Remote},
				Schedule: c.Schedule,
				Retain:   c.Retain,
			})
		}
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items, problems, nil
}

// Orphan is a volume that carries a copy policy, and so is being copied, but that the stack does not declare.
type Orphan struct {
	Volume volbackup.Volume
	// Owned is true when the volume points back at this stack (its user.tink.stack names it): it is certain to be this
	// stack's to let go. False means it points at no stack at all and only sits where this stack's volumes sit.
	Owned bool
}

// Undeclared lists the volumes that carry a copy policy but that the stack does not declare: a volume taken out of the
// YAML keeps the policy `apply` wrote, because tink never removes what it is no longer told about, and a policy that is
// no longer wanted should not go on silently.
//
// When the stack names itself (kind: stack), `apply` stamped each volume with that name, so the answer is exact: every
// volume stamped with it that the stack no longer declares, wherever it is. A volume stamped with ANOTHER stack's name is
// never listed.
//
// A volume with no stamp (applied before stacks were named, or by a stack that is not) can only be guessed at: those in
// the projects and pools the stack declares volumes in, which on a server with several stacks may belong to another one.
// They come back with Owned false, and callers present them as a note, not a warning. Restore points and unfinished
// copies are not volumes in their own right and are never listed. The result is sorted by Label.
func Undeclared(eng Engine, resources []resolve.Resource) ([]Orphan, error) {
	stack, err := resolve.StackName(resources)
	if err != nil {
		return nil, err
	}
	scope, declared := map[string]bool{}, map[string]bool{}
	for _, r := range resources {
		if r.Kind != resolve.KindStorageVolume {
			continue
		}
		v := volbackup.Volume{Project: r.Project, Pool: r.Pool, Name: r.Name}
		scope[placeOf(v)] = true
		declared[placeOf(v)+"/"+v.Name] = true
	}
	if len(scope) == 0 && stack == "" {
		return nil, nil
	}
	vols, _, err := eng.Volumes()
	if err != nil {
		return nil, err
	}
	var out []Orphan
	for _, lv := range vols {
		if _, has := lv.Config[backupmeta.PolicyKey]; !has {
			continue
		}
		if _, isPoint := lv.Config[backupmeta.MarkerCopyOf]; isPoint {
			continue
		}
		if _, isPartial := lv.Config[backupmeta.MarkerPartialOf]; isPartial {
			continue
		}
		if declared[placeOf(lv.Volume)+"/"+lv.Volume.Name] {
			continue
		}
		switch owner := lv.Config[backupmeta.StackKey]; {
		case owner != "" && owner == stack:
			out = append(out, Orphan{Volume: lv.Volume, Owned: true})
		case owner != "":
			// another stack's, or this stack's before it was named differently: not ours to say anything about
		case scope[placeOf(lv.Volume)]:
			out = append(out, Orphan{Volume: lv.Volume})
		}
	}
	sort.Slice(out, func(i, j int) bool { return Label(out[i].Volume) < Label(out[j].Volume) })
	return out, nil
}

// placeOf is the project and pool a volume is in, with the defaults spelled out so a stack that leaves them out matches
// a listing that does not.
func placeOf(v volbackup.Volume) string {
	project, pool := v.Project, v.Pool
	if project == "" {
		project = "default"
	}
	if pool == "" {
		pool = "default"
	}
	return project + "/" + pool
}

// Label names a volume the way reports and `tink backup forget` do: its name, or project/name outside the default
// project.
func Label(v volbackup.Volume) string {
	if v.Project == "" || v.Project == "default" {
		return v.Name
	}
	return v.Project + "/" + v.Name
}

// Select picks the items a run acts on: every one, or with names, only those. A name matches an item's label or its
// bare volume name. Names that match nothing come back in unknown, once each, in the order given, so asking for a
// volume that cannot be backed up is an error and not silently nothing.
func Select(items []Item, names []string) (selected []Item, unknown []string) {
	asked := map[string]bool{}
	for _, n := range names {
		asked[n] = true
	}
	found := map[string]bool{}
	for _, it := range items {
		if len(names) > 0 && !asked[it.Label] && !asked[it.Volume.Name] {
			continue
		}
		found[it.Label], found[it.Volume.Name] = true, true
		selected = append(selected, it)
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
// altogether; failed copies are in the Report.
func Run(ctx context.Context, eng Engine, items []Item, opts Options, out io.Writer) (Report, error) {
	if opts.UnknownMsg == "" {
		opts.UnknownMsg = stackUnknownMsg
	}
	if opts.EmptyMsg == "" {
		opts.EmptyMsg = stackEmptyMsg
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	selected, unknown := Select(items, opts.Volumes)

	var rep Report
	rep.Unknown = unknown
	warnedRelay := map[string]bool{}
	add := func(c CopyReport) { rep.Copies = append(rep.Copies, c) }

	for _, it := range selected {
		live, err := eng.LiveConfig(it.Volume)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", it.Label, err)
			rep.Failed++
			add(CopyReport{Volume: it.Label, Outcome: Failed, Detail: err.Error()})
			continue
		}
		for _, c := range it.Copies {
			name := c.Target.Name
			if err := ctx.Err(); err != nil {
				fmt.Fprintf(out, "%s -> %s: stopped (%v)\n", it.Label, name, err)
				rep.Skipped++
				add(CopyReport{Volume: it.Label, Target: name, Outcome: Skipped, Detail: "stopped: " + err.Error()})
				continue
			}
			if opts.Due {
				decision, err := backupmeta.CopyDue(c.Schedule, live, name, now())
				if err != nil {
					fmt.Fprintf(out, "%s -> %s: %v\n", it.Label, name, err)
					rep.Failed++
					add(CopyReport{Volume: it.Label, Target: name, Outcome: Failed, Detail: err.Error()})
					continue
				}
				if !decision.Due {
					fmt.Fprintf(out, "%s -> %s: %s\n", it.Label, name, decision.Reason)
					rep.Skipped++
					add(CopyReport{Volume: it.Label, Target: name, Outcome: Skipped, Detail: decision.Reason})
					continue
				}
			}
			rep.Tried++
			// A copy to ANOTHER server is relayed through the process that runs it.
			if opts.Remote != "" && c.Target.Remote != "" && !warnedRelay[name] {
				warnedRelay[name] = true
				fmt.Fprintf(out, "note: tink is pointed at the remote %q, and the copy to %q (remote %q) is relayed through this machine, so the volume's data passes through it\n", opts.Remote, name, c.Target.Remote)
			}
			res, err := eng.Copy(it.Volume, c.Target, volbackup.CopyOptions{Retain: c.Retain, DryRun: opts.DryRun, Now: now, Progress: out})
			if errors.Is(err, volbackup.ErrBusy) {
				rep.Tried--
				rep.Skipped++
				fmt.Fprintf(out, "%s -> %s: already running\n", it.Label, name)
				add(CopyReport{Volume: it.Label, Target: name, Outcome: Skipped, Detail: "already running"})
				continue
			}
			if err != nil {
				fmt.Fprintf(out, "FAILED %s -> %s: %v\n", it.Label, name, err)
				rep.Failed++
				add(CopyReport{Volume: it.Label, Target: name, Outcome: Failed, Detail: err.Error()})
				continue
			}
			if opts.DryRun {
				for _, p := range res.Planned {
					fmt.Fprintf(out, "%s -> %s: would %s\n", it.Label, name, p)
				}
				add(CopyReport{Volume: it.Label, Target: name, Outcome: Planned})
				continue
			}
			if len(res.OtherServers) > 0 {
				fmt.Fprintf(out, "note: %s -> %s also holds restore points of a volume with this name made by other server(s) (%s); tink leaves them alone\n", it.Label, name, strings.Join(res.OtherServers, ", "))
			}
			fmt.Fprintf(out, "copied %s -> %s: restore point %s", it.Label, name, res.Volume)
			if len(res.Pruned) > 0 {
				fmt.Fprintf(out, " (pruned %d older: %s)", len(res.Pruned), strings.Join(res.Pruned, ", "))
			}
			if len(res.Swept) > 0 {
				fmt.Fprintf(out, " (removed %d abandoned partial cop(ies): %s)", len(res.Swept), strings.Join(res.Swept, ", "))
			}
			fmt.Fprintln(out)
			add(CopyReport{Volume: it.Label, Target: name, Outcome: Copied, RestorePoint: res.Volume, Pruned: res.Pruned, Swept: res.Swept, OtherServers: res.OtherServers})
		}
	}
	for _, name := range unknown {
		fmt.Fprintf(out, "%s: %s\n", name, opts.UnknownMsg)
		rep.Failed++
	}
	if rep.Tried == 0 && rep.Skipped == 0 && rep.Failed == 0 {
		fmt.Fprintln(out, opts.EmptyMsg)
	}
	return rep, nil
}

// DueCopy is a copy that should run now.
type DueCopy struct{ Volume, Target string }

// FailingCopy is a copy that has failed since its last success.
type FailingCopy struct {
	Volume, Target string
	Count          int
	Since          time.Time
}

// Assessment is what a look at the volumes' live state finds: the copies that are due, the ones failing, and what could not be
// decided.
type Assessment struct {
	Due     []DueCopy
	Failing []FailingCopy
	// Problems are a volume whose live configuration could not be read (keyed by its label) and a copy whose schedule could
	// not be read (keyed "label -> target"). Neither is "due", and neither is silent.
	Problems map[string]error
}

// Assess looks at each item's live state once: what is due, what is failing, and what could not be decided. Due is the part a
// scheduler queues work from; Failing is what the helper reports about itself.
func Assess(eng Engine, items []Item, now time.Time) Assessment {
	a := Assessment{Problems: map[string]error{}}
	for _, it := range items {
		live, lerr := eng.LiveConfig(it.Volume)
		if lerr != nil {
			a.Problems[it.Label] = lerr
			continue
		}
		for _, c := range it.Copies {
			if f, ok := backupmeta.FailureOf(live, c.Target.Name); ok {
				a.Failing = append(a.Failing, FailingCopy{Volume: it.Label, Target: c.Target.Name, Count: f.N, Since: f.At})
			}
			d, derr := backupmeta.CopyDue(c.Schedule, live, c.Target.Name, now)
			if derr != nil {
				a.Problems[it.Label+" -> "+c.Target.Name] = derr
				continue
			}
			if d.Due {
				a.Due = append(a.Due, DueCopy{Volume: it.Label, Target: c.Target.Name})
			}
		}
	}
	return a
}

// Due lists the copies a `Due` run would make now, without making any: what a scheduler asks before it queues work, so
// it queues a job only when there is something to do. It applies the same decision as Run (the schedule, and the
// backoff after failures). A volume whose live configuration cannot be read is reported in problems, not skipped
// silently, and is not "due" (nothing can be decided about it).
func Due(eng Engine, items []Item, now time.Time) (due []DueCopy, problems map[string]error) {
	a := Assess(eng, items, now)
	return a.Due, a.Problems
}
