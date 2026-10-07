package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
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
	Store jobs.Store
	// Live, if set, is told what each tick found (volumes skipped, copies failing) for the status document.
	Live *Live
	// RetryAfter is how soon the scheduler tries again after a pass that could not look at the volumes (default 5 seconds).
	RetryAfter time.Duration
	// tickLooked is whether the last pass got as far as looking at the volumes.
	tickLooked atomic.Bool
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

// backupRun is the backup-run job: make the copies, through the same code `tink backup run` uses. A job that carries
// stack files (an operator's `daemon enqueue -f`) runs the copies that stack declares, for this job only. Any other job
// runs the copies the volumes' own policies call for, found by listing the server: there is no stack held anywhere to
// disagree with them.
func (h *Helper) backupRun(ctx context.Context, j jobs.Job, log io.Writer) (any, error) {
	var args BackupRunArgs
	if len(j.Request.Args) > 0 {
		if err := json.Unmarshal(j.Request.Args, &args); err != nil {
			return nil, fmt.Errorf("the job's arguments: %w", err)
		}
	}
	opts := backuprun.Options{Volumes: args.Volumes, Due: args.Due, DryRun: args.DryRun, Now: h.now}
	var items []backuprun.Item
	var eng backuprun.Engine
	if len(j.Request.Entries) > 0 {
		var resources []resolve.Resource
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
		var err error
		if items, err = backuprun.FromStack(resources); err != nil {
			return nil, err
		}
		if eng, err = h.Connect(); err != nil {
			return nil, fmt.Errorf("connecting to incus: %w", err)
		}
	} else {
		var err error
		if eng, err = h.Connect(); err != nil {
			return nil, fmt.Errorf("connecting to incus: %w", err)
		}
		var problems map[string]error
		if items, problems, err = backuprun.Discover(eng); err != nil {
			return nil, fmt.Errorf("listing the volumes: %w", err)
		}
		for _, what := range sortedKeys(problems) {
			fmt.Fprintf(log, "skipped %s: %v\n", what, problems[what])
		}
		opts.UnknownMsg = "not a volume with a copy policy on this server"
		opts.EmptyMsg = "nothing to do: no volume on this server carries a copy policy"
	}
	rep, err := backuprun.Run(ctx, eng, items, opts, log)
	if err != nil {
		return nil, err
	}
	return rep, rep.Err()
}

func sortedKeys(m map[string]error) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Tick is one pass of the scheduler: list the volumes that carry a copy policy and, if at least one of their copies is
// due, queue a backup-run job (unless one is already queued or running). It returns how many it queued, 0 or 1. The
// job finds what is due again when it runs, so what it does is never older than the volumes. Problems (a policy that
// cannot be understood, a pool or a volume that cannot be read, Incus unreachable) are logged once each, not every
// tick.
func (h *Helper) Tick(state *SchedulerState) int {
	h.tickLooked.Store(false)
	now := h.now()
	if err := h.Store.Beat(jobs.Heartbeat{Time: now.UTC(), PID: pid(), Version: h.Version, Zone: now.Location().String()}); err != nil {
		h.logf("writing the heartbeat: %v", err)
	}
	state.begin()
	defer state.end(h.logf)
	eng, err := h.Connect()
	if err != nil {
		state.note("incus", fmt.Sprintf("connecting to incus: %v", err), h.logf)
		state.abort()
		return 0
	}
	items, problems, err := backuprun.Discover(eng)
	if err != nil {
		state.note("incus", fmt.Sprintf("listing the volumes: %v", err), h.logf)
		state.abort()
		return 0
	}
	for _, what := range sortedKeys(problems) {
		state.note("problem "+what, fmt.Sprintf("%s: %v", what, problems[what]), h.logf)
	}
	assessed := backuprun.Assess(eng, items, now)
	due, dueProblems := assessed.Due, assessed.Problems
	for _, what := range sortedKeys(dueProblems) {
		state.note("problem "+what, fmt.Sprintf("%s: %v", what, dueProblems[what]), h.logf)
	}
	h.tickLooked.Store(true)
	if h.Live != nil {
		h.Live.setBackup(skipsFrom(problems, dueProblems), failingFrom(assessed.Failing))
	}
	if len(due) == 0 {
		return 0
	}
	if h.Store.Draining() {
		state.note("draining", "draining: no new backup job is queued while it lasts", h.logf)
		return 0
	}
	pending, err := h.Store.Pending(KindBackupRun)
	if err != nil {
		h.logf("looking for pending jobs: %v", err)
		return 0
	}
	if pending {
		return 0
	}
	args, _ := json.Marshal(BackupRunArgs{Due: true})
	id, err := h.Store.Enqueue(jobs.Request{Kind: KindBackupRun, Origin: jobs.OriginSchedule, Args: args}, nil, now)
	if err != nil {
		h.logf("queueing a backup: %v", err)
		return 0
	}
	h.logf("%d cop(ies) due: queued job %s", len(due), id)
	return 1
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
