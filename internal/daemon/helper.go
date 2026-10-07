package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/secrets"
)

// KindBackupRun is the job kind that runs `tink backup run`.
const KindBackupRun = "backup-run"

// BackupRunArgs are the arguments of a backup-run job.
type BackupRunArgs struct {
	Volumes []string `json:"volumes,omitempty"`
	Due     bool     `json:"due,omitempty"`
	DryRun  bool     `json:"dry_run,omitempty"`
}

// Helper is the part of the daemon that runs queued work and decides what to queue: the helper design's "scheduler
// and executor", with nothing about containers in it, so any supervisor can run it.
type Helper struct {
	Stacks jobs.Stacks
	Store  jobs.Store
	// Connect opens the Incus engine the work is done through. It is called per job and per scheduler tick, so a
	// connection that died is not kept.
	Connect func() (backuprun.Engine, error)
	// Zone is the time zone schedules are evaluated in (default: local).
	Zone    *time.Location
	Version string
	// SchedulerInterval is how often the scheduler looks for due copies (default 1 minute).
	SchedulerInterval time.Duration
	// ExecutorPoll is how often the job directory is scanned (default 2 seconds).
	ExecutorPoll time.Duration
	Redactor     *secrets.Redactor
	Logf         func(format string, args ...any)
	// Now is a test seam; nil is the real clock in Zone.
	Now func() time.Time
}

func (h *Helper) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	z := h.Zone
	if z == nil {
		z = time.Local
	}
	return time.Now().In(z)
}

func (h *Helper) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

// Executor builds the executor for this helper's jobs.
func (h *Helper) Executor() *jobs.Executor {
	return &jobs.Executor{
		Store:    h.Store,
		Handlers: map[string]jobs.Handler{KindBackupRun: h.backupRun},
		Redactor: h.Redactor,
		Now:      time.Now,
		Poll:     h.ExecutorPoll,
		Logf:     h.Logf,
	}
}

// backupRun is the backup-run job: load the stack the request names (a synced stack, or the bundle it carries) and
// make the copies, through the same code `tink backup run` uses.
func (h *Helper) backupRun(ctx context.Context, j jobs.Job, log io.Writer) (any, error) {
	var args BackupRunArgs
	if len(j.Request.Args) > 0 {
		if err := json.Unmarshal(j.Request.Args, &args); err != nil {
			return nil, fmt.Errorf("the job's arguments: %w", err)
		}
	}
	var resources []resolve.Resource
	switch {
	case j.Request.Stack != "":
		st, err := h.Stacks.Load(j.Request.Stack)
		if err != nil {
			return nil, err
		}
		resources = st.Resources
	case len(j.Request.Entries) > 0:
		for _, e := range j.Request.Entries {
			c, err := jobs.CleanBundlePath(e)
			if err != nil {
				return nil, fmt.Errorf("the job's stack file: %w", err)
			}
			rs, err := resolve.LoadFileConfined(filepath.Join(j.BundleDir, c), j.BundleDir)
			if err != nil {
				return nil, err
			}
			resources = append(resources, rs...)
		}
	default:
		return nil, fmt.Errorf("the job names no stack and carries none")
	}
	if err := backuprun.Check(resources); err != nil {
		return nil, err
	}
	eng, err := h.Connect()
	if err != nil {
		return nil, fmt.Errorf("connecting to incus: %w", err)
	}
	rep, err := backuprun.Run(ctx, eng, resources, backuprun.Options{Volumes: args.Volumes, Due: args.Due, DryRun: args.DryRun, Now: h.now}, log)
	if err != nil {
		return nil, err
	}
	return rep, rep.Err()
}

// Tick is one pass of the scheduler: for every stack synced to the helper, queue a backup-run job if at least one of
// its copies is due and none is already queued or running for that stack. It returns how many it queued. Problems
// (a stack that does not load, a volume that cannot be read) are logged once each, not every tick.
func (h *Helper) Tick(state *SchedulerState) int {
	now := h.now()
	if err := h.Store.Beat(jobs.Heartbeat{Time: now.UTC(), PID: pid(), Version: h.Version, Zone: now.Location().String()}); err != nil {
		h.logf("writing the heartbeat: %v", err)
	}
	state.begin()
	defer state.end(h.logf)
	stacks, bad := h.Stacks.LoadAll()
	for name, err := range bad {
		state.note("stack "+name, fmt.Sprintf("stack %q does not load, and is skipped: %v", name, err), h.logf)
	}
	var eng backuprun.Engine
	queued := 0
	for _, st := range stacks {
		pending, err := h.Store.Pending(KindBackupRun, st.Name)
		if err != nil {
			h.logf("looking for pending jobs: %v", err)
			continue
		}
		if pending {
			continue
		}
		if eng == nil {
			var err error
			if eng, err = h.Connect(); err != nil {
				state.note("incus", fmt.Sprintf("connecting to incus: %v", err), h.logf)
				state.abort()
				return queued
			}
		}
		due, problems, err := backuprun.Due(eng, st.Resources, now)
		if err != nil {
			state.note("stack "+st.Name+" due", fmt.Sprintf("stack %q: %v", st.Name, err), h.logf)
			continue
		}
		for what, perr := range problems {
			state.note("problem "+st.Name+" "+what, fmt.Sprintf("stack %q: %s: %v", st.Name, what, perr), h.logf)
		}
		if len(due) == 0 {
			continue
		}
		args, _ := json.Marshal(BackupRunArgs{Due: true})
		id, err := h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginSchedule, Stack: st.Name, Args: args}, nil, now)
		if err != nil {
			h.logf("queueing a backup for stack %q: %v", st.Name, err)
			continue
		}
		h.logf("stack %q: %d cop(ies) due: queued job %s", st.Name, len(due), id)
		queued++
	}
	return queued
}

// SchedulerState remembers which problems have been reported, so a stack that stays broken is logged when it breaks
// and when it recovers, not once a minute for a week. A tick calls begin, notes what is wrong, and calls end: anything
// that was reported before and was not noted this time has recovered.
type SchedulerState struct {
	mu      sync.Mutex
	seen    map[string]string
	touched map[string]bool
}

func (s *SchedulerState) begin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched = map[string]bool{}
}

func (s *SchedulerState) note(key, msg string, logf func(string, ...any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = map[string]string{}
	}
	if s.touched != nil {
		s.touched[key] = true
	}
	if s.seen[key] == msg {
		return
	}
	s.seen[key] = msg
	if logf != nil {
		logf("%s", msg)
	}
}

// abort ends a tick that could not look at everything (Incus was unreachable): what it did not get to is not
// "recovered", it is merely unknown.
func (s *SchedulerState) abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touched = nil
}

func (s *SchedulerState) end(logf func(string, ...any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touched == nil {
		return
	}
	for key := range s.seen {
		if !s.touched[key] {
			delete(s.seen, key)
			if logf != nil {
				logf("recovered: %s", key)
			}
		}
	}
}
