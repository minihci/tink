package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/instant"
	"github.com/minihci/tink/internal/observe"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// skipped is the record of a volume the helper will not copy, and failingCopy that of a copy that is failing, as the daemon writes them.
func skipped(volume, reason string) observe.Record {
	return observe.Record{What: observe.WhatBackup, Resource: volume, State: observe.Skipped, Reason: reason, Since: instant.At(t0.Add(-time.Hour))}
}

func failingCopy(volume, target string, failures int, since time.Time) observe.Record {
	return observe.Record{What: observe.WhatBackup, Resource: volume, Target: target, State: observe.Failing, Reason: "the copy has failed since its last success", Failures: failures, Since: instant.At(since)}
}

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
	s.States = []observe.Record{skipped("lib", "policy of a newer protocol")}
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
	if err != nil || st.Proto != StatusProto || !st.WrittenAt.Equal(r.now.UTC()) || st.Heartbeat.D() != 10*time.Minute {
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
		"a newer protocol": {`{"proto":4}`, "protocol 4"},
	} {
		if _, err := Parse(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a newer helper may add fields within a protocol: they are ignored, not an error
	if st, err := Parse(`{"proto":3,"version":"v9","something_new":[1,2,3]}`); err != nil || st.Version != "v9" {
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
	s := &Status{Proto: 1, WrittenAt: instant.At(t0.Add(-2 * time.Minute)), Heartbeat: instant.Duration(10 * time.Minute)}
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
		{"quiet for a little: one late heartbeat is not an alarm", found("Running", fresh(func(s *Status) { s.WrittenAt = instant.At(now.Add(-20 * time.Minute)) })), Healthy, ""},
		{"quiet for more than 2.5 heartbeats", found("Running", fresh(func(s *Status) { s.WrittenAt = instant.At(now.Add(-26 * time.Minute)) })), Down, "stale: last heard from 26m ago"},
		{"running, nothing published yet", found("Running", nil), Degraded, "has not published"},
		{"a skipped volume", found("Running", fresh(func(s *Status) { s.States = []observe.Record{skipped("tenant/lib", "policy of a newer protocol")} })), Degraded, "tenant/lib is skipped: policy of a newer protocol"},
		{"a failing copy", found("Running", fresh(func(s *Status) { s.States = []observe.Record{failingCopy("lib", "nas", 3, time.Time{})} })), Degraded, "the copy of lib to nas is failing, 3 in a row"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(tc.f, now)
			if r.Health != tc.want {
				t.Fatalf("health = %s, want %s (%v)", r.Health, tc.want, r.Details())
			}
			if tc.reason != "" && !strings.Contains(r.Summary(), tc.reason) {
				t.Errorf("summary = %q, want it to contain %q", r.Summary(), tc.reason)
			}
		})
	}
}

// A document that does not say when it was written cannot be shown to be fresh, so it is never healthy: whoever wrote it (another version,
// a hand edit, damage), a helper that has stopped would otherwise be read as well for ever.
func TestADocumentWithNoWriteTimeIsNeverHealthy(t *testing.T) {
	noTime := func(s *Status) { s.WrittenAt = instant.Time{} }
	noBeat := func(s *Status) { s.Heartbeat = 0 }
	for name, tc := range map[string]struct {
		f      Found
		want   Health
		reason string
	}{
		"no write time":              {found("Running", fresh(noTime)), Degraded, "no write time (written-at)"},
		"no write time and failing":  {found("Running", fresh(func(s *Status) { noTime(s); s.States = []observe.Record{failingCopy("lib", "nas", 1, time.Time{})} })), Degraded, "the copy of lib to nas is failing"},
		"no heartbeat":               {found("Running", fresh(noBeat)), Degraded, "no heartbeat"},
		"no write time, and stopped": {found("Stopped", fresh(noTime)), Down, "the instance is stopped"},
	} {
		r := Evaluate(tc.f, t0)
		if r.Health != tc.want || !strings.Contains(r.Summary(), tc.reason) {
			t.Errorf("%s: %s, want %s with %q", name, r.Summary(), tc.want, tc.reason)
		}
	}
	// the same through the text a person could have edited: the write time cut out of a document that was fine
	text, _ := Encode(*fresh(nil))
	cut := strings.Replace(text, `"written-at":"2026-10-07T11:58:00Z",`, "", 1)
	if cut == text {
		t.Fatalf("the test's document has no write time to remove: %s", text)
	}
	f := found("Running", nil)
	f.Config[StatusKey] = cut
	if r := Evaluate(f, t0); r.Health == Healthy || !strings.Contains(r.Summary(), "no write time") {
		t.Errorf("a hand-edited document: %s", r.Summary())
	}
	// and a document of the old protocol, which called it "tick", is judged by the same rule and nothing else
	f.Config[StatusKey] = `{"proto":1,"tick":"2026-10-07T11:58:00Z","heartbeat_seconds":600}`
	if r := Evaluate(f, t0); r.Health != Degraded || !strings.Contains(r.Summary(), "no write time") {
		t.Errorf("an older document: %s", r.Summary())
	}
}

func TestTheDocumentNamesItsInstantsAndItsHeartbeatTheGlossaryWay(t *testing.T) {
	st := Status{Proto: StatusProto, StartedAt: instant.At(t0.Add(-time.Hour)), WrittenAt: instant.At(t0), Heartbeat: instant.Duration(10 * time.Minute),
		States:  []observe.Record{{What: observe.WhatBackup, Resource: "lib", Target: "nas", State: observe.Failing, Reason: "r", Failures: 3, Since: instant.At(t0.Add(-30 * time.Minute)), FailedAt: instant.At(t0.Add(-time.Minute))}},
		LastJob: &LastJob{ID: "j", State: "failed", FinishedAt: instant.At(t0.Add(-5 * time.Minute))}}
	text, err := Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"started-at":"2026-10-07T11:00:00Z"`, `"written-at":"2026-10-07T12:00:00Z"`, `"heartbeat":"10m0s"`,
		`"since":"2026-10-07T11:30:00Z"`, `"failed-at":"2026-10-07T11:59:00Z"`, `"finished-at":"2026-10-07T11:55:00Z"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the document lacks %s: %s", want, text)
		}
	}
	for _, gone := range []string{`"tick"`, `"started"`, `heartbeat_seconds`, `"finished"`} {
		if strings.Contains(text, gone) {
			t.Errorf("the document still says %s: %s", gone, text)
		}
	}
	back, err := Parse(text)
	if err != nil || !back.WrittenAt.Equal(t0) || back.Heartbeat.D() != 10*time.Minute || !back.States[0].Since.Equal(t0.Add(-30*time.Minute)) {
		t.Errorf("round trip: %+v %v", back, err)
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
		mk("default", "caddy", map[string]string{"limits.cpu": "1"}),
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

func TestADrainingHelperIsDegradedAndSaysHowToEndIt(t *testing.T) {
	r := Evaluate(found("Running", fresh(func(s *Status) { s.Draining = true })), t0)
	if r.Health != Degraded || !strings.Contains(r.Summary(), "draining") || !strings.Contains(r.Summary(), "DRAIN") {
		t.Errorf("a drain that was left behind must not look healthy: %s", r.Summary())
	}
}

// The states of what the daemon keeps (a derived file that is blocked, say) are judged like the volumes' copies: a blocked or failing one makes the helper
// degraded. This replaced a test that said they were not judged yet; it is changed on purpose, by the health work.
func TestHealthJudgesTheReconcileStatesAsWellAsTheBackupStates(t *testing.T) {
	reconcileOnly := fresh(func(s *Status) {
		s.States = []observe.Record{{What: "derived", Resource: "ingress-routes", Site: "home", State: observe.Blocked, Reason: "the file is not there", Since: instant.At(t0.Add(-time.Hour))}}
	})
	if r := Evaluate(found("Running", reconcileOnly), t0); r.Health != Degraded || !strings.Contains(r.Summary(), "derived/ingress-routes (home) is blocked: the file is not there") {
		t.Errorf("a blocked derived file: %s %v", r.Health, r.Details())
	}
	both := fresh(func(s *Status) {
		s.States = []observe.Record{
			{What: "derived", Resource: "ingress-routes", State: observe.Failing, Reason: "r"},
			skipped("lib", "policy of a newer protocol"), failingCopy("lib", "nas", 2, t0.Add(-time.Hour)),
		}
	})
	r := Evaluate(found("Running", both), t0)
	if r.Health != Degraded || !strings.Contains(r.Summary(), "lib is skipped: policy of a newer protocol") || !strings.Contains(r.Summary(), "the copy of lib to nas is failing, 2 in a row") ||
		!strings.Contains(r.Summary(), "derived/ingress-routes (the unnamed site) is failing: r") {
		t.Errorf("%s: %s", r.Health, r.Summary())
	}
}

// A new event is a change worth a write, like a new state; the same list again is not.
func TestAnEventIsAChangeTheHeartbeatDoesNotHaveToWaitFor(t *testing.T) {
	r := newRecorder()
	s := Status{JobProto: 1}
	if wrote, _ := r.p.Publish(s); !wrote {
		t.Fatal("the first publish always writes")
	}
	r.now = r.now.Add(time.Minute)
	s.Events = []observe.Event{{Event: observe.Healed, What: "derived", Resource: "ingress-routes", At: instant.At(r.now)}}
	if wrote, _ := r.p.Publish(s); !wrote {
		t.Error("a healed event must be written at once")
	}
	r.now = r.now.Add(time.Minute)
	if wrote, _ := r.p.Publish(s); wrote {
		t.Error("the same list again is nothing to say")
	}
}

func TestLoadReadsTheStoredDocumentAndSaysSoWhenItCannotBeRead(t *testing.T) {
	text, err := Encode(Status{Proto: StatusProto, States: []observe.Record{skipped("lib", "x")}, Events: []observe.Event{{Event: observe.Restarted, What: "derived"}}})
	if err != nil {
		t.Fatal(err)
	}
	read := func(s string, err error) *Publisher {
		return &Publisher{Read: func() (string, error) { return s, err }}
	}
	if st, err := read(text, nil).Load(); err != nil || len(st.States) != 1 || len(st.Events) != 1 {
		t.Errorf("a stored document: %+v %v", st, err)
	}
	if st, err := read("", nil).Load(); err != nil || len(st.States) != 0 {
		t.Errorf("nothing stored: %+v %v", st, err)
	}
	if st, err := (&Publisher{}).Load(); err != nil || len(st.States) != 0 {
		t.Errorf("no place to read: %+v %v", st, err)
	}
	for name, p := range map[string]*Publisher{
		"not json":         read("nope", nil),
		"a newer protocol": read(`{"proto":99}`, nil),
		"a failed read":    read("", errors.New("the volume is not there")),
	} {
		if _, err := p.Load(); err == nil {
			t.Errorf("%s: Load did not say it could not read it", name)
		}
	}
}
