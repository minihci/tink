package bootstrap

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runner executes incus/crontab commands, or -- in dry-run mode -- just
// records what it would have run, so a whole deploy pass can be computed
// and reported without ever touching the daemon.
type runner struct {
	dryRun  bool
	actions []string
}

// note records a human-readable line describing something that happened
// (or, in dry-run mode, would happen) -- independent of whether it
// involved running a command at all (e.g. "profile x already exists").
func (r *runner) note(format string, args ...any) {
	r.actions = append(r.actions, fmt.Sprintf(format, args...))
}

// run executes name with args, or in dry-run mode just records the
// command line it would have run. Returns combined stdout+stderr.
func (r *runner) run(description string, name string, args ...string) (string, error) {
	cmdline := name + " " + strings.Join(args, " ")
	if r.dryRun {
		r.note("would run: %s (%s)", cmdline, description)
		return "", nil
	}
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s: %s: %w\n%s", description, cmdline, err, out)
	}
	r.note("%s", description)
	return string(out), nil
}

// runWithStdin is like run, but pipes content to the command's stdin --
// used for `incus profile edit`/`incus config edit`, which read their new
// value from stdin rather than an argument.
func (r *runner) runWithStdin(description string, content string, name string, args ...string) error {
	cmdline := name + " " + strings.Join(args, " ")
	if r.dryRun {
		r.note("would run: %s < (rendered content) (%s)", cmdline, description)
		return nil
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(content)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s: %w\n%s", description, cmdline, err, out.String())
	}
	r.note("%s", description)
	return nil
}

// runWithRetry is like run, but retries on failure -- for operations
// confirmed live to race against a freshly launched or just-crashed
// instance's own state transitions (Incus's daemon-side operation queue,
// not anything this port controls). Two confirmed live: a fresh
// container's `test -d` succeeding doesn't mean file push is ready yet,
// and `incus restart` can collide with an in-flight auto-restart-driven
// stop (a race this project has hit before, manually, with the bash
// script -- see incus-host's own project history). deploy.sh has no
// protection against either; retrying serves what its wait loop and
// restart step were already trying to guarantee, not a deviation from it.
func (r *runner) runWithRetry(description string, name string, args ...string) error {
	if r.dryRun {
		_, err := r.run(description, name, args...)
		return err
	}

	const maxAttempts = 5
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if _, err := r.run(description, name, args...); err != nil {
			lastErr = err
			time.Sleep(time.Second)
			continue
		}
		return nil
	}
	return fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}
