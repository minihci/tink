// Package remote manages the entries of the Incus client configuration that tink's --remote uses: it adds a
// TLS remote (the job `incus remote add` does), lists them and removes them, so a machine that has never had
// the Incus client installed can still be set up. It writes the same files the `incus` CLI does, in the same
// place, so the two are interchangeable.
//
// TLS only. A server that offers OIDC is still added with TLS: `incus remote add` prefers OIDC when the server
// advertises it (an interactive browser login) unless told otherwise, which is hostile to a tool run from a
// script, a helper container or a laptop without a browser flow, and was the likely cause of an unexplained
// "400 Bad Request" against a server that advertises both.
package remote

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
	localtls "github.com/lxc/incus/v7/shared/tls"
)

// DefaultPort is the port Incus serves its API on unless configured otherwise.
const DefaultPort = "8443"

// AddOptions say how to add a remote.
type AddOptions struct {
	Name string
	// Addr is the server's address ("host", "host:port" or "https://host:port"). It may be empty when Token
	// carries the server's addresses.
	Addr string
	// Token is a trust token from `incus config trust add` on the server. It carries the server's certificate
	// fingerprint (which pins the server, so no prompt is needed), its addresses, and the secret that makes the
	// server trust this machine's client certificate. Empty if the server already trusts this machine.
	Token string
	// Fingerprint pins the server's certificate (SHA-256, hex). With a token the token's own fingerprint is used.
	Fingerprint string
	// AcceptCertificate trusts whatever certificate the server presents on first contact (trust on first use).
	// Without it, one of the three ways to verify the server is required.
	AcceptCertificate bool
	// Project is the project the remote defaults to; empty chooses like `incus remote add` does.
	Project string
	// Confirm is asked, with the certificate's fingerprint, when nothing else verifies the server. Nil means
	// there is no one to ask, and an unverified certificate is refused.
	Confirm func(fingerprint string) (bool, error)
	Out     io.Writer

	// fetchCert and connect are test seams; nil uses the network.
	fetchCert func(addr string) (*x509.Certificate, error)
}

// Result says how the remote was set up.
type Result struct {
	Addr        string
	Fingerprint string
	// Verified is how the server's certificate was verified: "token", "fingerprint", "accepted" or "confirmed".
	Verified string
	// Trusted reports the server trusts this machine's client certificate now.
	Trusted bool
	Project string
}

// ValidName reports whether name can be a remote's name.
func ValidName(name string) error {
	switch {
	case name == "":
		return errors.New("a remote needs a name")
	case strings.ContainsAny(name, ":/ \t"):
		return fmt.Errorf("remote name %q must not contain a colon, a slash or whitespace", name)
	case name == "local":
		return errors.New(`"local" is the built-in name of the local daemon`)
	}
	return nil
}

// NormalizeAddr turns "host", "host:port" or "https://host[:port]" into "https://host:port".
func NormalizeAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("an address cannot be empty")
	}
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}
	u, err := url.Parse(addr)
	if err != nil {
		return "", fmt.Errorf("address %q: %w", addr, err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("address %q: only https is supported (the Incus API is TLS)", addr)
	}
	host, port := u.Hostname(), u.Port()
	if host == "" {
		return "", fmt.Errorf("address %q has no host", addr)
	}
	if port == "" {
		port = DefaultPort
	}
	return "https://" + net.JoinHostPort(host, port), nil
}

// canonicalFingerprint is how a certificate fingerprint is compared: lower case, with the colons some tools print between bytes left out.
func canonicalFingerprint(fp string) string {
	return strings.ToLower(strings.ReplaceAll(fp, ":", ""))
}

// fingerprintForm is what a SHA-256 certificate fingerprint looks like once canonical: 64 hex digits.
var fingerprintForm = regexp.MustCompile(`^[0-9a-f]{64}$`)

// NormalizeFingerprint returns fp in the form Add compares (lower case, no colons) and an error unless it is the 64 hex digits of a SHA-256
// certificate fingerprint. It is for a fingerprint written down ahead of time (a stack declaring a server), where a typo should be refused
// when the file is read, not when a connection is first refused.
func NormalizeFingerprint(fp string) (string, error) {
	c := canonicalFingerprint(strings.TrimSpace(fp))
	if !fingerprintForm.MatchString(c) {
		return "", fmt.Errorf("fingerprint %q is not a SHA-256 certificate fingerprint (64 hex digits, colons allowed)", fp)
	}
	return c, nil
}

// Add adds a TLS remote to conf and saves it. Nothing is saved, and no server certificate file is left behind,
// unless the whole thing succeeds.
//
// The order is the point: the server's certificate is fetched and verified BEFORE anything is sent to it, it is stored so that every
// later connection is pinned to exactly that certificate, and only then, if the server does not trust this machine yet, is the trust
// token's secret presented.
func Add(conf *cliconfig.Config, opts AddOptions) (res Result, err error) {
	say := func(format string, a ...any) {
		if opts.Out != nil {
			fmt.Fprintf(opts.Out, format+"\n", a...)
		}
	}
	if err := ValidName(opts.Name); err != nil {
		return res, err
	}
	if _, exists := conf.Remotes[opts.Name]; exists {
		return res, fmt.Errorf("remote %q already exists (remove it first with `tink remote remove %s`)", opts.Name, opts.Name)
	}

	target, err := resolveTarget(opts)
	if err != nil {
		return res, err
	}
	if err := ensureClientCertificate(conf, say); err != nil {
		return res, err
	}

	cert, used, err := fetchServerCertificate(conf, opts, target.addrs)
	if err != nil {
		return res, err
	}
	got, how, err := verifyServerCertificate(cert, target, opts)
	if err != nil {
		return res, err
	}

	certPath, err := storeServerCertificate(conf, opts.Name, cert)
	if err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(certPath)
			delete(conf.Remotes, opts.Name)
		}
	}()

	conf.Remotes[opts.Name] = cliconfig.Remote{Addrs: []string{used}, Protocol: "incus", AuthType: api.AuthenticationMethodTLS}
	d, err := conf.GetInstanceServer(opts.Name)
	if err != nil {
		return res, fmt.Errorf("connecting to %s: %w", used, err)
	}
	srv, err := checkManageable(d)
	if err != nil {
		return res, err
	}
	if err := establishTrust(conf, d, srv, opts.Token); err != nil {
		return res, err
	}

	project, err := chooseProject(d, opts.Project)
	if err != nil {
		return res, err
	}
	r := conf.Remotes[opts.Name]
	r.Project = project
	conf.Remotes[opts.Name] = r
	if err := conf.SaveConfig(conf.ConfigPath("config.yml")); err != nil {
		return res, fmt.Errorf("saving the Incus client configuration: %w", err)
	}
	return Result{Addr: used, Fingerprint: got, Verified: how, Trusted: true, Project: project}, nil
}

// addTarget is what Add was told about the server before it has contacted it: where it may be, and what pins its certificate.
type addTarget struct {
	addrs []string // normalised, in the order they are tried
	// fingerprint is the certificate fingerprint that pins the server (canonical form), or "" when nothing does yet.
	fingerprint string
	// how is what supplied fingerprint: "token" or "fingerprint"; "" when nothing did.
	how string
}

// resolveTarget works out from the options where the server is and what pins its certificate. A token that is not the encoded kind is
// an older server's bare secret: it carries neither an address nor a fingerprint, so it pins nothing (it is still presented later).
func resolveTarget(opts AddOptions) (addTarget, error) {
	var tok *api.CertificateAddToken
	if opts.Token != "" {
		var err error
		if tok, err = localtls.CertificateTokenDecode(opts.Token); err != nil {
			tok = nil
		}
	}
	var addrs []string
	if opts.Addr != "" {
		a, err := NormalizeAddr(opts.Addr)
		if err != nil {
			return addTarget{}, err
		}
		addrs = []string{a}
	} else if tok != nil {
		for _, a := range tok.Addresses {
			n, err := NormalizeAddr(a)
			if err != nil {
				return addTarget{}, err
			}
			addrs = append(addrs, n)
		}
	}
	if len(addrs) == 0 {
		return addTarget{}, errors.New("no server address: give one, or a trust token that carries the server's addresses")
	}
	t := addTarget{addrs: addrs, fingerprint: canonicalFingerprint(opts.Fingerprint)}
	switch {
	case tok != nil && tok.Fingerprint != "":
		t.fingerprint, t.how = strings.ToLower(tok.Fingerprint), "token"
	case t.fingerprint != "":
		t.how = "fingerprint"
	}
	return t, nil
}

// ensureClientCertificate generates this machine's client certificate if it has none yet.
func ensureClientCertificate(conf *cliconfig.Config, say func(string, ...any)) error {
	if conf.HasClientCertificate() {
		return nil
	}
	say("Generating a client certificate. This may take a minute...")
	return conf.GenerateClientCertificate()
}

// fetchServerCertificate takes the certificate of the first address that answers, and says which one it was. Nothing is sent to the
// server beyond the TLS handshake that shows its certificate.
func fetchServerCertificate(conf *cliconfig.Config, opts AddOptions, addrs []string) (cert *x509.Certificate, used string, err error) {
	fetch := opts.fetchCert
	if fetch == nil {
		fetch = func(addr string) (*x509.Certificate, error) {
			return localtls.GetRemoteCertificate(addr, conf.UserAgent)
		}
	}
	var errs []error
	for _, a := range addrs {
		if cert, err = fetch(a); err == nil {
			return cert, a, nil
		}
		errs = append(errs, err)
	}
	return nil, "", fmt.Errorf("could not reach the server: %w", errors.Join(errs...))
}

// verifyServerCertificate decides whether to trust the certificate the server showed, and returns its fingerprint and how it was
// verified: against the pinned fingerprint, by trust on first use (AcceptCertificate), or because the person confirmed it.
func verifyServerCertificate(cert *x509.Certificate, target addTarget, opts AddOptions) (got, how string, err error) {
	got, how = localtls.CertFingerprint(cert), target.how
	switch {
	case target.fingerprint != "":
		if got != target.fingerprint {
			return "", "", fmt.Errorf("the server's certificate (%s) is not the one expected from the %s (%s): refusing to trust it", got, how, target.fingerprint)
		}
	case opts.AcceptCertificate:
		how = "accepted"
	case opts.Confirm != nil:
		ok, err := opts.Confirm(got)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", errors.New("server certificate refused")
		}
		how = "confirmed"
	default:
		return "", "", fmt.Errorf("the server's certificate fingerprint is %s, and nothing verifies it: pass --fingerprint %s if it is right, use a trust token, or --accept-certificate to trust it on first use", got, got)
	}
	return got, how, nil
}

// storeServerCertificate writes the certificate where the client configuration looks for the named remote's, so that every later
// connection is pinned to exactly it, and returns that path.
func storeServerCertificate(conf *cliconfig.Config, name string, cert *x509.Certificate) (string, error) {
	certPath := conf.ServerCertPath(name)
	if err := os.MkdirAll(conf.ConfigPath("servercerts"), 0o750); err != nil {
		return "", fmt.Errorf("creating the server certificate directory: %w", err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0o644); err != nil {
		return "", fmt.Errorf("storing the server certificate: %w", err)
	}
	return certPath, nil
}

// checkManageable reads the server's state and refuses what is not an Incus server this client can manage: an image server, or one that
// does not offer TLS authentication.
func checkManageable(d incus.InstanceServer) (*api.Server, error) {
	srv, _, err := d.GetServer()
	if err != nil {
		return nil, fmt.Errorf("reading the server's state: %w", err)
	}
	if srv.Public {
		return nil, errors.New("that is an image server, not an Incus server to manage")
	}
	if !slices.Contains(srv.AuthMethods, api.AuthenticationMethodTLS) {
		return nil, fmt.Errorf("the server does not offer TLS authentication (it offers %v)", srv.AuthMethods)
	}
	return srv, nil
}

// establishTrust makes the server trust this machine's client certificate when it does not already: it presents the trust token, exactly
// as given (the server decodes its own token), and checks that the server now trusts the machine. Without a token it says what to ask for.
func establishTrust(conf *cliconfig.Config, d incus.InstanceServer, srv *api.Server, token string) error {
	if srv.Auth == "trusted" {
		return nil
	}
	if token == "" {
		fp, _ := clientFingerprint(conf)
		return fmt.Errorf("the server does not trust this machine's client certificate yet (fingerprint %s): give a trust token (`incus config trust add NAME` on the server), or ask its admin to run `incus config trust add-certificate` with %s", fp, conf.ConfigPath("client.crt"))
	}
	if err := d.CreateCertificate(api.CertificatesPost{TrustToken: token, CertificatePut: api.CertificatePut{Type: api.CertificateTypeClient}}); err != nil {
		return fmt.Errorf("presenting the trust token: %w", err)
	}
	after, _, err := d.GetServer()
	if err != nil {
		return err
	}
	if after.Auth != "trusted" {
		return errors.New("the server still does not trust this machine after the trust token was accepted")
	}
	return nil
}

// chooseProject follows `incus remote add`: an explicit project must exist; otherwise the only project the
// client may see, else the default project when it is visible, else the caller must choose.
func chooseProject(d incus.InstanceServer, project string) (string, error) {
	if project != "" {
		if _, _, err := d.GetProject(project); err != nil {
			return "", fmt.Errorf("project %q: %w", project, err)
		}
		return project, nil
	}
	if !d.HasExtension("projects") {
		return "", nil
	}
	names, err := d.GetProjectNames()
	if err != nil {
		return "", fmt.Errorf("listing projects: %w", err)
	}
	switch {
	case len(names) == 0:
		return "", nil
	case len(names) == 1:
		return names[0], nil
	case slices.Contains(names, api.ProjectDefaultName):
		return "", nil
	}
	return "", fmt.Errorf("this client may use several projects and not the default one (%s): choose with --project", strings.Join(names, ", "))
}

func clientFingerprint(conf *cliconfig.Config) (string, error) {
	b, err := os.ReadFile(conf.ConfigPath("client.crt"))
	if err != nil {
		return "", err
	}
	return localtls.CertFingerprintStr(string(b))
}

// List writes the remotes in conf that tink can manage (TLS or local Incus servers), then the built-in image remotes.
func List(conf *cliconfig.Config, builtin []string, out io.Writer) {
	names := make([]string, 0, len(conf.Remotes))
	for n := range conf.Remotes {
		names = append(names, n)
	}
	sort.Strings(names)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tADDRESS\tPROTOCOL\tAUTH\tPROJECT")
	for _, n := range names {
		r := conf.Remotes[n]
		project := r.Project
		if project == "" && r.Protocol == "incus" && !r.Public {
			project = "(default)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", n, strings.Join(r.Addrs, ","), r.Protocol, r.AuthType, project)
	}
	_ = w.Flush()
	var missing []string
	for _, b := range builtin {
		if _, ok := conf.Remotes[b]; !ok {
			missing = append(missing, b)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(out, "\nbuilt in, used when not configured: %s\n", strings.Join(missing, ", "))
	}
}

// Remove deletes a remote from conf and its stored server certificate. It cannot remove the server's trust of
// this machine: that is the server's to forget.
func Remove(conf *cliconfig.Config, name string) error {
	r, ok := conf.Remotes[name]
	if !ok {
		return fmt.Errorf("no remote %q", name)
	}
	if r.Static || name == "local" {
		return fmt.Errorf("remote %q is built in and cannot be removed", name)
	}
	delete(conf.Remotes, name)
	if err := conf.SaveConfig(conf.ConfigPath("config.yml")); err != nil {
		return fmt.Errorf("saving the Incus client configuration: %w", err)
	}
	if err := os.Remove(conf.ServerCertPath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing the stored server certificate: %w", err)
	}
	return nil
}
