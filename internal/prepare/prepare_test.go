package prepare

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/resolve"
)

// recorder builds steps that only write down the order they were called in, and what each was handed, so the order of the passes is
// what the test asserts. mark is what a pass leaves on the resources, standing for the in-place changes the real ones make.
type recorder struct {
	calls []string
	seen  map[string][]string // for each pass, the marks on the resources it was handed
}

func (r *recorder) steps() steps {
	r.seen = map[string][]string{}
	note := func(pass string, rs []resolve.Resource) {
		r.calls = append(r.calls, pass)
		for _, x := range rs {
			r.seen[pass] = append(r.seen[pass], x.Description)
		}
	}
	mark := func(rs []resolve.Resource, m string) {
		for i := range rs {
			rs[i].Description += m
		}
	}
	return steps{
		load: func(files []string) ([]resolve.Resource, error) {
			r.calls = append(r.calls, "load")
			return []resolve.Resource{{Kind: resolve.KindProfile, Name: "p", Description: "L"}}, nil
		},
		defaults: func(rs []resolve.Resource) ([]resolve.Resource, error) {
			note("defaults", rs)
			mark(rs, "D")
			return rs, nil
		},
		secrets: func(rs []resolve.Resource, req Request) ([]resolve.Resource, error) {
			note("secrets", rs)
			mark(rs, "S")
			return rs, nil
		},
		connect: func(socket string) (incus.InstanceServer, error) {
			r.calls = append(r.calls, "connect")
			return &fakeServer{}, nil // a server there is; the passes are stubs that do not use it
		},
		ownNetwork: func(server incus.InstanceServer, rs []resolve.Resource) ([]resolve.Resource, []string, error) {
			note("ownNetwork", rs)
			mark(rs, "N")
			return rs, []string{"network notice"}, nil
		},
		aclRules: func(server incus.InstanceServer, rs []resolve.Resource) ([]string, error) {
			note("aclRules", rs)
			mark(rs, "A")
			return []string{"acl notice"}, nil
		},
		derivedFiles: func(server incus.InstanceServer, rs []resolve.Resource) ([]string, error) {
			note("derivedFiles", rs)
			mark(rs, "F")
			return []string{"files notice"}, nil
		},
		qualify: func(server incus.InstanceServer, rs []resolve.Resource) ([]string, error) {
			note("qualify", rs)
			mark(rs, "Q")
			return []string{"first notice", "second notice"}, nil
		},
		levels: func(rs []resolve.Resource) ([][]resolve.Resource, error) {
			note("levels", rs)
			return [][]resolve.Resource{rs}, nil
		},
	}
}

// The order of the passes is the contract: each one reads what the one before left on the resources, and Levels is last because it
// builds the graph from them. A pass added in the wrong place shows up here, and so does one added to only one command (there is only
// one place to add it).
func TestPassesRunInOrderEachSeeingWhatTheLastLeft(t *testing.T) {
	r := &recorder{}
	var notices []string
	got, err := prepare(Request{Notice: func(l string) { notices = append(notices, l) }}, r.steps())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"load", "defaults", "secrets", "connect", "ownNetwork", "aclRules", "derivedFiles", "qualify", "levels"}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("passes ran as %v, want %v", r.calls, want)
	}
	for pass, want := range map[string]string{"defaults": "L", "secrets": "LD", "ownNetwork": "LDS", "aclRules": "LDSN", "derivedFiles": "LDSNA", "qualify": "LDSNAF", "levels": "LDSNAFQ"} {
		if seen := r.seen[pass]; len(seen) != 1 || seen[0] != want {
			t.Errorf("%s was handed %v, want a resource marked %q", pass, seen, want)
		}
	}
	if len(got.Levels) != 1 || got.Levels[0][0].Description != "LDSNAFQ" || got.Resources[0].Description != "LDSNAFQ" {
		t.Errorf("result = %+v", got)
	}
	if want := []string{"network notice", "acl notice", "files notice", "first notice", "second notice"}; !reflect.DeepEqual(notices, want) {
		t.Errorf("notices = %v, want %v", notices, want)
	}
}

// Each failure stops the passes after it, and the ones before it have run.
func TestAFailingPassStopsTheRest(t *testing.T) {
	boom := errors.New("boom")
	order := []string{"load", "defaults", "secrets", "connect", "ownNetwork", "aclRules", "derivedFiles", "qualify", "levels"}
	for i, pass := range order {
		r := &recorder{}
		s := r.steps()
		fail := func() { r.calls = append(r.calls, "FAIL") }
		switch pass {
		case "load":
			s.load = func([]string) ([]resolve.Resource, error) { fail(); return nil, boom }
		case "defaults":
			s.defaults = func([]resolve.Resource) ([]resolve.Resource, error) { fail(); return nil, boom }
		case "secrets":
			s.secrets = func([]resolve.Resource, Request) ([]resolve.Resource, error) { fail(); return nil, boom }
		case "connect":
			s.connect = func(string) (incus.InstanceServer, error) { fail(); return nil, boom }
		case "ownNetwork":
			s.ownNetwork = func(incus.InstanceServer, []resolve.Resource) ([]resolve.Resource, []string, error) {
				fail()
				return nil, nil, boom
			}
		case "aclRules":
			s.aclRules = func(incus.InstanceServer, []resolve.Resource) ([]string, error) { fail(); return nil, boom }
		case "derivedFiles":
			s.derivedFiles = func(incus.InstanceServer, []resolve.Resource) ([]string, error) { fail(); return nil, boom }
		case "qualify":
			s.qualify = func(incus.InstanceServer, []resolve.Resource) ([]string, error) { fail(); return nil, boom }
		case "levels":
			s.levels = func([]resolve.Resource) ([][]resolve.Resource, error) { fail(); return nil, boom }
		}
		want := "boom"
		if pass == "connect" {
			want = "connecting to incus: boom"
		}
		got, err := prepare(Request{}, s)
		if err == nil || err.Error() != want || got != nil {
			t.Errorf("%s: got %v, %v; want nothing and %q", pass, got, err, want)
		}
		wantCalls := append(append([]string{}, order[:i]...), "FAIL")
		if !reflect.DeepEqual(r.calls, wantCalls) {
			t.Errorf("%s: passes ran as %v, want %v", pass, r.calls, wantCalls)
		}
	}
}

// What the person is told is told as it is known: a later failure does not swallow it.
func TestNoticesAreSaidEvenWhenALaterPassFails(t *testing.T) {
	r := &recorder{}
	s := r.steps()
	s.levels = func([]resolve.Resource) ([][]resolve.Resource, error) {
		return nil, errors.New("duplicate resource name")
	}
	var notices []string
	if _, err := prepare(Request{Notice: func(l string) { notices = append(notices, l) }}, s); err == nil {
		t.Fatal("expected the error")
	}
	if len(notices) != 5 {
		t.Errorf("notices = %v, want all five (the three derived ones and the two about images)", notices)
	}
}

func TestANilNoticeDropsThem(t *testing.T) {
	r := &recorder{}
	if _, err := prepare(Request{}, r.steps()); err != nil {
		t.Fatal(err)
	}
}

const stackFile = `kind: stack
name: site
project: lab
---
kind: profile
name: base
---
kind: storage-volume
name: data
---
kind: instance
name: app
image: local-image
profiles: [base]
devices:
  data:
    type: disk
    pool: default
    source: data
    path: /data
`

// With the real passes, only the connection replaced: the stack's default project reaches every resource, and the levels follow the
// dependencies the resources name.
func TestTheRealPassesBuildTheLevels(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(file, []byte(stackFile), 0o644); err != nil {
		t.Fatal(err)
	}
	s := defaultSteps()
	s.connect = func(string) (incus.InstanceServer, error) { return nil, nil }
	// the image is a local alias: a stack that declares its images (or names one the server has) is not qualified against a registry
	s.qualify = func(incus.InstanceServer, []resolve.Resource) ([]string, error) { return nil, nil }
	got, err := prepare(Request{Files: []string{file}}, s)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i, level := range got.Levels {
		for _, r := range level {
			names = append(names, strings.Join([]string{string(rune('0' + i)), string(r.Kind), r.Name, r.Project}, " "))
		}
	}
	want := []string{"0 profile base lab", "0 storage-volume data lab", "1 instance app lab"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("levels = %v, want %v", names, want)
	}
	if len(got.Resources) != 4 {
		t.Errorf("resources = %d, want the stack's four documents (the stack declaration included)", len(got.Resources))
	}
}

// With the real passes, a stack's defaults reach the levels the commands plan from: the instance is planned with the config, the NIC name and
// the volume's backup block it did not write.
func TestTheStacksDefaultsReachTheLevels(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stack.yaml")
	text := `kind: stack
name: site
defaults:
  instance:
    config: {boot.autostart: "true"}
    devices: {nic: {name: eth0}}
  storage-volume:
    backup: {none: regenerable}
---
kind: storage-volume
name: data
---
kind: instance
name: app
image: local-image
devices:
  eth0: {type: nic, network: lab0}
`
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	s := defaultSteps()
	s.connect = func(string) (incus.InstanceServer, error) { return nil, nil }
	s.qualify = func(incus.InstanceServer, []resolve.Resource) ([]string, error) { return nil, nil }
	got, err := prepare(Request{Files: []string{file}}, s)
	if err != nil {
		t.Fatal(err)
	}
	planned := map[string]resolve.Resource{}
	for _, level := range got.Levels {
		for _, r := range level {
			planned[r.Name] = r
		}
	}
	if app := planned["app"]; app.Config["boot.autostart"] != "true" || app.Devices["eth0"]["name"] != "eth0" {
		t.Errorf("app = %+v, want the defaults filled in", app)
	}
	if data := planned["data"]; data.Backup == nil || data.Backup.None != "regenerable" {
		t.Errorf("data = %+v, want the default backup block", data)
	}
	// and Load, for the commands that never plan, sees the same
	loaded, err := Load([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range loaded {
		if r.Name == "data" && r.Backup == nil {
			t.Error("Load must fill the defaults too")
		}
	}
}

func TestAConnectionErrorNamesIncus(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "stack.yaml")
	if err := os.WriteFile(file, []byte(stackFile), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Prepare(Request{Files: []string{file}, Socket: filepath.Join(dir, "no-such.sock")})
	if err == nil || !strings.HasPrefix(err.Error(), "connecting to incus: ") {
		t.Errorf("err = %v", err)
	}
}

// A stack with no ${secret:...} reference never opens a store: a store that is not there is no error.
func TestSecretsAreNotOpenedWhenNothingReferencesThem(t *testing.T) {
	rs := []resolve.Resource{{Kind: resolve.KindProfile, Name: "p"}}
	got, err := expandSecrets(rs, Request{SecretStore: "/nonexistent/secrets.yaml"})
	if err != nil || len(got) != 1 {
		t.Errorf("got %v, %v", got, err)
	}
}

// The derive pass reads the resources the secrets pass left and the graph reads what it leaves, so a derived value is planned as if it had been
// written; with the real pass and a stack that asks for nothing, it does not touch the server (here there is none).
func TestTheRealDerivePassAsksNothingOfAStackThatWantsNothing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "stack.yaml")
	if err := os.WriteFile(file, []byte(stackFile), 0o644); err != nil {
		t.Fatal(err)
	}
	s := defaultSteps()
	s.connect = func(string) (incus.InstanceServer, error) { return nil, nil }
	s.qualify = func(incus.InstanceServer, []resolve.Resource) ([]string, error) { return nil, nil }
	var notices []string
	if _, err := prepare(Request{Files: []string{file}, Notice: func(l string) { notices = append(notices, l) }}, s); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 0 {
		t.Errorf("notices = %v, want none", notices)
	}
}

// fakeServer stands for a connection: the stub passes never call it.
type fakeServer struct{ incus.InstanceServer }

// With no server to reach and NoServerOK, the passes that read the server are told so when they have something to derive, the images are left as
// written, and the rest runs; without NoServerOK the connection error stops it.
func TestNoServerOK(t *testing.T) {
	boom := errors.New("no socket")
	r := &recorder{}
	s := r.steps()
	s.connect = func(string) (incus.InstanceServer, error) { return nil, boom }
	var notices []string
	got, err := prepare(Request{NoServerOK: true, Notice: func(l string) { notices = append(notices, l) }}, s)
	if err != nil {
		t.Fatal(err)
	}
	if got.Server != nil {
		t.Error("there is no server in the result")
	}
	for _, c := range r.calls {
		if c == "qualify" {
			t.Error("images are not qualified without a server")
		}
	}
	if len(notices) == 0 || !strings.Contains(notices[0], "no server: no socket") {
		t.Errorf("notices = %v: the first should say there is no server", notices)
	}
	if _, err := prepare(Request{}, s); err == nil || err.Error() != "connecting to incus: no socket" {
		t.Errorf("without NoServerOK: err = %v", err)
	}
}

// A tracer is told each pass in order, with the resources before and after, and the lines the pass reported.
func TestTracerSeesEachPass(t *testing.T) {
	r := &recorder{}
	tr := &traceLog{}
	if _, err := prepare(Request{Trace: tr}, r.steps()); err != nil {
		t.Fatal(err)
	}
	want := []string{"defaults", "secrets", "network: own", "ingress rules", "derived files", "images"}
	if !reflect.DeepEqual(tr.passes, want) {
		t.Errorf("passes = %v, want %v", tr.passes, want)
	}
	if tr.before["network: own"] != "LDS" || tr.after["network: own"] != "LDSN" || tr.notices["images"] != "first notice,second notice" {
		t.Errorf("before %v, after %v, notices %v", tr.before, tr.after, tr.notices)
	}
}

type traceLog struct {
	passes                 []string
	before, after, notices map[string]string
}

func (l *traceLog) Before(pass string, rs []resolve.Resource) {
	if l.before == nil {
		l.before, l.after, l.notices = map[string]string{}, map[string]string{}, map[string]string{}
	}
	l.before[pass] = rs[0].Description
}

func (l *traceLog) After(pass string, rs []resolve.Resource, notices []string) {
	l.passes = append(l.passes, pass)
	l.after[pass] = rs[0].Description
	l.notices[pass] = strings.Join(notices, ",")
}
