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
		secrets: func(rs []resolve.Resource, req Request) ([]resolve.Resource, error) {
			note("secrets", rs)
			mark(rs, "S")
			return rs, nil
		},
		connect: func(socket string) (incus.InstanceServer, error) {
			r.calls = append(r.calls, "connect")
			return nil, nil
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
	if want := []string{"load", "secrets", "connect", "qualify", "levels"}; !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("passes ran as %v, want %v", r.calls, want)
	}
	for pass, want := range map[string]string{"secrets": "L", "qualify": "LS", "levels": "LSQ"} {
		if seen := r.seen[pass]; len(seen) != 1 || seen[0] != want {
			t.Errorf("%s was handed %v, want a resource marked %q", pass, seen, want)
		}
	}
	if len(got.Levels) != 1 || got.Levels[0][0].Description != "LSQ" || got.Resources[0].Description != "LSQ" {
		t.Errorf("result = %+v", got)
	}
	if want := []string{"first notice", "second notice"}; !reflect.DeepEqual(notices, want) {
		t.Errorf("notices = %v, want %v", notices, want)
	}
}

// Each failure stops the passes after it, and the ones before it have run.
func TestAFailingPassStopsTheRest(t *testing.T) {
	boom := errors.New("boom")
	order := []string{"load", "secrets", "connect", "qualify", "levels"}
	for i, pass := range order {
		r := &recorder{}
		s := r.steps()
		fail := func() { r.calls = append(r.calls, "FAIL") }
		switch pass {
		case "load":
			s.load = func([]string) ([]resolve.Resource, error) { fail(); return nil, boom }
		case "secrets":
			s.secrets = func([]resolve.Resource, Request) ([]resolve.Resource, error) { fail(); return nil, boom }
		case "connect":
			s.connect = func(string) (incus.InstanceServer, error) { fail(); return nil, boom }
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
	if len(notices) != 2 {
		t.Errorf("notices = %v, want both", notices)
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
