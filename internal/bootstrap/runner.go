package bootstrap

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// runner executes incus/crontab commands, or -- in dry-run mode -- just
// records what it would have run, so a whole apply pass can be computed
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

// incusListNames runs `incus <kind> list -f csv -c n` and returns the
// resulting names -- read-only, always actually runs even in dry-run mode,
// since dry-run needs real current state to report accurate plans against.
func incusListNames(kind string) (map[string]bool, error) {
	out, err := exec.Command("incus", kind, "list", "-f", "csv", "-c", "n").Output()
	if err != nil {
		return nil, fmt.Errorf("incus %s list: %w", kind, err)
	}
	names := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names[line] = true
		}
	}
	return names, nil
}
