package helper

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/run"
)

const (
	// DefaultProject and DefaultName are where `helper install` puts the helper: a project of its own, so who can reach it is a
	// project-level question.
	DefaultProject = "tink-helper"
	DefaultName    = "helper"
	// TrustName is the name of the helper's certificate in the host's trust store. There is one helper per server, so one name.
	TrustName = "tink-helper"
	// RemoteName is what the helper calls its own host, in its Incus client configuration.
	RemoteName = "host"

	configVolume = "tink-helper-config"
	dataVolume   = "tink-helper-data"
	configMount  = "/root/.config/incus"
	dataMount    = "/data"
	binaryPath   = "/usr/local/bin/tink"
	stockImage   = "docker-oci:library/alpine:3"
)

// InstallOptions say where and from what to install the helper.
type InstallOptions struct {
	Project string // default DefaultProject
	Name    string // default DefaultName
	// Pool holds the instance's root disk and its two volumes (default "default").
	Pool string
	// Network is the network the helper's NIC joins. Empty means: the default profile already gives instances one, or else
	// "incusbr0" if there is such a network. The helper needs a NIC even when it talks to nothing but its own host, because the
	// loopback proxy that reaches the host's API listens on the container's loopback, which only a container with a network has up.
	Network string
	// Image is the OCI image the instance runs, resolved the way `tink run` resolves one (docker-oci:library/alpine:3).
	Image string
	// Binary is the path of a linux tink binary to put in the instance. With it and no Image the instance runs a stock alpine image.
	// It is how a helper runs where the helper image cannot be pulled, and what a development build is tried with.
	Binary string
	TZ     string
	// Reissue enrols the helper again: it removes the old certificate from the trust store and redeems a fresh token.
	Reissue bool
	// Wait is how long to wait for the helper to report in once it is enrolled (default 90 seconds; negative: do not wait).
	Wait time.Duration
}

func (o *InstallOptions) defaults() {
	if o.Project == "" {
		o.Project = DefaultProject
	}
	if o.Name == "" {
		o.Name = DefaultName
	}
	if o.Pool == "" {
		o.Pool = "default"
	}
	if o.Image == "" && o.Binary != "" {
		o.Image = stockImage
	}
	if o.Wait == 0 {
		o.Wait = 90 * time.Second
	}
}

// Installer installs and removes the helper. Its seams are for the tests; a zero Installer around a real server is the real thing.
type Installer struct {
	Server incus.InstanceServer
	Out    io.Writer
	// Create makes the (stopped) instance from its image. Default: run.Create, which resolves the image like `tink run` does.
	Create func(server incus.InstanceServer, spec *run.Spec, project string) error
	// ReadFile reads the binary to push. Default: os.ReadFile.
	ReadFile func(path string) ([]byte, error)
	Now      func() time.Time
	Sleep    func(time.Duration)
}

func (in *Installer) say(format string, args ...any) {
	if in.Out != nil {
		fmt.Fprintf(in.Out, format+"\n", args...)
	}
}

func (in *Installer) create() func(incus.InstanceServer, *run.Spec, string) error {
	if in.Create != nil {
		return in.Create
	}
	return run.Create
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now()
}

func (in *Installer) sleep(d time.Duration) {
	if in.Sleep != nil {
		in.Sleep(d)
		return
	}
	time.Sleep(d)
}

// Install creates the helper, starts it and enrols it. It is safe to run again: what exists is left alone, and a helper that is
// already enrolled is not enrolled twice (Reissue is how to do it again on purpose).
func (in *Installer) Install(opts InstallOptions) error {
	opts.defaults()
	if opts.Image == "" {
		return fmt.Errorf("no image given: pass --image (an OCI image that has tink at %s), or --binary FILE to run a linux tink binary in a stock alpine image", binaryPath)
	}

	// One helper per server, wherever it is.
	existing, err := Find(in.Server)
	if err != nil {
		return err
	}
	for _, f := range existing {
		if f.Project != opts.Project || f.Name != opts.Name {
			return fmt.Errorf("there is already a helper on this server, %s: only one is supported (remove it first, or install into that project and name)", f.Label())
		}
	}

	apiAddr, err := in.apiAddress()
	if err != nil {
		return err
	}
	_, port, _ := net.SplitHostPort(apiAddr)
	network, err := in.networkFor(opts)
	if err != nil {
		return err
	}

	if err := in.ensureProject(opts.Project); err != nil {
		return err
	}
	scoped := in.Server.UseProject(opts.Project)

	_, _, haveInstance, err := incusapi.LookupInstance(scoped, opts.Name)
	if err != nil {
		return fmt.Errorf("checking whether the helper instance exists: %w", err)
	}
	spec := in.spec(opts, apiAddr, network)
	if !haveInstance {
		var blob []byte
		if opts.Binary != "" {
			read := in.ReadFile
			if read == nil {
				read = os.ReadFile
			}
			if blob, err = read(opts.Binary); err != nil {
				return fmt.Errorf("reading --binary: %w", err)
			}
		}
		in.say("creating the helper instance %s/%s from %s", opts.Project, opts.Name, opts.Image)
		if err := in.create()(in.Server, spec, opts.Project); err != nil {
			return err
		}
		if err := run.ApplyConfig(scoped, spec); err != nil {
			return err
		}
		if blob != nil {
			in.say("putting the tink binary in the instance (%d bytes)", len(blob))
			if err := scoped.CreateInstanceFile(opts.Name, binaryPath, incus.InstanceFileArgs{
				Content: strings.NewReader(string(blob)), Type: "file", WriteMode: "overwrite", Mode: 0o755, UID: 0, GID: 0,
			}); err != nil {
				return fmt.Errorf("putting the tink binary in the helper: %w", err)
			}
		}
	} else {
		in.say("the helper instance %s/%s exists", opts.Project, opts.Name)
	}
	in.say("starting it")
	if err := run.EnsureRunning(scoped, opts.Name); err != nil {
		return err
	}

	enrolling := in.now()
	if err := in.enrol(scoped, opts, port, !haveInstance); err != nil {
		return err
	}
	return in.waitForReport(opts, enrolling)
}

// apiAddress is where the host's API listens, as an address the helper's loopback proxy can connect to on the host.
func (in *Installer) apiAddress() (string, error) {
	srv, _, err := in.Server.GetServer()
	if err != nil {
		return "", fmt.Errorf("reading the server's configuration: %w", err)
	}
	addr := srv.Config["core.https_address"]
	if addr == "" {
		return "", fmt.Errorf("this server has no HTTPS API listener (core.https_address is not set), which the helper's own certificate needs: " +
			"set one (the loopback address is enough: `incus config set core.https_address 127.0.0.1:8443`). A helper that uses the unix socket instead is not built yet")
	}
	return connectAddress(addr)
}

// connectAddress turns a listening address into one a client on the same host can dial: a wildcard listener is reached on loopback.
func connectAddress(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("core.https_address %q is not host:port: %w", listen, err)
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

// networkFor decides which network the helper's NIC joins: the one asked for; or none to add, when the default profile gives
// instances a NIC already; or incusbr0 when there is one.
func (in *Installer) networkFor(opts InstallOptions) (string, error) {
	if opts.Network != "" {
		if _, _, found, err := lookupNetwork(in.Server, opts.Network); err != nil {
			return "", err
		} else if !found {
			return "", fmt.Errorf("--network %s: there is no such network", opts.Network)
		}
		return opts.Network, nil
	}
	if prof, _, found, err := incusapi.LookupProfile(in.Server, "default"); err != nil {
		return "", fmt.Errorf("reading the default profile: %w", err)
	} else if found {
		for _, dev := range prof.Devices {
			if dev["type"] == "nic" {
				return "", nil
			}
		}
	}
	if _, _, found, err := lookupNetwork(in.Server, "incusbr0"); err != nil {
		return "", err
	} else if found {
		return "incusbr0", nil
	}
	return "", fmt.Errorf("the helper needs a network (a NIC): the default profile has none and there is no incusbr0, so say which with --network")
}

func lookupNetwork(s incus.InstanceServer, name string) (*api.Network, string, bool, error) {
	n, e, err := s.GetNetwork(name)
	switch {
	case err == nil:
		return n, e, true, nil
	case incusapi.IsNotFound(err):
		return nil, "", false, nil
	default:
		return nil, "", false, err
	}
}

func (in *Installer) ensureProject(name string) error {
	_, _, found, err := incusapi.LookupProject(in.Server, name)
	if err != nil {
		return fmt.Errorf("checking whether project %s exists: %w", name, err)
	}
	if found {
		return nil
	}
	in.say("creating project %s", name)
	return in.Server.CreateProject(api.ProjectsPost{Name: name, ProjectPut: api.ProjectPut{Config: map[string]string{
		"features.storage.volumes": "true", "features.images": "false", "features.profiles": "false",
	}}})
}

// spec is the helper instance: what it runs, what it mounts, how it reaches the host's API.
func (in *Installer) spec(o InstallOptions, apiAddr, network string) *run.Spec {
	_, port, _ := net.SplitHostPort(apiAddr)
	entry := []string{binaryPath, "daemon", "run", "--remote", RemoteName,
		"--jobs", dataMount + "/jobs", "--no-ingress",
		"--status-instance", o.Name, "--status-project", o.Project}
	if o.TZ != "" {
		entry = append(entry, "--timezone", o.TZ)
	}
	cfg := map[string]string{
		"boot.autostart":      "true",
		"boot.autorestart":    "true",
		"oci.entrypoint":      strings.Join(entry, " "),
		"environment.HOME":    "/root",
		MarkerKey:             fmt.Sprint(jobs.Proto),
		MarkerKey + ".image":  o.Image,
		MarkerKey + ".remote": "https://127.0.0.1:" + port,
		MarkerKey + ".pool":   o.Pool,
	}
	if o.TZ != "" {
		cfg["environment.TZ"] = o.TZ
	}
	dev := map[string]map[string]string{
		"root":   {"type": "disk", "path": "/", "pool": o.Pool},
		"config": {"type": "disk", "pool": o.Pool, "source": configVolume, "path": configMount},
		"data":   {"type": "disk", "pool": o.Pool, "source": dataVolume, "path": dataMount},
		// the host's API, on the container's own loopback: the helper reaches it without it being exposed to anything new
		"api": {"type": "proxy", "bind": "container", "listen": "tcp:127.0.0.1:" + port, "connect": "tcp:" + apiAddr},
	}
	if network != "" {
		dev["eth0"] = map[string]string{"type": "nic", "network": network, "name": "eth0"}
	}
	return &run.Spec{Name: o.Name, Image: o.Image, Config: cfg, Devices: dev}
}

// enrol makes the host trust the helper's own certificate. The key pair is generated inside the instance, so the private key never
// leaves it; what passes is a single-use token, handed over on standard input, never on a command line or to disk.
func (in *Installer) enrol(scoped incus.InstanceServer, opts InstallOptions, port string, fresh bool) error {
	entry, err := in.trustEntry()
	if err != nil {
		return err
	}
	if entry != nil && !opts.Reissue {
		in.say("the helper is already enrolled (%s in the trust store); --reissue enrols it again", TrustName)
		return nil
	}
	if entry != nil {
		in.say("removing the old certificate %s from the trust store", entry.Fingerprint[:12])
		if err := in.Server.DeleteCertificate(entry.Fingerprint); err != nil {
			return fmt.Errorf("removing the old certificate: %w", err)
		}
	}
	if !fresh {
		// An instance that was enrolled before still has its `host` remote, and `remote add` will not replace one. Forget it. On
		// --reissue the key pair goes too, so the helper makes a new one; otherwise it keeps its key, which the host is told to
		// trust again.
		in.say("forgetting the helper's earlier enrolment")
		_, _, _ = incusapi.ExecWithStdin(scoped, opts.Name, []string{binaryPath, "remote", "remove", RemoteName}, strings.NewReader(""), map[string]string{"HOME": "/root"})
		if opts.Reissue {
			for _, f := range []string{configMount + "/client.key", configMount + "/client.crt"} {
				if err := scoped.DeleteInstanceFile(opts.Name, f); err != nil && !incusapi.IsNotFound(err) {
					return fmt.Errorf("removing the helper's old key (%s): %w", f, err)
				}
			}
		}
	}
	token, cancel, err := in.mintToken()
	if err != nil {
		return err
	}
	// the token is single-use and lives for hours: whatever happens next, it must not be left lying around, and a token that was
	// redeemed is already gone (cancelling it then is harmless)
	defer cancel()
	in.say("enrolling the helper: its key is made inside the instance, and the host is told to trust it")
	addr := "https://127.0.0.1:" + port
	code, out, err := incusapi.ExecWithStdin(scoped, opts.Name,
		[]string{binaryPath, "remote", "add", RemoteName, addr, "--token-file", "-"},
		strings.NewReader(token), map[string]string{"HOME": "/root"})
	if err != nil {
		return fmt.Errorf("enrolling the helper: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("enrolling the helper failed (exit %d): %s", code, strings.TrimSpace(out))
	}
	return nil
}

func (in *Installer) trustEntry() (*api.Certificate, error) {
	certs, err := in.Server.GetCertificates()
	if err != nil {
		return nil, fmt.Errorf("reading the trust store: %w", err)
	}
	for i := range certs {
		if certs[i].Name == TrustName {
			return &certs[i], nil
		}
	}
	return nil, nil
}

// mintToken makes a single-use trust token and returns it with a function that cancels it. It must NOT wait on the operation Incus
// returns: that is a token operation, which does not end until the token is redeemed or expires, hours later (`incus config trust add`
// reads the token from the operation straight away, and so does this).
func (in *Installer) mintToken() (token string, cancel func(), err error) {
	op, err := in.Server.CreateCertificateToken(api.CertificatesPost{CertificatePut: api.CertificatePut{Name: TrustName, Type: "client"}, Token: true})
	if err != nil {
		return "", nil, fmt.Errorf("minting a trust token: %w", err)
	}
	cancel = func() { _ = op.Cancel() }
	opAPI := op.Get()
	tok, err := opAPI.ToCertificateAddToken()
	if err != nil {
		cancel()
		return "", nil, fmt.Errorf("reading the trust token: %w", err)
	}
	return tok.String(), cancel, nil
}

// waitForReport waits for a status document the helper published AFTER enrolment began, and says what it found: an older one proves
// nothing about the enrolment (a helper that was revoked and enrolled again still has the document from before). A helper that is
// enrolled but has not reported yet is not a failure of the install: it says so, and says what to run.
func (in *Installer) waitForReport(opts InstallOptions, since time.Time) error {
	if opts.Wait < 0 {
		return nil
	}
	deadline := in.now().Add(opts.Wait)
	for {
		found, err := Find(in.Server)
		if err == nil {
			for _, f := range found {
				if f.Project == opts.Project && f.Name == opts.Name {
					if r := Evaluate(f, in.now()); r.Status != nil && !r.Status.Tick.Before(since) {
						in.say("%s", r.Summary())
						return nil
					}
				}
			}
		}
		if !in.now().Before(deadline) {
			in.say("the helper is enrolled but has not published a status document since, within %s. It publishes when something changes and at least every %s, so it may simply not have had a reason yet: look with `tink helper status`, and at its log with `incus console %s --project %s --show-log`", opts.Wait, DefaultHeartbeat, opts.Name, opts.Project)
			return nil
		}
		in.sleep(2 * time.Second)
	}
}

// RemoveOptions say which helper to remove and how much.
type RemoveOptions struct {
	Project string // default: where the helper is found
	Name    string
	// Purge also deletes the helper's two volumes (its job history, and its client configuration with any keys for remote backup targets)
	// and the project if that leaves it empty.
	Purge bool
}

// Remove revokes the helper's certificate, then stops and deletes the instance. Its volumes stay unless Purge: the job history and the
// client configuration are the operator's to keep or to drop.
func (in *Installer) Remove(opts RemoveOptions) error {
	found, err := Find(in.Server)
	if err != nil {
		return err
	}
	var target *Found
	for i := range found {
		if (opts.Name == "" || found[i].Name == opts.Name) && (opts.Project == "" || found[i].Project == opts.Project) {
			target = &found[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("no helper found to remove")
	}

	entry, err := in.trustEntry()
	if err != nil {
		return err
	}
	if entry != nil {
		in.say("revoking the helper's certificate (%s)", entry.Fingerprint[:12])
		if err := in.Server.DeleteCertificate(entry.Fingerprint); err != nil {
			return fmt.Errorf("revoking the helper's certificate: %w", err)
		}
	}
	scoped := in.Server.UseProject(target.Project)
	if target.State == "Running" {
		in.say("stopping %s", target.Label())
		op, err := scoped.UpdateInstanceState(target.Name, api.InstanceStatePut{Action: "stop", Force: true, Timeout: 30}, "")
		if err != nil {
			return fmt.Errorf("stopping the helper: %w", err)
		}
		if err := op.Wait(); err != nil {
			return fmt.Errorf("stopping the helper: %w", err)
		}
	}
	in.say("deleting the instance %s", target.Label())
	op, err := scoped.DeleteInstance(target.Name)
	if err != nil {
		return fmt.Errorf("deleting the helper: %w", err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("deleting the helper: %w", err)
	}

	if !opts.Purge {
		in.say("kept the volumes %s and %s in project %s (--purge removes them)", configVolume, dataVolume, target.Project)
		return nil
	}
	for _, v := range []string{configVolume, dataVolume} {
		pool := volumePool(target)
		if err := scoped.DeleteStoragePoolVolume(pool, "custom", v); err != nil && !incusapi.IsNotFound(err) {
			return fmt.Errorf("deleting volume %s: %w", v, err)
		}
		in.say("deleted volume %s", v)
	}
	// the project goes too when nothing else is in it; if something is, it is the operator's, and stays
	if err := in.Server.DeleteProject(target.Project); err != nil {
		in.say("kept project %s (%v)", target.Project, err)
	} else {
		in.say("deleted project %s", target.Project)
	}
	return nil
}

// volumePool is the pool the helper's volumes were made in, which install recorded on the instance; "default" for one that did not.
func volumePool(f *Found) string {
	if p := f.Config["user.tink.helper.pool"]; p != "" {
		return p
	}
	return "default"
}
