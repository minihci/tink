package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// recorder is a Publisher whose clock the test moves and whose writes it counts.
type recorder struct {
	p      *Publisher
	now    time.Time
	writes []map[string]string
	fail   error
}

func newRecorder() *recorder {
	r := &recorder{now: t0}
	r.p = &Publisher{Heartbeat: 10 * time.Minute, Now: func() time.Time { return r.now }, Patch: func(c map[string]string) error {
		if r.fail != nil {
			return r.fail
		}
		r.writes = append(r.writes, c)
		return nil
	}}
	return r
}

func TestThePublisherWritesOnChangeAndOnTheHeartbeatAndOtherwiseStaysQuiet(t *testing.T) {
	r := newRecorder()
	s := Status{Version: "v1", JobProto: 1, PolicyProto: 1}

	if wrote, err := r.p.Publish(s); !wrote || err != nil {
		t.Fatalf("the first publish always writes: %v %v", wrote, err)
	}
	// the same facts a minute later, and again: nothing to say, so no event
	for i := 0; i < 5; i++ {
		r.now = r.now.Add(time.Minute)
		if wrote, _ := r.p.Publish(s); wrote {
			t.Fatalf("minute %d: nothing changed and the heartbeat is not due, but it wrote", i+1)
		}
	}
	// a skipped volume appears: that is a change
	s.Skipped = []Skip{{Volume: "lib", Reason: "policy of a newer protocol"}}
	r.now = r.now.Add(time.Minute)
	if wrote, _ := r.p.Publish(s); !wrote {
		t.Error("a new skipped volume must be written at once")
	}
	// then quiet again until the heartbeat is due (10 minutes after the last write)
	r.now = r.now.Add(9 * time.Minute)
	if wrote, _ := r.p.Publish(s); wrote {
		t.Error("9 minutes after the last write is too soon")
	}
	r.now = r.now.Add(time.Minute)
	if wrote, _ := r.p.Publish(s); !wrote {
		t.Error("the heartbeat is due and must be written even though nothing changed")
	}
	if len(r.writes) != 3 {
		t.Errorf("3 writes in about 17 minutes, got %d", len(r.writes))
	}
}

func TestWhenTheIngressPassRanAloneIsNotAChange(t *testing.T) {
	r := newRecorder()
	s := Status{Ingress: &IngressState{OK: true, At: t0}}
	r.p.Publish(s)
	r.now = r.now.Add(time.Minute)
	s.Ingress = &IngressState{OK: true, At: r.now} // it ran again; the result is the same
	if wrote, _ := r.p.Publish(s); wrote {
		t.Error("the ingress pass running again with the same result is not worth an event")
	}
	s.Ingress = &IngressState{OK: false, At: r.now}
	if wrote, _ := r.p.Publish(s); !wrote {
		t.Error("the ingress pass starting to fail is")
	}
}

func TestThePublisherStampsTheDocumentAndMarksTheInstanceOnce(t *testing.T) {
	r := newRecorder()
	r.p.Publish(Status{JobProto: 1})
	r.now = r.now.Add(11 * time.Minute)
	r.p.Publish(Status{JobProto: 1})
	if len(r.writes) != 2 {
		t.Fatalf("writes = %d", len(r.writes))
	}
	if r.writes[0][MarkerKey] != "1" || r.writes[1][MarkerKey] != "" {
		t.Errorf("the marker rides on the first write only: %v / %v", r.writes[0], r.writes[1])
	}
	st, err := Parse(r.writes[1][StatusKey])
	if err != nil || st.Proto != StatusProto || !st.Tick.Equal(r.now.UTC()) || st.HeartbeatSeconds != 600 {
		t.Errorf("stamped: %+v %v", st, err)
	}
	if strings.Contains(r.writes[0][StatusKey], "\n") {
		t.Error("the document is one line")
	}
}

func TestAFailedWriteIsNotRememberedAsDone(t *testing.T) {
	r := newRecorder()
	r.fail = errors.New("denied")
	if wrote, err := r.p.Publish(Status{JobProto: 1}); wrote || err == nil {
		t.Fatalf("%v %v", wrote, err)
	}
	r.fail = nil
	if wrote, err := r.p.Publish(Status{JobProto: 1}); !wrote || err != nil {
		t.Errorf("the next call must try again at once, not wait for a heartbeat: %v %v", wrote, err)
	}
	// and once it has got through, it is quiet again
	r.now = r.now.Add(time.Minute)
	if wrote, _ := r.p.Publish(Status{JobProto: 1}); wrote {
		t.Error("after a successful write nothing is due until something changes or the heartbeat")
	}
	if r.writes[0][MarkerKey] != "1" {
		t.Error("and the marker still goes with the first write that succeeded")
	}
}

func TestParseRefusesWhatItCannotUnderstand(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"not json":         {"nope", "not a status document"},
		"no protocol":      {`{"version":"x"}`, "no protocol"},
		"a newer protocol": {`{"proto":2}`, "protocol 2"},
	} {
		if _, err := Parse(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a newer helper may add fields within a protocol: they are ignored, not an error
	if st, err := Parse(`{"proto":1,"version":"v9","something_new":[1,2,3]}`); err != nil || st.Version != "v9" {
		t.Errorf("%+v %v", st, err)
	}
}

func found(state string, st *Status) Found {
	f := Found{Project: "tink-helper", Name: "helper", State: state, Config: map[string]string{MarkerKey: "1"}}
	if st != nil {
		text, _ := Encode(*st)
		f.Config[StatusKey] = text
	}
	return f
}

func fresh(mut func(*Status)) *Status {
	s := &Status{Proto: 1, Tick: t0.Add(-2 * time.Minute), HeartbeatSeconds: 600}
	if mut != nil {
		mut(s)
	}
	return s
}

func TestEvaluateJudgesTheHelperFromWhatItSaidAndWhatItsInstanceIs(t *testing.T) {
	now := t0
	tests := []struct {
		name   string
		f      Found
		want   Health
		reason string
	}{
		{"healthy", found("Running", fresh(nil)), Healthy, ""},
		{"stopped, and says when it was last heard from", found("Stopped", fresh(nil)), Down, "stopped (last heard from 2m ago)"},
		{"stopped with nothing ever published", found("Stopped", nil), Down, "the instance is stopped"},
		{"quiet for a little: one late heartbeat is not an alarm", found("Running", fresh(func(s *Status) { s.Tick = now.Add(-20 * time.Minute) })), Healthy, ""},
		{"quiet for more than 2.5 heartbeats", found("Running", fresh(func(s *Status) { s.Tick = now.Add(-26 * time.Minute) })), Down, "stale: last heard from 26m ago"},
		{"running, nothing published yet", found("Running", nil), Degraded, "has not published"},
		{"a skipped volume", found("Running", fresh(func(s *Status) { s.Skipped = []Skip{{"tenant/lib", "policy of a newer protocol"}} })), Degraded, "tenant/lib (policy of a newer protocol)"},
		{"a failing copy", found("Running", fresh(func(s *Status) { s.Failing = []Failing{{Volume: "lib", Target: "nas", Count: 3}} })), Degraded, "lib -> nas (3x)"},
		{"ingress failing", found("Running", fresh(func(s *Status) { s.Ingress = &IngressState{OK: false} })), Degraded, "ingress reconcile failed"},
		{"ingress fine", found("Running", fresh(func(s *Status) { s.Ingress = &IngressState{OK: true} })), Healthy, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(tc.f, now)
			if r.Health != tc.want {
				t.Fatalf("health = %s, want %s (%v)", r.Health, tc.want, r.Reasons)
			}
			if tc.reason != "" && !strings.Contains(r.Summary(), tc.reason) {
				t.Errorf("summary = %q, want it to contain %q", r.Summary(), tc.reason)
			}
		})
	}
}

func TestEvaluateOnADocumentItCannotReadIsDegradedNotDown(t *testing.T) {
	f := found("Running", nil)
	f.Config[StatusKey] = `{"proto":9}`
	r := Evaluate(f, t0)
	if r.Health != Degraded || !strings.Contains(r.Summary(), "cannot read its status") || strings.Contains(r.Summary(), "has not published") {
		t.Errorf("an unreadable document is its own message, not 'nothing published': %s", r.Summary())
	}
}

func TestHealthIsOrderedSoAMonitorCanActOnTheWorst(t *testing.T) {
	if !(Healthy < Degraded && Degraded < Down) || int(Healthy) != 0 || int(Degraded) != 1 || int(Down) != 2 {
		t.Error("these are the exit codes of `status --check`: 0, 1, 2")
	}
}

type listServer struct {
	incus.InstanceServer
	all []api.Instance
	err error
}

func (s listServer) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return s.all, s.err
}

func TestFindReturnsOnlyMarkedInstancesInEveryProject(t *testing.T) {
	mk := func(project, name string, cfg map[string]string) api.Instance {
		return api.Instance{Name: name, Project: project, Status: "Running", InstancePut: api.InstancePut{Config: cfg}}
	}
	got, err := Find(listServer{all: []api.Instance{
		mk("default", "caddy", map[string]string{"user.ingress.enabled": "true"}),
		mk("tink-helper", "helper", map[string]string{MarkerKey: "1"}),
		mk("other", "also", map[string]string{MarkerKey: "1"}),
	}})
	if err != nil || len(got) != 2 || got[0].Label() != "other/also" || got[1].Label() != "tink-helper/helper" {
		t.Errorf("%v %v", got, err)
	}
	if _, err := Find(listServer{err: errors.New("boom")}); err == nil {
		t.Error("a failed listing is an error, not 'no helper'")
	}
}
