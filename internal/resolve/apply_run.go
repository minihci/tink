package resolve

import (
	"fmt"
	"strings"
	"sync"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// outcome is how one resource ended an apply.
type outcome int

const (
	outConverged outcome = iota // already matched
	outChanged                  // differences found and fixed
	outBlocked                  // differences found, deliberately not fixed
)

// NotConvergedError is returned when apply finished but left resources
// blocked, so a non-zero exit status means "not converged", not just "failed".
type NotConvergedError struct{ Blocked int }

func (e *NotConvergedError) Error() string {
	return fmt.Sprintf("%d resource(s) not converged (BLOCKED or SKIPPED, see above)", e.Blocked)
}

// Apply converges resources with default options.
func Apply(socket string, resources []Resource) ([]string, error) {
	return ApplyWithOptions(socket, resources, PlanOptions{})
}

// ApplyWithOptions converges resources level by level, every resource within a
// level concurrently. It returns the notes to print (ending with a summary
// line). A resource that errors stops the apply after its level, as before; a
// resource that is blocked does not, but the final error is a *NotConvergedError.
func ApplyWithOptions(socket string, resources []Resource, opts PlanOptions) ([]string, error) {
	levels, err := Levels(resources)
	if err != nil {
		return nil, err
	}
	server, err := incusapi.Connect(socket)
	if err != nil {
		return nil, fmt.Errorf("connecting to incus: %w", err)
	}
	if opts.env == nil {
		opts.env = newImageEnv(opts.Offline)
	}
	opts = opts.withTargets(resources)
	return applyLevels(levels, func(r Resource, note func(string, ...any)) (outcome, error) {
		return applyOne(server, r, opts, note)
	})
}

type applyFunc func(r Resource, note func(string, ...any)) (outcome, error)

// applyLevels is the apply loop without the daemon, so the blocked-propagation
// and exit-status rules can be tested.
func applyLevels(levels [][]Resource, do applyFunc) ([]string, error) {
	var notes []string
	var mu sync.Mutex
	note := func(format string, args ...any) {
		mu.Lock()
		notes = append(notes, fmt.Sprintf(format, args...))
		mu.Unlock()
	}

	var converged, changed, blocked int
	summary := func(failed int) string {
		return fmt.Sprintf("summary: %d converged, %d changed, %d blocked, %d failed", converged, changed, blocked, failed)
	}
	// Instances left blocked. Resources that live INSIDE one (files, execs) are
	// skipped; a mere depends_on is not, since that only means "must exist first".
	blockedInstances := map[string]bool{}

	for _, level := range levels {
		outs := make([]outcome, len(level))
		errs := make([]error, len(level))
		var wg sync.WaitGroup
		for i, r := range level {
			wg.Add(1)
			go func(i int, r Resource) {
				defer wg.Done()
				if (r.Kind == KindFile || r.Kind == KindExec) && blockedInstances[r.Instance] {
					note("%s/%s: SKIPPED: instance/%s is not converged", r.Kind, r.Name, r.Instance)
					outs[i] = outBlocked
					return
				}
				outs[i], errs[i] = do(r, note)
			}(i, r)
		}
		wg.Wait()

		failed := 0
		for i, r := range level {
			if errs[i] != nil {
				failed++
				continue
			}
			switch outs[i] {
			case outConverged:
				converged++
			case outChanged:
				changed++
			case outBlocked:
				blocked++
				if r.Kind == KindInstance {
					blockedInstances[r.Name] = true
				}
			}
		}
		for i, err := range errs {
			if err != nil {
				notes = append(notes, summary(failed))
				return notes, fmt.Errorf("%s/%s: %w", level[i].Kind, level[i].Name, err)
			}
		}
	}

	notes = append(notes, summary(0))
	if blocked > 0 {
		return notes, &NotConvergedError{Blocked: blocked}
	}
	return notes, nil
}

func applyOne(server incus.InstanceServer, r Resource, opts PlanOptions, note func(string, ...any)) (outcome, error) {
	plan, err := planOne(server, r, opts)
	if err != nil {
		return outConverged, err
	}
	for _, w := range plan.Warnings {
		note("%s/%s: warning: %s", r.Kind, r.Name, w)
	}
	switch plan.Action {
	case ActionNone:
		note("%s/%s: no changes", r.Kind, r.Name)
		return outConverged, nil
	case ActionBlocked:
		note("%s/%s: BLOCKED: %s", r.Kind, r.Name, strings.Join(plan.Blocked, "; "))
		return outBlocked, nil
	case ActionCreate:
		if err := createOne(server, r, opts.targets); err != nil {
			return outConverged, err
		}
		if r.Kind == KindExec {
			note("%s/%s: ran %v", r.Kind, r.Name, r.Command)
			return outChanged, nil
		}
		note("%s/%s: created", r.Kind, r.Name)
		return outChanged, nil
	case ActionUpdate:
		if err := updateOne(server, r, opts.targets); err != nil {
			return outConverged, err
		}
		if r.Kind == KindInstance && r.Restart {
			note("%s/%s: updated and restarted (%v)", r.Kind, r.Name, plan.Changes)
			return outChanged, nil
		}
		if r.Kind == KindInstance && !r.Restart && changesEnvironment(plan.Changes) {
			note("%s/%s: updated (%v); its environment changed but it was not restarted, so running processes keep the old values until it is (restart: true does that on apply)", r.Kind, r.Name, plan.Changes)
			return outChanged, nil
		}
		note("%s/%s: updated (%v)", r.Kind, r.Name, plan.Changes)
		return outChanged, nil
	case ActionRebuild:
		rebuildMu.Lock()
		defer rebuildMu.Unlock()
		ops := incusRebuildOps{server: scopedServer(server, r), env: opts.env}
		if err := runRebuild(ops, r, time.Now, note); err != nil {
			return outConverged, err
		}
		return outChanged, nil
	}
	return outConverged, nil
}

// changesEnvironment reports whether any planned change is to an environment.* config key.
func changesEnvironment(changes []string) bool {
	for _, c := range changes {
		if strings.HasPrefix(c, "config.environment.") {
			return true
		}
	}
	return false
}
