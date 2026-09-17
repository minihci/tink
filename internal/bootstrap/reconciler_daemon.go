package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/minihci/tink/internal/daemon"
)

const legacyReconcilerCronMarker = "reconciler/reconcile.sh"

// withoutLegacyReconcilerLine filters the legacy bash reconciler's cron
// line out of current, reporting whether it was actually present.
func withoutLegacyReconcilerLine(current string) (result string, found bool) {
	var kept []string
	for _, line := range strings.Split(current, "\n") {
		if line == "" {
			continue
		}
		if strings.Contains(line, legacyReconcilerCronMarker) {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if len(kept) == 0 {
		return "", found
	}
	return strings.Join(kept, "\n") + "\n", found
}

// removeLegacyReconcilerCron drops any prior cron entry for the bash
// reconciler, if one exists -- a host that ever ran an older version of
// this step (or deploy.sh itself) may still have it, and leaving it in
// place would mean two things reconciling the same ingress routes.
// Idempotent: a no-op crontab rewrite on a host that never had one.
func removeLegacyReconcilerCron() (changed bool, err error) {
	out, err := exec.Command("crontab", "-l").Output()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return false, nil // no crontab at all -- nothing to remove
		}
		return false, fmt.Errorf("crontab -l: %w", err)
	}

	desired, found := withoutLegacyReconcilerLine(string(out))
	if !found {
		return false, nil
	}

	cmd := exec.Command("crontab", "-")
	cmd.Stdin = strings.NewReader(desired)
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("removing legacy reconciler cron entry: %w", err)
	}
	return true, nil
}

// applyReconcilerDaemon supersedes the old cron-based reconciler: it
// removes any legacy cron entry left over from an older run, then
// installs and enables "tink daemon run" under whatever init system the
// host actually runs -- real restart-on-crash/start-on-boot supervision,
// which cron never provided. Falls back to a clear warning (not a failed
// deploy) if neither systemd nor OpenRC is detected, since deploy has
// never required a specific init system to succeed.
func applyReconcilerDaemon(r *runner, opts Options) error {
	if removed, err := removeLegacyReconcilerCronStep(r); err != nil {
		return err
	} else if removed {
		r.note("removed legacy reconciler cron entry")
	}

	initSystem := daemon.DetectInit()
	if initSystem == "" {
		r.note("WARN: could not detect the running init system -- tink-daemon not installed, run `tink daemon install` manually")
		return nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving tink's own binary path: %w", err)
	}
	unitOpts := daemon.UnitOptions{ExecPath: execPath, Args: []string{"daemon", "run"}}

	content, err := daemon.Generate(initSystem, unitOpts)
	if err != nil {
		return err
	}

	switch initSystem {
	case "systemd":
		if err := r.runWithStdin("wrote tink-daemon.service", content, "tee", "/etc/systemd/system/tink-daemon.service"); err != nil {
			return err
		}
		if _, err := r.run("systemd: reloaded unit files", "systemctl", "daemon-reload"); err != nil {
			return err
		}
		if _, err := r.run("systemd: enabled and started tink-daemon", "systemctl", "enable", "--now", "tink-daemon"); err != nil {
			return err
		}
	case "openrc":
		if err := r.runWithStdin("wrote /etc/init.d/tink-daemon", content, "tee", "/etc/init.d/tink-daemon"); err != nil {
			return err
		}
		if _, err := r.run("made /etc/init.d/tink-daemon executable", "chmod", "+x", "/etc/init.d/tink-daemon"); err != nil {
			return err
		}
		if _, err := r.run("openrc: added tink-daemon to the default runlevel", "rc-update", "add", "tink-daemon", "default"); err != nil {
			return err
		}
		if _, err := r.run("openrc: started tink-daemon", "rc-service", "tink-daemon", "start"); err != nil {
			return err
		}
	}

	return nil
}

// removeLegacyReconcilerCronStep wraps removeLegacyReconcilerCron for
// dry-run reporting -- the actual crontab read/rewrite always happens
// (read-only unless a legacy entry is actually found), matching how
// other steps compute an accurate plan against real current state.
func removeLegacyReconcilerCronStep(r *runner) (bool, error) {
	if r.dryRun {
		out, err := exec.Command("crontab", "-l").Output()
		if err != nil {
			return false, nil
		}
		if strings.Contains(string(out), legacyReconcilerCronMarker) {
			r.note("would remove legacy reconciler cron entry")
		}
		return false, nil
	}
	return removeLegacyReconcilerCron()
}
