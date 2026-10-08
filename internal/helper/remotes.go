package helper

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/incusconf"
	"github.com/minihci/tink/internal/remote"
)

// ConfiguredRemotes is the Incus servers this process can reach by name: the entries of its Incus client configuration that
// incusapi.ConnectRemote would open over the network (an Incus server, not an image registry, and not the `local` unix socket, which the
// helper does not have). It is what the helper publishes as Status.Remotes, and the list is never nil on success.
func ConfiguredRemotes() ([]Remote, error) {
	conf, err := incusconf.Load()
	if err != nil {
		return nil, err
	}
	out := []Remote{}
	for name, r := range conf.Remotes {
		if r.Public || r.Protocol != "incus" {
			continue
		}
		addr := ""
		if len(r.Addrs) > 0 {
			addr = r.Addrs[0]
		}
		if strings.HasPrefix(addr, "unix:") {
			continue
		}
		out = append(out, Remote{Name: name, Addr: addr, Project: r.Project})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// RemoteAddOptions say which remote to give the helper, and how to verify and trust it. They are what `tink remote add` takes, since that
// is what runs, inside the helper.
type RemoteAddOptions struct {
	// Project and Name pick the helper; by default the one that is found.
	Project, Name string
	// Remote is the name the helper will know the server by. It is the name a backup target's `remote:` uses, so it must be the same
	// one the stack uses.
	Remote string
	// Addr may be empty when Token carries the server's addresses.
	Addr string
	// Token is a single-use trust token made on the server being added (`incus config trust add`). It goes to the helper on standard input,
	// never on a command line. Empty when the server already trusts the helper's certificate.
	Token             string
	Fingerprint       string
	AcceptCertificate bool
	// RemoteProject is the project the remote defaults to.
	RemoteProject string
}

// reservedRemote says why a remote name cannot be used by hand, or "".
func reservedRemote(name string) string {
	if name == RemoteName {
		return fmt.Sprintf("%q is the helper's own host, which `tink helper install` set up; it cannot be added or removed by hand (`tink helper remove` and `install --reissue` are how it is changed)", name)
	}
	return ""
}

// AddRemote gives the helper another Incus server to reach by name, so a backup target that names it (`remote: NAME`) can be copied to by the
// helper's schedule. It runs `tink remote add` inside the helper, so the helper's client certificate (the one the host already trusts, made
// inside the instance) is what the other server is told to trust: the key never leaves the helper, and the server can revoke it on its own.
func (in *Installer) AddRemote(opts RemoteAddOptions) error {
	if err := remote.ValidName(opts.Remote); err != nil {
		return err
	}
	if why := reservedRemote(opts.Remote); why != "" {
		return fmt.Errorf("%s", why)
	}
	if opts.Addr == "" && opts.Token == "" {
		return fmt.Errorf("give the server's address, or a trust token (which carries it)")
	}
	target, err := in.runningHelper(opts.Project, opts.Name)
	if err != nil {
		return err
	}
	command := []string{binaryPath, "remote", "add", opts.Remote}
	if opts.Addr != "" {
		command = append(command, opts.Addr)
	}
	var stdin string
	if opts.Token != "" {
		command, stdin = append(command, "--token-file", "-"), opts.Token
	}
	if opts.Fingerprint != "" {
		command = append(command, "--fingerprint", opts.Fingerprint)
	}
	if opts.AcceptCertificate {
		command = append(command, "--accept-certificate")
	}
	if opts.RemoteProject != "" {
		command = append(command, "--project", opts.RemoteProject)
	}
	in.say("adding the remote %s to %s: the helper connects to it itself, so it must be reachable from the helper", opts.Remote, target.Label())
	code, out, err := incusapi.ExecWithStdin(in.Server.UseProject(target.Project), target.Name, command, strings.NewReader(stdin), map[string]string{"HOME": "/root"})
	if err != nil {
		return fmt.Errorf("adding the remote in the helper: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("adding the remote in the helper failed (exit %d): %s", code, strings.TrimSpace(out))
	}
	// "use it with: tink --remote NAME plan FILE" is advice for the machine `tink remote add` ran on, which here is the helper
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(line, "use it with:") {
			kept = append(kept, line)
		}
	}
	if out = strings.TrimSpace(strings.Join(kept, "\n")); out != "" {
		in.say("%s", out)
	}
	in.say("the helper reports its remotes within a few seconds: see `tink helper remote list`")
	return nil
}

// RemoveRemote makes the helper forget a remote. The other server still trusts the helper's certificate; that is the server's to remove.
func (in *Installer) RemoveRemote(project, name, remoteName string) error {
	if why := reservedRemote(remoteName); why != "" {
		return fmt.Errorf("%s", why)
	}
	target, err := in.runningHelper(project, name)
	if err != nil {
		return err
	}
	code, out, err := incusapi.ExecWithStdin(in.Server.UseProject(target.Project), target.Name, []string{binaryPath, "remote", "remove", remoteName}, strings.NewReader(""), map[string]string{"HOME": "/root"})
	if err != nil {
		return fmt.Errorf("removing the remote in the helper: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("removing the remote in the helper failed (exit %d): %s", code, strings.TrimSpace(out))
	}
	if out = strings.TrimSpace(out); out != "" {
		in.say("%s", out)
	}
	return nil
}

// Remotes are the remotes a helper says it has, and how long ago it said so. It reads the status document, which costs no event and works
// when the helper is stopped; it is what the helper reported last, not a look at its configuration.
func (in *Installer) Remotes(project, name string, now time.Time) (list []Remote, age time.Duration, helperLabel string, err error) {
	target, err := in.findHelper(project, name)
	if err != nil {
		return nil, 0, "", err
	}
	text := target.Config[StatusKey]
	if text == "" {
		return nil, 0, target.Label(), fmt.Errorf("the helper %s has not reported yet, so its remotes are not known (see `tink helper status`)", target.Label())
	}
	st, err := Parse(text)
	if err != nil {
		return nil, 0, target.Label(), fmt.Errorf("the helper %s: %w", target.Label(), err)
	}
	if st.Remotes == nil {
		return nil, 0, target.Label(), fmt.Errorf("the helper %s does not report its remotes (it is older than this tink: `tink helper upgrade`)", target.Label())
	}
	return st.Remotes, now.Sub(st.Tick), target.Label(), nil
}

// runningHelper finds the helper, and refuses one that is not running: the remote is added by running tink inside it.
func (in *Installer) runningHelper(project, name string) (Found, error) {
	target, err := in.findHelper(project, name)
	if err != nil {
		return Found{}, err
	}
	if target.State != "Running" {
		return Found{}, fmt.Errorf("the helper %s is %s: its remotes are changed by running tink inside it, so start it first", target.Label(), strings.ToLower(target.State))
	}
	return target, nil
}
