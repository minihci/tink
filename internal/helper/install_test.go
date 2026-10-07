package helper

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/run"
)

var notFound = api.StatusErrorf(http.StatusNotFound, "not found")

// fakeHost is just enough of an Incus server to install a helper on and look at what was done. Methods the installer has no business
// calling panic through the nil embedded interface.
type fakeHost struct {
	incus.InstanceServer
	serverCfg  map[string]string
	defaultNIC bool
	networks   map[string]bool

	projects  map[string]bool
	instances map[string]*api.Instance // name -> instance (the project is on it)
	volumes   map[string]bool          // "pool/name"
	certs     []api.Certificate
	files     map[string]string // path -> content
	execs     []execCall
	execCode  int
	execOut   string
	tokens    int
	log       []string
}

type execCall struct {
	command []string
	stdin   string
	env     map[string]string
}

// cert is a trusted client certificate with a fingerprint made from n.
func cert(name string, n int) api.Certificate {
	return api.Certificate{CertificatePut: api.CertificatePut{Name: name, Type: "client"}, Fingerprint: fmt.Sprintf("%064d", n)}
}

func newHost() *fakeHost {
	return &fakeHost{
		serverCfg: map[string]string{"core.https_address": ":8443"},
		networks:  map[string]bool{"incusbr0": true},
		projects:  map[string]bool{}, instances: map[string]*api.Instance{}, volumes: map[string]bool{}, files: map[string]string{},
	}
}

type fakeOp struct {
	incus.Operation
	meta map[string]any
}

func (o fakeOp) Wait() error        { return nil }
func (o fakeOp) Get() api.Operation { return api.Operation{Metadata: o.meta} }

func (h *fakeHost) UseProject(string) incus.InstanceServer { return h }

func (h *fakeHost) GetServer() (*api.Server, string, error) {
	srv := &api.Server{}
	srv.Config = h.serverCfg
	return srv, "", nil
}

func (h *fakeHost) GetProject(n string) (*api.Project, string, error) {
	if !h.projects[n] {
		return nil, "", notFound
	}
	return &api.Project{Name: n}, "", nil
}

func (h *fakeHost) CreateProject(p api.ProjectsPost) error {
	h.projects[p.Name] = true
	h.log = append(h.log, "project "+p.Name)
	return nil
}

func (h *fakeHost) DeleteProject(n string) error {
	delete(h.projects, n)
	return nil
}

func (h *fakeHost) GetProfile(n string) (*api.Profile, string, error) {
	p := &api.Profile{Name: n, ProfilePut: api.ProfilePut{Devices: map[string]map[string]string{"root": {"type": "disk"}}}}
	if h.defaultNIC {
		p.Devices["eth0"] = map[string]string{"type": "nic"}
	}
	return p, "", nil
}

func (h *fakeHost) GetNetwork(n string) (*api.Network, string, error) {
	if !h.networks[n] {
		return nil, "", notFound
	}
	return &api.Network{Name: n}, "", nil
}

func (h *fakeHost) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	var out []api.Instance
	for _, i := range h.instances {
		out = append(out, *i)
	}
	return out, nil
}

func (h *fakeHost) GetInstance(n string) (*api.Instance, string, error) {
	i, ok := h.instances[n]
	if !ok {
		return nil, "", notFound
	}
	c := *i
	c.Config = map[string]string{}
	for k, v := range i.Config {
		c.Config[k] = v
	}
	return &c, "etag", nil
}

func (h *fakeHost) UpdateInstance(n string, put api.InstancePut, _ string) (incus.Operation, error) {
	h.instances[n].InstancePut = put
	h.log = append(h.log, "configure "+n)
	return fakeOp{}, nil
}

func (h *fakeHost) UpdateInstanceState(n string, put api.InstanceStatePut, _ string) (incus.Operation, error) {
	switch put.Action {
	case "start", "restart":
		h.instances[n].Status = "Running"
	case "stop":
		h.instances[n].Status = "Stopped"
	}
	h.log = append(h.log, put.Action+" "+n)
	return fakeOp{}, nil
}

func (h *fakeHost) DeleteInstance(n string) (incus.Operation, error) {
	delete(h.instances, n)
	h.log = append(h.log, "delete "+n)
	return fakeOp{}, nil
}

func (h *fakeHost) CreateInstanceFile(_, path string, args incus.InstanceFileArgs) error {
	b := new(strings.Builder)
	buf := make([]byte, 4096)
	for {
		n, err := args.Content.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	h.files[path] = b.String()
	h.log = append(h.log, fmt.Sprintf("push %s mode %o", path, args.Mode))
	return nil
}

func (h *fakeHost) DeleteInstanceFile(_, path string) error {
	h.log = append(h.log, "delete file "+path)
	return nil
}

func (h *fakeHost) GetStoragePoolVolume(pool, _, n string) (*api.StorageVolume, string, error) {
	if !h.volumes[pool+"/"+n] {
		return nil, "", notFound
	}
	return &api.StorageVolume{Name: n}, "", nil
}

func (h *fakeHost) CreateStoragePoolVolume(pool string, v api.StorageVolumesPost) error {
	h.volumes[pool+"/"+v.Name] = true
	return nil
}

func (h *fakeHost) DeleteStoragePoolVolume(pool, _, n string) error {
	delete(h.volumes, pool+"/"+n)
	h.log = append(h.log, "delete volume "+n)
	return nil
}

func (h *fakeHost) GetCertificates() ([]api.Certificate, error) { return h.certs, nil }

func (h *fakeHost) DeleteCertificate(fp string) error {
	var keep []api.Certificate
	for _, c := range h.certs {
		if c.Fingerprint != fp {
			keep = append(keep, c)
		}
	}
	h.certs = keep
	h.log = append(h.log, "revoke "+fp[:6])
	return nil
}

// tokenOp is the operation Incus returns for a trust token: it does not end until the token is redeemed or expires, so waiting on it
// hangs for hours. The fake fails the test instead of hanging.
type tokenOp struct {
	incus.Operation
	h    *fakeHost
	meta map[string]any
}

func (o tokenOp) Wait() error {
	panic("waited on a token operation: it does not end until the token is used, so this would hang for hours")
}
func (o tokenOp) Get() api.Operation { return api.Operation{Metadata: o.meta} }
func (o tokenOp) Cancel() error {
	o.h.log = append(o.h.log, "cancel token")
	return nil
}

func (h *fakeHost) CreateCertificateToken(c api.CertificatesPost) (incus.Operation, error) {
	h.tokens++
	h.log = append(h.log, "mint token for "+c.Name)
	return tokenOp{h: h, meta: map[string]any{
		"request": map[string]any{"name": c.Name}, "secret": fmt.Sprintf("secret-%d", h.tokens),
		"fingerprint": "serverfp", "addresses": []any{"127.0.0.1:8443"},
	}}, nil
}

// ExecInstance stands for `tink remote add` inside the helper: when the token arrives, the host trusts a new certificate.
func (h *fakeHost) ExecInstance(_ string, post api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	buf := new(strings.Builder)
	b := make([]byte, 4096)
	for {
		n, err := args.Stdin.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	h.execs = append(h.execs, execCall{command: post.Command, stdin: buf.String(), env: post.Environment})
	args.Stdout.Write([]byte(h.execOut))
	close(args.DataDone)
	if h.execCode == 0 && contains(post.Command, "add") { // `tink remote add`: the host now trusts the new certificate
		h.certs = append(h.certs, cert(TrustName, h.tokens))
	}
	return fakeOp{meta: map[string]any{"return": float64(h.execCode)}}, nil
}

// creator is Installer.Create: it makes the stopped instance the way run.Create would, and records the spec it was given.
func (h *fakeHost) creator(specs *[]*run.Spec) func(incus.InstanceServer, *run.Spec, string) error {
	return func(_ incus.InstanceServer, s *run.Spec, project string) error {
		*specs = append(*specs, s)
		h.instances[s.Name] = &api.Instance{Name: s.Name, Project: project, Status: "Stopped"}
		h.log = append(h.log, "create "+s.Name+" from "+s.Image)
		return nil
	}
}

func installer(h *fakeHost, specs *[]*run.Spec) (*Installer, *strings.Builder) {
	out := new(strings.Builder)
	return &Installer{Server: h, Out: out, Create: h.creator(specs), ReadFile: func(p string) ([]byte, error) { return []byte("ELF-" + p), nil },
		Sleep: func(time.Duration) {}}, out
}

func TestInstallCreatesTheHelperAndEnrolsItWithTheTokenOnStandardInput(t *testing.T) {
	h := newHost()
	var specs []*run.Spec
	in, out := installer(h, &specs)

	if err := in.Install(InstallOptions{Binary: "/tmp/tink-linux", TZ: "America/Denver", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("one instance: %d", len(specs))
	}
	s := specs[0]
	if s.Image != stockImage || s.Name != "helper" {
		t.Errorf("--binary with no --image means a stock image: %q %q", s.Image, s.Name)
	}
	if !h.projects["tink-helper"] {
		t.Error("the helper gets a project of its own")
	}

	// what the instance is
	for k, want := range map[string]string{"boot.autostart": "true", "boot.autorestart": "true", "environment.HOME": "/root", "environment.TZ": "America/Denver", MarkerKey: "1", MarkerKey + ".pool": "default"} {
		if got := s.Config[k]; got != want {
			t.Errorf("config %s = %q, want %q", k, got, want)
		}
	}
	entry := s.Config["oci.entrypoint"]
	for _, want := range []string{"/usr/local/bin/tink daemon run", "--remote host", "--jobs /data/jobs", "--status-instance helper", "--status-project tink-helper", "--timezone America/Denver"} {
		if !strings.Contains(entry, want) {
			t.Errorf("the entrypoint must contain %q: %s", want, entry)
		}
	}
	if strings.Contains(entry, "INCUS_SOCKET") || strings.Contains(s.Config["environment.HOME"], "INCUS") {
		t.Error("the instance must never set INCUS_SOCKET or INCUS_DIR: Incus's own start hook inherits them and the instance fails to start")
	}

	// how it reaches the host, and what it mounts
	api, root := s.Devices["api"], s.Devices["root"]
	if api["type"] != "proxy" || api["bind"] != "container" || api["listen"] != "tcp:127.0.0.1:8443" || api["connect"] != "tcp:127.0.0.1:8443" {
		t.Errorf("a loopback proxy to the host's API, inside the container: %v", api)
	}
	if root["pool"] != "default" || s.Devices["config"]["path"] != "/root/.config/incus" || s.Devices["data"]["path"] != "/data" {
		t.Errorf("%v", s.Devices)
	}
	if s.Devices["eth0"]["network"] != "incusbr0" {
		t.Errorf("a NIC, because the loopback proxy needs the container's loopback up: %v", s.Devices["eth0"])
	}
	if !h.volumes["default/tink-helper-config"] || !h.volumes["default/tink-helper-data"] {
		t.Errorf("both volumes exist: %v", h.volumes)
	}

	// the binary, started, and enrolled
	if got := h.files["/usr/local/bin/tink"]; got != "ELF-/tmp/tink-linux" {
		t.Errorf("the binary is pushed into the instance: %q", got)
	}
	if !contains(h.log, "push /usr/local/bin/tink mode 755") || !contains(h.log, "start helper") {
		t.Errorf("%v", h.log)
	}
	if len(h.execs) != 1 {
		t.Fatalf("one enrolment: %v", h.execs)
	}
	e := h.execs[0]
	if strings.Join(e.command, " ") != "/usr/local/bin/tink remote add host https://127.0.0.1:8443 --token-file -" {
		t.Errorf("command = %v", e.command)
	}
	if !strings.HasPrefix(e.stdin, "") || e.stdin == "" || strings.Contains(strings.Join(e.command, " "), e.stdin) {
		t.Errorf("the token is on standard input and never on the command line: stdin=%q", e.stdin)
	}
	if e.env["HOME"] != "/root" {
		t.Errorf("%v", e.env)
	}
	if len(h.certs) != 1 || h.certs[0].Name != TrustName {
		t.Errorf("the host now trusts the helper's certificate: %+v", h.certs)
	}
	if !strings.Contains(out.String(), "its key is made inside the instance") {
		t.Errorf("%s", out.String())
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestInstallTwiceDoesNotCreateOrEnrolTwice(t *testing.T) {
	h := newHost()
	var specs []*run.Spec
	in, out := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "ghcr.io/x/tink-helper:1", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	h.log = nil
	if err := in.Install(InstallOptions{Image: "ghcr.io/x/tink-helper:1", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || h.tokens != 1 || len(h.execs) != 1 {
		t.Errorf("created %d, tokens %d, enrolments %d: install is safe to run again", len(specs), h.tokens, len(h.execs))
	}
	if !strings.Contains(out.String(), "already enrolled") || !strings.Contains(out.String(), "exists") {
		t.Errorf("it says what it found:\n%s", out.String())
	}
}

func TestReissueReplacesTheCertificate(t *testing.T) {
	h := newHost()
	var specs []*run.Spec
	in, _ := installer(h, &specs)
	in.Install(InstallOptions{Image: "x", Wait: -1})
	old := h.certs[0].Fingerprint
	if err := in.Install(InstallOptions{Image: "x", Reissue: true, Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if got := commandsRun(h); h.tokens != 2 || len(got) != 3 || got[1] != "remote remove host" || !strings.HasPrefix(got[2], "remote add host") {
		t.Errorf("a fresh token, the old remote forgotten, and a second enrolment: tokens %d, %v", h.tokens, got)
	}
	if len(h.certs) != 1 || h.certs[0].Fingerprint == old {
		t.Errorf("the old certificate is revoked and a new one trusted: %+v", h.certs)
	}
	if len(specs) != 1 {
		t.Error("the instance itself is not recreated")
	}
}

func TestInstallRefusesASecondHelper(t *testing.T) {
	h := newHost()
	h.instances["other"] = &api.Instance{Name: "other", Project: "elsewhere", Status: "Running", InstancePut: api.InstancePut{Config: map[string]string{MarkerKey: "1"}}}
	var specs []*run.Spec
	in, _ := installer(h, &specs)
	err := in.Install(InstallOptions{Image: "x", Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "already a helper") || !strings.Contains(err.Error(), "elsewhere/other") {
		t.Errorf("err = %v", err)
	}
	if len(specs) != 0 || len(h.projects) != 0 {
		t.Error("nothing is created")
	}
}

func TestInstallSaysWhatIsMissingBeforeTouchingAnything(t *testing.T) {
	cases := map[string]struct {
		mut  func(*fakeHost)
		opts InstallOptions
		want string
	}{
		"no image and no binary":      {func(*fakeHost) {}, InstallOptions{}, "no image given"},
		"no API listener":             {func(h *fakeHost) { h.serverCfg = map[string]string{} }, InstallOptions{Image: "x"}, "no HTTPS API listener"},
		"no network to join":          {func(h *fakeHost) { h.networks = map[string]bool{} }, InstallOptions{Image: "x"}, "needs a network"},
		"a network that is not there": {func(*fakeHost) {}, InstallOptions{Image: "x", Network: "nope"}, "no such network"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost()
			tc.mut(h)
			var specs []*run.Spec
			in, _ := installer(h, &specs)
			tc.opts.Wait = -1
			err := in.Install(tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
			if len(h.projects) != 0 || len(specs) != 0 || len(h.log) != 0 {
				t.Errorf("it must say so before creating anything: %v %v", h.projects, h.log)
			}
		})
	}
}

func TestTheNICComesFromTheDefaultProfileWhenItHasOne(t *testing.T) {
	h := newHost()
	h.defaultNIC = true
	var specs []*run.Spec
	in, _ := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if _, has := specs[0].Devices["eth0"]; has {
		t.Errorf("no second NIC when the profile already gives one: %v", specs[0].Devices)
	}
	h = newHost()
	h.networks["lan"] = true
	in, _ = installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", Network: "lan", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if got := specs[1].Devices["eth0"]["network"]; got != "lan" {
		t.Errorf("--network wins: %q", got)
	}
}

func TestConnectAddress(t *testing.T) {
	for in, want := range map[string]string{
		":8443": "127.0.0.1:8443", "0.0.0.0:8443": "127.0.0.1:8443", "[::]:8443": "127.0.0.1:8443",
		"127.0.0.1:8443": "127.0.0.1:8443", "10.0.0.5:9443": "10.0.0.5:9443", "[fd00::1]:8443": "[fd00::1]:8443",
	} {
		if got, err := connectAddress(in); err != nil || got != want {
			t.Errorf("%q -> %q (%v), want %q", in, got, err, want)
		}
	}
	if _, err := connectAddress("nonsense"); err == nil {
		t.Error("an address that is not host:port is an error")
	}
}

func TestAnEnrolmentThatFailsSaysWhy(t *testing.T) {
	h := newHost()
	h.execCode, h.execOut = 1, "Error: the token was already used\n"
	var specs []*run.Spec
	in, _ := installer(h, &specs)
	err := in.Install(InstallOptions{Image: "x", Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "exit 1") || !strings.Contains(err.Error(), "already used") {
		t.Errorf("err = %v", err)
	}
	if len(h.certs) != 0 {
		t.Error("nothing is trusted")
	}
	if !contains(h.log, "cancel token") {
		t.Errorf("a token whose enrolment failed must not be left live for hours: %v", h.log)
	}
}

func TestInstallWaitsForTheFirstStatusAndSaysWhatItSaw(t *testing.T) {
	h := newHost()
	var specs []*run.Spec
	in, out := installer(h, &specs)
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	in.Now = func() time.Time { return clock }
	sleeps := 0
	in.Sleep = func(d time.Duration) {
		sleeps++
		clock = clock.Add(d)
		if sleeps == 1 { // a document from BEFORE the enrolment: a helper that was revoked and enrolled again still has one
			st := Status{Proto: 1, Tick: clock.Add(-time.Hour), HeartbeatSeconds: 7200}
			text, _ := Encode(st)
			h.instances["helper"].Config[StatusKey] = text
		}
		if sleeps == 3 { // the helper's first status document since the enrolment arrives
			st := Status{Proto: 1, Tick: clock, HeartbeatSeconds: 600}
			text, _ := Encode(st)
			h.instances["helper"].Config[StatusKey] = text
		}
	}
	if err := in.Install(InstallOptions{Image: "x", Wait: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "healthy: tink-helper/helper") || sleeps != 3 || strings.Count(out.String(), "healthy") != 1 {
		t.Errorf("sleeps %d:\n%s", sleeps, out.String())
	}

	// and when it never reports, the install is not a failure: it says what to run
	h = newHost()
	in, out = installer(h, &specs)
	clock = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	in.Now = func() time.Time { return clock }
	in.Sleep = func(d time.Duration) { clock = clock.Add(d) }
	if err := in.Install(InstallOptions{Image: "x", Wait: 10 * time.Second}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "has not published a status document since, within 10s") || !strings.Contains(out.String(), "tink helper status") {
		t.Errorf("%s", out.String())
	}
}

func helperRunning(h *fakeHost) {
	h.instances["helper"] = &api.Instance{Name: "helper", Project: "tink-helper", Status: "Running", InstancePut: api.InstancePut{Config: map[string]string{MarkerKey: "1", MarkerKey + ".pool": "fast"}}}
	h.projects["tink-helper"] = true
	h.volumes["fast/tink-helper-config"], h.volumes["fast/tink-helper-data"] = true, true
	h.certs = []api.Certificate{cert(TrustName, 7), cert("someone-else", 9)}
}

func TestRemoveRevokesTheCertificateStopsAndDeletesButKeepsTheVolumes(t *testing.T) {
	h := newHost()
	helperRunning(h)
	in, out := installer(h, new([]*run.Spec))
	if err := in.Remove(RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if len(h.instances) != 0 {
		t.Error("the instance is deleted")
	}
	if len(h.certs) != 1 || h.certs[0].Name != "someone-else" {
		t.Errorf("only the helper's own certificate is revoked: %+v", h.certs)
	}
	order := strings.Join(h.log, " | ")
	if !(strings.Index(order, "revoke") < strings.Index(order, "stop helper") && strings.Index(order, "stop helper") < strings.Index(order, "delete helper")) {
		t.Errorf("revoke first, then stop, then delete: %s", order)
	}
	if !h.volumes["fast/tink-helper-config"] || !h.volumes["fast/tink-helper-data"] || !h.projects["tink-helper"] {
		t.Errorf("without --purge the volumes and the project stay: %v %v", h.volumes, h.projects)
	}
	if !strings.Contains(out.String(), "kept the volumes") {
		t.Errorf("%s", out.String())
	}
}

func TestRemovePurgeDeletesTheVolumesInTheirOwnPoolAndTheProject(t *testing.T) {
	h := newHost()
	helperRunning(h)
	in, _ := installer(h, new([]*run.Spec))
	if err := in.Remove(RemoveOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	var left []string
	for v := range h.volumes {
		left = append(left, v)
	}
	sort.Strings(left)
	if len(left) != 0 || h.projects["tink-helper"] {
		t.Errorf("volumes %v, project %v: the pool it was installed in is read from the instance", left, h.projects)
	}
}

func TestRemoveWithNoHelperIsAnError(t *testing.T) {
	in, _ := installer(newHost(), new([]*run.Spec))
	if err := in.Remove(RemoveOptions{}); err == nil || !strings.Contains(err.Error(), "no helper found") {
		t.Errorf("%v", err)
	}
	h := newHost()
	helperRunning(h)
	in, _ = installer(h, new([]*run.Spec))
	if err := in.Remove(RemoveOptions{Name: "someone-else"}); err == nil {
		t.Error("a name that matches no helper removes nothing")
	}
	if len(h.instances) != 1 || len(h.certs) != 2 {
		t.Error("and revokes nothing")
	}
}

var _ = errors.New

func commandsRun(h *fakeHost) []string {
	var out []string
	for _, e := range h.execs {
		out = append(out, strings.Join(e.command[1:], " "))
	}
	return out
}

func TestAFirstInstallDoesNotForgetAnythingBecauseThereIsNothingToForget(t *testing.T) {
	h := newHost()
	in, _ := installer(h, new([]*run.Spec))
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if got := commandsRun(h); len(got) != 1 || !strings.HasPrefix(got[0], "remote add host") {
		t.Errorf("only the enrolment: %v", got)
	}
}

func TestInstallingAgainAfterARevocationEnrolsTheSameKeyAgain(t *testing.T) {
	h := newHost()
	in, out := installer(h, new([]*run.Spec))
	in.Install(InstallOptions{Image: "x", Wait: -1})
	h.certs = nil // revoked: incus config trust remove
	h.log = nil
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	got := commandsRun(h)
	if len(got) != 3 || got[1] != "remote remove host" || !strings.HasPrefix(got[2], "remote add host") {
		t.Errorf("`remote add` will not replace a remote, so the old one is forgotten first: %v", got)
	}
	for _, l := range h.log {
		if strings.HasPrefix(l, "delete file") {
			t.Errorf("without --reissue the helper keeps its key, and the host is told to trust it again: %v", h.log)
		}
	}
	if len(h.certs) != 1 {
		t.Errorf("trusted again: %+v", h.certs)
	}
	if !strings.Contains(out.String(), "forgetting the helper's earlier enrolment") {
		t.Errorf("%s", out.String())
	}
}

func TestReissueAlsoRemovesTheOldKeyPairSoANewOneIsMade(t *testing.T) {
	h := newHost()
	in, _ := installer(h, new([]*run.Spec))
	in.Install(InstallOptions{Image: "x", Wait: -1})
	h.log = nil
	if err := in.Install(InstallOptions{Image: "x", Reissue: true, Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if !contains(h.log, "delete file /root/.config/incus/client.key") || !contains(h.log, "delete file /root/.config/incus/client.crt") {
		t.Errorf("the key and certificate are deleted: %v", h.log)
	}
	order := strings.Join(h.log, " | ")
	if !(strings.Index(order, "revoke") < strings.Index(order, "delete file") && strings.Index(order, "delete file") < strings.Index(order, "mint token")) {
		t.Errorf("revoke the old certificate, remove the old key, then mint a token: %s", order)
	}
}

func TestCheckTrustCatchesARevocationAtOnceInsteadOfAfterTheHeartbeatsGoStale(t *testing.T) {
	f := found("Running", fresh(nil))
	f.Config[MarkerKey+".remote"] = "https://127.0.0.1:8443"
	r := Evaluate(f, t0)
	if r.Health != Healthy {
		t.Fatalf("its last document is fresh: %v", r.Reasons)
	}
	r.CheckTrust([]api.Certificate{cert("someone-else", 1)})
	if r.Health != Down || !strings.Contains(r.Summary(), "not in the host's trust store") || !strings.Contains(r.Summary(), "--reissue") {
		t.Errorf("%s", r.Summary())
	}
	// present: nothing to say
	r = Evaluate(f, t0)
	r.CheckTrust([]api.Certificate{cert(TrustName, 1)})
	if r.Health != Healthy {
		t.Errorf("%s", r.Summary())
	}
	// a helper that has no certificate of its own (the socket fallback) is not judged by the trust store
	g := found("Running", fresh(nil))
	r = Evaluate(g, t0)
	r.CheckTrust(nil)
	if r.Health != Healthy {
		t.Errorf("%s", r.Summary())
	}
}

func TestReleaseImageIsOnlyForAReleaseBuild(t *testing.T) {
	for in, want := range map[string]string{
		"v1.2.3":                               "ghcr:minihci/tink-helper:v1.2.3",
		"v0.1.0":                               "ghcr:minihci/tink-helper:v0.1.0",
		"v1.2.3-rc.1":                          "ghcr:minihci/tink-helper:v1.2.3-rc.1",
		"":                                     "", // a build that was given no version
		"unknown":                              "",
		"(devel)":                              "",
		"v0.0.0-20261007111551-e8a148ba0ea5":   "", // a Go pseudo-version: a commit, not a release, and no image exists for it
		"v1.2.4-0.20261007111551-e8a148ba0ea5": "",
		"v1.2.3+dirty":                         "",
		"1.2.3":                                "", // no leading v: not a tag this repository publishes
	} {
		if got := ReleaseImage(in); got != want {
			t.Errorf("ReleaseImage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstallRecordsTheExactImageSoAnUpgradeCanTellWhichItIs(t *testing.T) {
	h := newHost()
	var specs []*run.Spec
	in, _ := installer(h, &specs)
	base := in.Create
	in.Create = func(s incus.InstanceServer, spec *run.Spec, project string) error {
		if err := base(s, spec, project); err != nil {
			return err
		}
		// what Incus records when it makes an instance from an image: the image's fingerprint (an OCI image's digest)
		h.instances[spec.Name].Config = map[string]string{"volatile.base_image": "sha256:abc123"}
		return nil
	}
	if err := in.Install(InstallOptions{Image: "ghcr:minihci/tink-helper:v1.2.3", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if got := specs[0].Config[MarkerKey+".image-fingerprint"]; got != "sha256:abc123" {
		t.Errorf("a tag can move and a fingerprint cannot: %q", got)
	}
	if got := h.instances["helper"].Config[MarkerKey+".image-fingerprint"]; got != "sha256:abc123" {
		t.Errorf("and it is on the instance: %q", got)
	}
}
