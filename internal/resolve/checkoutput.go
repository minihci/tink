package resolve

import (
	"fmt"
	"strings"
)

// An exec resource's check_output guard: the work is done when a command's output equals the declared content of a file resource. It is for an
// engine with a get/set pair (Headscale's `policy get` and `policy set`): the live state can be read back, so it is compared with what the stack
// declares, which heals from a change made by hand where a remembered trigger hash cannot. See docs/exec-output-check-design.md.

// maxCheckOutput bounds what is compared. A policy or a loaded configuration is a few kilobytes.
const maxCheckOutput = 1 << 20

// OutputCheck is the check_output guard.
type OutputCheck struct {
	// Command is run inside the exec's instance. Non-zero means "not done yet", as for Check.
	Command []string
	// Equals names the kind: file resource whose declared content the command's output must equal.
	Equals string
	// Expected is that content. It is filled in by the graph builder (resolveCheckOutputs), never read from YAML.
	Expected string
}

// outputMatches compares live output with declared content, ignoring trailing whitespace on both: `headscale policy get` prints the policy it holds
// plus one newline, byte for byte otherwise (found live). Anything else, CRLF included, is compared as written.
func outputMatches(got, want string) bool {
	return strings.TrimRight(got, " \t\r\n") == strings.TrimRight(want, " \t\r\n")
}

// outputCheckConverged runs the check through run and says whether the work is already done, and if not, why. A failed run is an error, not
// "not done": the exec would otherwise run its command on the strength of a check that never answered.
func outputCheckConverged(run func(command []string) (exitCode int, output string, err error), c *OutputCheck) (converged bool, why string, err error) {
	code, out, err := run(c.Command)
	if err != nil {
		return false, "", err
	}
	if code != 0 {
		return false, fmt.Sprintf("the check %v exited %d", c.Command, code), nil
	}
	if len(out) > maxCheckOutput {
		return false, "", fmt.Errorf("the output of %v is over %d bytes, too much to compare with file/%s", c.Command, maxCheckOutput, c.Equals)
	}
	if outputMatches(out, c.Expected) {
		return true, "", nil
	}
	return false, fmt.Sprintf("the output of %v differs from file/%s", c.Command, c.Equals), nil
}

// validateCheckOutputs rejects a check_output whose equals does not name a file resource in the stack, on the same instance, with declared content.
func validateCheckOutputs(resources []Resource) error {
	byName := map[string]Resource{}
	names := map[string]bool{}
	for _, r := range resources {
		if r.Kind != KindStack {
			byName[r.Name] = r
			names[r.Name] = true
		}
	}
	for _, r := range resources {
		if r.Kind != KindExec || r.CheckOutput == nil {
			continue
		}
		eq := r.CheckOutput.Equals
		f, ok := byName[eq]
		if !ok {
			hint := ""
			if near := closestName(eq, names); near != "" {
				hint = fmt.Sprintf(" (did you mean %q?)", near)
			}
			return fmt.Errorf("resource %q: check_output.equals names %q, which is not a resource in this stack%s", r.Name, eq, hint)
		}
		if f.Kind != KindFile {
			return fmt.Errorf("resource %q: check_output.equals names %q, which is a %s, not a file: it must name the kind: file resource whose content the output is compared with", r.Name, eq, f.Kind)
		}
		if f.Instance != r.Instance {
			return fmt.Errorf("resource %q: check_output.equals names file %q, which is placed on instance %q, but this exec runs on %q", r.Name, eq, f.Instance, r.Instance)
		}
		if f.Content == "" {
			return fmt.Errorf("resource %q: check_output.equals names file %q, which has no declared content (content or source_path) to compare with", r.Name, eq)
		}
	}
	return nil
}

// resolveCheckOutputs copies each file's declared content into the check_output that names it. Like the other passes it updates the resource in place,
// but it gives the resource a copy of the OutputCheck rather than writing through the one it held.
func resolveCheckOutputs(all map[string]*Resource) {
	for _, r := range all {
		if r.Kind != KindExec || r.CheckOutput == nil {
			continue
		}
		if f, ok := all[r.CheckOutput.Equals]; ok {
			c := *r.CheckOutput
			c.Expected = f.Content
			r.CheckOutput = &c
		}
	}
}
