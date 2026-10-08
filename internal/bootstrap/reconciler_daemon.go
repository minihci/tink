package bootstrap

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
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

// Where an earlier deploy put the host's tink-daemon unit. Variables so a test can point them at a temporary directory.
var (
	systemdUnitPath = "/etc/systemd/system/tink-daemon.service"
	openrcUnitPath  = "/etc/init.d/tink-daemon"
)

// Seams for the tests: how the server is reached, how the helper is installed, and how long to wait for it to reconcile ingress.
var (
	connectIncus  = incusapi.Connect
	installHelper = func(server incus.InstanceServer, out io.Writer, o helper.InstallOptions) error {
		return (&helper.Installer{Server: server, Out: out}).Install(o)
	}
	ingressWait  = 90 * time.Second
	ingressSleep = time.Sleep
)

// applyHelper makes the helper the host's reconciler. It removes any legacy cron entry, installs the helper with the ingress reconcile
// (a long-running instance next to the data, supervised by Incus, replacing the "tink daemon run" unit that deploy used to write under
// systemd or OpenRC), and then retires that unit if an earlier deploy left one, because two reconcilers would render the same routes and
// reload the same Caddy from places that know nothing of each other.
//
// The old unit is retired only once the helper has reported a successful ingress reconcile: a host is never left without one.
func applyHelper(r *runner, opts Options) error {
	if removed, err := removeLegacyReconcilerCronStep(r); err != nil {
		return err
	} else if removed {
		r.note("removed legacy reconciler cron entry")
	}

	if r.dryRun {
		r.note("would install the helper with the ingress reconcile (tink helper install --ingress) if the host has none, " +
			"and retire the host's tink-daemon unit once the helper has reconciled ingress")
		return nil
	}

	server, err := connectIncus(opts.socket())
	if err != nil {
		return fmt.Errorf("connecting to incus to install the helper: %w", err)
	}
	found, err := helper.Find(server)
	if err != nil {
		return err
	}
	label, runs := helperRunsIngress(server)
	switch {
	case runs:
		r.note("the helper %s runs the ingress reconcile", label)
	case len(found) > 0:
		r.note("WARN: the helper %s does not run the ingress reconcile, and deploy no longer installs a host daemon to do it. "+
			"`tink helper remove` (its volumes and history stay), then deploy again to install one that does. "+
			"The host's tink-daemon unit, if there is one, is left running.", found[0].Label())
		return nil
	default:
		w := &noteWriter{r: r}
		err := installHelper(server, w, helper.InstallOptions{
			Ingress: true, Image: opts.Helper.Image, Binary: opts.Helper.Binary, TZ: opts.Helper.TZ,
		})
		w.flush()
		if err != nil {
			return fmt.Errorf("installing the helper: %w", err)
		}
		if found, err = helper.Find(server); err != nil || len(found) == 0 {
			return fmt.Errorf("the helper was installed but cannot be found again: %v", err)
		}
		label = found[0].Label()
	}

	if !hostUnitInstalled() {
		return nil
	}
	if !waitForIngress(server, label) {
		r.note("WARN: the host's tink-daemon unit is still installed, and is left running: the helper %s has not reported a successful ingress reconcile "+
			"within %s (see `tink helper status`). Once it does, disable the unit: systemctl disable --now tink-daemon", label, ingressWait)
		return nil
	}
	return retireHostDaemon(r)
}

// waitForIngress is whether the helper has reported a successful ingress reconcile, within ingressWait.
func waitForIngress(server incus.InstanceServer, label string) bool {
	deadline := time.Now().Add(ingressWait)
	for {
		if found, err := helper.Find(server); err == nil {
			for _, f := range found {
				if f.Label() != label {
					continue
				}
				if rep := helper.Evaluate(f, time.Now()); rep.Status != nil && rep.Status.Ingress != nil && rep.Status.Ingress.OK {
					return true
				}
			}
		}
		if !time.Now().Before(deadline) {
			return false
		}
		ingressSleep(2 * time.Second)
	}
}

func hostUnitInstalled() bool {
	for _, p := range []string{systemdUnitPath, openrcUnitPath} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// retireHostDaemon stops, disables and removes the unit an earlier deploy wrote for "tink daemon run".
func retireHostDaemon(r *runner) error {
	if _, err := os.Stat(systemdUnitPath); err == nil {
		if _, err := r.run("systemd: stopped and disabled tink-daemon (the helper reconciles ingress now)", "systemctl", "disable", "--now", "tink-daemon"); err != nil {
			return err
		}
		if err := os.Remove(systemdUnitPath); err != nil {
			return fmt.Errorf("removing %s: %w", systemdUnitPath, err)
		}
		r.note("removed %s", systemdUnitPath)
		if _, err := r.run("systemd: reloaded unit files", "systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	if _, err := os.Stat(openrcUnitPath); err == nil {
		if _, err := r.run("openrc: stopped tink-daemon (the helper reconciles ingress now)", "rc-service", "tink-daemon", "stop"); err != nil {
			return err
		}
		if _, err := r.run("openrc: removed tink-daemon from the default runlevel", "rc-update", "del", "tink-daemon", "default"); err != nil {
			return err
		}
		if err := os.Remove(openrcUnitPath); err != nil {
			return fmt.Errorf("removing %s: %w", openrcUnitPath, err)
		}
		r.note("removed %s", openrcUnitPath)
	}
	return nil
}

// helperRunsIngress says whether a helper on this server runs the ingress reconcile, and which. A server that cannot be asked, or has no
// helper, is a server where none does.
func helperRunsIngress(server incus.InstanceServer) (label string, runs bool) {
	found, err := helper.Find(server)
	if err != nil {
		return "", false
	}
	for _, f := range found {
		if f.Config[helper.MarkerKey+".ingress"] != "" {
			return f.Label(), true
		}
	}
	return "", false
}

// noteWriter turns what the helper installer says, a line at a time, into the deploy log.
type noteWriter struct {
	r   *runner
	buf []byte
}

func (w *noteWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			return len(p), nil
		}
		w.r.note("helper: %s", string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
}

func (w *noteWriter) flush() {
	if len(w.buf) > 0 {
		w.r.note("helper: %s", string(w.buf))
		w.buf = nil
	}
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
