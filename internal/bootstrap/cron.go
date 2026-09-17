package bootstrap

import (
	"fmt"
	"os/exec"
	"strings"
)

const reconcilerCronMarker = "reconciler/reconcile.sh"

// currentCrontab returns the invoking user's crontab, or "" if none
// exists yet -- `crontab -l` exits non-zero with no crontab installed,
// which deploy.sh's own `2>/dev/null` already treats as empty, not a
// failure.
func currentCrontab() (string, error) {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return "", nil
		}
		return "", fmt.Errorf("crontab -l: %w", err)
	}
	return string(out), nil
}

// buildCrontab drops any prior line for the reconciler cron job, then
// appends the current one -- idempotent, so re-running deploy never
// accumulates duplicate entries. repoRoot is the reconciler script's own
// absolute path, matching deploy.sh's `$(pwd)` (the repo root it's always
// run from).
func buildCrontab(current, repoRoot string) string {
	var kept []string
	for _, line := range strings.Split(current, "\n") {
		if line != "" && !strings.Contains(line, reconcilerCronMarker) {
			kept = append(kept, line)
		}
	}
	newEntry := fmt.Sprintf("* * * * * %s/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1", repoRoot)
	kept = append(kept, newEntry)
	return strings.Join(kept, "\n") + "\n"
}

// applyReconcilerCron installs (or re-affirms) the cron entry that runs
// the bash reconciler every minute -- unconditionally re-applied on every
// run, same as deploy.sh and the profile steps above, rather than adding
// a skip-if-unchanged check bash itself doesn't have. Deliberately still
// points at reconciler/reconcile.sh, not `tink ingress reconcile` -- moving
// the live cron entry over to tink is its own separate cutover decision,
// not a side effect of porting bootstrap.
func applyReconcilerCron(r *runner, opts Options) error {
	current, err := currentCrontab()
	if err != nil {
		return err
	}
	desired := buildCrontab(current, opts.RepoRoot)
	return r.runWithStdin("installed reconciler cron entry", desired, "crontab", "-")
}
