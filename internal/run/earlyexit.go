package run

import (
	"fmt"
	"io"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/secrets"
)

// Say why when the process is gone (#176).
//
// An instance whose process exits at once (a volume it cannot write to, a missing setting) used to be reported as "started", or, when it has a published port,
// as Incus's `Error occurred when starting proxy device ... pidfd`: the proxy is started after the process, and the process is already gone, so the message
// names a device that was never the problem. The reason is in the instance's console log.
//
// So after a start that succeeded, Run waits a short settle time and asks whether the instance is still running; if it is not, it prints the end of the
// console log and fails. After a start that failed it prints the end of the console log beside the error. The check is a snapshot: an instance with a restart
// policy (boot.autorestart) that crashes in a loop may be running at the moment it is asked.
//
// The console log can hold what the app printed, including a value passed with -e. The values of the environment variables whose names look like secrets (the
// rule that masks them in the recorded command line) are redacted from what is printed.

const (
	// DefaultSettle is how long Run waits after a start before it asks whether the instance is still running.
	DefaultSettle = 2 * time.Second
	// consoleTailLines is how many lines of the console log are printed.
	consoleTailLines = 20
	// consoleReadLimit bounds what is read of the console log; a log longer than this is cut at its start, which is not what a person wants, so the limit is large.
	consoleReadLimit = 4 << 20
)

// secretRedactor holds the values of the environment variables of spec whose names look like secrets.
func secretRedactor(spec *Spec) *secrets.Redactor {
	r := secrets.NewRedactor()
	for k, v := range spec.Config {
		if name, ok := strings.CutPrefix(k, "environment."); ok && secrets.SensitiveKey(name) {
			r.Add(v)
		}
	}
	return r
}

// consoleTail is the last n lines of the instance's console log, redacted, and without trailing blank lines. server must be scoped to the instance's project.
func consoleTail(server incus.InstanceServer, name string, n int, redact *secrets.Redactor) ([]string, error) {
	rc, err := server.GetInstanceConsoleLog(name, &incus.InstanceConsoleLogArgs{})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, consoleReadLimit))
	if err != nil {
		return nil, err
	}
	text := strings.TrimRight(redact.Redact(strings.ReplaceAll(string(b), "\r\n", "\n")), "\n ")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// explainConsole notes the end of the instance's console log, under a line that says what it is. A log that cannot be read is said, not left out, unless the
// instance has none to read (a start that failed before the process existed).
func explainConsole(server incus.InstanceServer, spec *Spec, note func(string, ...any), what string) {
	lines, err := consoleTail(server, spec.Name, consoleTailLines, secretRedactor(spec))
	switch {
	case err != nil && incusapi.IsNotFound(err):
		note("%s; it has no console log to show", what)
	case err != nil:
		note("%s; its console log could not be read: %v", what, err)
	case len(lines) == 0:
		note("%s; its console log is empty", what)
	default:
		note("%s; the last lines of its console log:", what)
		for _, l := range lines {
			note("  %s", l)
		}
	}
}

// checkStillRunning is asked after a start that succeeded. It waits settle, and when the instance is not running prints the end of its console log and returns an
// error. server must be scoped to the instance's project.
func checkStillRunning(server incus.InstanceServer, spec *Spec, settle time.Duration, note func(string, ...any)) error {
	if settle > 0 {
		time.Sleep(settle)
	}
	inst, _, found, err := incusapi.LookupInstance(server, spec.Name)
	switch {
	case err != nil:
		return fmt.Errorf("started %s, but checking that it is still running failed: %w", spec.Name, err)
	case !found && spec.Ephemeral:
		return fmt.Errorf("%s stopped straight after it started and, being ephemeral, was deleted with its console log: run it again without --rm or --ephemeral to see why", spec.Name)
	case !found:
		return fmt.Errorf("%s is gone straight after it started", spec.Name)
	case inst.Status == "Running":
		return nil
	}
	explainConsole(server, spec, note, fmt.Sprintf("%s stopped straight after it started (status %s)", spec.Name, inst.Status))
	return fmt.Errorf("%s stopped straight after it started: the process exited", spec.Name)
}
