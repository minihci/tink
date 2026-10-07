package helper

import (
	"fmt"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/run"
)

// drainPath is the file that tells the helper to drain, on its data volume.
const drainPath = dataMount + "/jobs/" + jobs.DrainFile

// UpgradeOptions say which helper to upgrade, and to what.
type UpgradeOptions struct {
	Project, Name string // default: the helper that is found
	// Image replaces the instance with one made from this image, keeping its volumes. Binary replaces only the tink binary in it.
	// Exactly one is given.
	Image  string
	Binary string
	// DrainTimeout is how long to wait for the running work to finish (default 15 minutes).
	DrainTimeout time.Duration
	// Force goes on after the drain timeout, interrupting what is running: the job is marked failed (interrupted) and the schedule
	// retries the work.
	Force bool
	// Wait is how long to wait for the upgraded helper to report in (default 90 seconds; negative: do not wait).
	Wait time.Duration
}

// reportWait is how long a drain waits for the helper to say anything at all before deciding it is not reporting.
const reportWait = 30 * time.Second

// Upgrade replaces the helper's tink: it drains the helper (the scheduler queues nothing, the executor starts nothing, a running job
// finishes), replaces it, lifts the drain and waits for it to report. The certificate and key are on the config volume, which an
// upgrade never touches, so the helper stays enrolled: there is no token and no trust-store change.
func (in *Installer) Upgrade(opts UpgradeOptions) error {
	if (opts.Image == "") == (opts.Binary == "") {
		return fmt.Errorf("give exactly one of --image (replace the instance with one made from that image, keeping its volumes) and --binary (replace only the tink binary in it)")
	}
	if opts.DrainTimeout <= 0 {
		opts.DrainTimeout = 15 * time.Minute
	}
	if opts.Wait == 0 {
		opts.Wait = 90 * time.Second
	}

	target, err := in.findHelper(opts.Project, opts.Name)
	if err != nil {
		return err
	}
	scoped := in.Server.UseProject(target.Project)
	inst, _, found, err := incusapi.LookupInstance(scoped, target.Name)
	if err != nil {
		return fmt.Errorf("reading the helper: %w", err)
	}
	if !found {
		return fmt.Errorf("the helper %s disappeared", target.Label())
	}
	previous := inst.Config[MarkerKey+".image"]

	var blob []byte
	if opts.Binary != "" {
		read := in.ReadFile
		if read == nil {
			read = osReadFile
		}
		if blob, err = read(opts.Binary); err != nil {
			return fmt.Errorf("reading --binary: %w", err)
		}
	}

	// 1. drain, if there is anything to drain
	drained := false
	if inst.Status == "Running" {
		if err := in.drain(scoped, target, opts); err != nil {
			return err
		}
		drained = true
	} else {
		in.say("the helper is %s: nothing to drain", strings.ToLower(inst.Status))
	}

	// 2. replace
	if inst.Status == "Running" {
		in.say("stopping %s", target.Label())
		if err := in.stop(scoped, target.Name); err != nil {
			return err
		}
	}
	replaced := time.Time{}
	if opts.Binary != "" {
		in.say("putting the new tink binary in the instance (%d bytes)", len(blob))
		if err := scoped.CreateInstanceFile(target.Name, binaryPath, incus.InstanceFileArgs{
			Content: strings.NewReader(string(blob)), Type: "file", WriteMode: "overwrite", Mode: 0o755,
		}); err != nil {
			return fmt.Errorf("putting the new binary in the helper (it is stopped; start it with incus start %s --project %s): %w", target.Name, target.Project, err)
		}
	} else {
		in.say("replacing the instance with one made from %s (its volumes stay)", opts.Image)
		if err := in.recreate(scoped, inst, target, opts.Image); err != nil {
			return fmt.Errorf("%w (the previous image was %q: `tink helper upgrade --image %s` goes back to it)", err, previous, previous)
		}
	}
	in.say("starting it")
	replaced = in.now()
	if err := run.EnsureRunning(scoped, target.Name); err != nil {
		return err
	}

	// 3. lift the drain: the file is on the volume, so it survives the replacement
	if drained {
		if err := scoped.DeleteInstanceFile(target.Name, drainPath); err != nil && !incusapi.IsNotFound(err) {
			return fmt.Errorf("the helper is upgraded but still draining, because its drain file could not be removed (remove %s in it): %w", drainPath, err)
		}
	}
	return in.waitForReport(InstallOptions{Project: target.Project, Name: target.Name, Wait: opts.Wait}, replaced)
}

// drain asks the helper to stop starting work and waits until nothing is running. It leaves the drain in place when it succeeds (the
// caller lifts it after the replacement) and removes it when it gives up.
func (in *Installer) drain(scoped incus.InstanceServer, target Found, opts UpgradeOptions) error {
	in.say("draining %s: it queues and starts nothing new while a running job finishes", target.Label())
	begun := in.now()
	if err := scoped.CreateInstanceFile(target.Name, drainPath, incus.InstanceFileArgs{
		Content: strings.NewReader(""), Type: "file", WriteMode: "overwrite", Mode: 0o600,
	}); err != nil {
		return fmt.Errorf("asking the helper to drain: %w", err)
	}
	giveUp := func(format string, args ...any) error {
		_ = scoped.DeleteInstanceFile(target.Name, drainPath)
		return fmt.Errorf(format, args...)
	}

	reportDeadline := begun.Add(reportWait)
	deadline := begun.Add(opts.DrainTimeout)
	announced := false
	for {
		st, err := in.statusOf(target)
		switch {
		case err == nil && st != nil && st.Draining && !st.Tick.Before(begun):
			if st.Running == 0 {
				in.say("drained: nothing is running (%d queued job(s) wait for after the upgrade)", st.Queued)
				return nil
			}
			if !announced {
				in.say("waiting for %d running job(s) to finish (up to %s)", st.Running, opts.DrainTimeout)
				announced = true
			}
		case !in.now().Before(reportDeadline) && !announced:
			// it has said nothing since: it is not reporting, so there is nothing we can see to wait on
			in.say("the helper has not reported since the drain began, so it cannot be seen to be idle: going on")
			return nil
		}
		if !in.now().Before(deadline) {
			if opts.Force {
				in.say("the drain timed out after %s: going on anyway (--force), which interrupts the running job; its schedule retries the work", opts.DrainTimeout)
				return nil
			}
			return giveUp("the helper is still running a job after %s: wait and try again, or --force to interrupt it (the job is marked failed and its schedule retries the work). The drain has been lifted", opts.DrainTimeout)
		}
		in.sleep(2 * time.Second)
	}
}

// statusOf reads the helper's current status document, or nil if it has none.
func (in *Installer) statusOf(target Found) (*Status, error) {
	all, err := Find(in.Server)
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		if f.Project == target.Project && f.Name == target.Name {
			r := Evaluate(f, in.now())
			return r.Status, nil
		}
	}
	return nil, nil
}

func (in *Installer) findHelper(project, name string) (Found, error) {
	all, err := Find(in.Server)
	if err != nil {
		return Found{}, err
	}
	var match []Found
	for _, f := range all {
		if (name == "" || f.Name == name) && (project == "" || f.Project == project) {
			match = append(match, f)
		}
	}
	switch len(match) {
	case 0:
		return Found{}, fmt.Errorf("no helper found")
	case 1:
		return match[0], nil
	}
	return Found{}, fmt.Errorf("more than one helper found; say which with --project and --name")
}

func (in *Installer) stop(scoped incus.InstanceServer, name string) error {
	op, err := scoped.UpdateInstanceState(name, api.InstanceStatePut{Action: "stop", Force: true, Timeout: 30}, "")
	if err != nil {
		return fmt.Errorf("stopping the helper: %w", err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("stopping the helper: %w", err)
	}
	return nil
}

// recreate deletes the (stopped) instance and makes it again from image with the same configuration and devices, which is what keeps
// its volumes, its certificate and its entrypoint. Only the image, and what Incus itself records about the old one (volatile.*), differ.
func (in *Installer) recreate(scoped incus.InstanceServer, old *api.Instance, target Found, image string) error {
	cfg := map[string]string{}
	for k, v := range old.Config {
		if !strings.HasPrefix(k, "volatile.") && k != MarkerKey+".image-fingerprint" {
			cfg[k] = v
		}
	}
	cfg[MarkerKey+".image"] = image
	dev := map[string]map[string]string{}
	for k, v := range old.Devices {
		dev[k] = v
	}
	spec := &run.Spec{Name: target.Name, Image: image, Config: cfg, Devices: dev}

	op, err := scoped.DeleteInstance(target.Name)
	if err != nil {
		return fmt.Errorf("deleting the old helper instance: %w", err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("deleting the old helper instance: %w", err)
	}
	if err := in.create()(in.Server, spec, target.Project); err != nil {
		return fmt.Errorf("creating the new helper instance: %w", err)
	}
	if inst, _, ok, err := incusapi.LookupInstance(scoped, target.Name); err == nil && ok {
		if fp := inst.Config["volatile.base_image"]; fp != "" {
			spec.Config[MarkerKey+".image-fingerprint"] = fp
		}
	}
	if err := run.ApplyConfig(scoped, spec); err != nil {
		return fmt.Errorf("configuring the new helper instance: %w", err)
	}
	return nil
}
