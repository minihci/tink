package helper

import (
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/instant"
	"github.com/minihci/tink/internal/observe"
)

// equivalenceCases are every verdict Evaluate gave before it was split into the part that needs the instance and the part that reads only the
// status document. Each case records the health and the one line `status --check` printed, as written down from the unsplit function. The
// split must not change one of them.
func equivalenceCases() []struct {
	name   string
	f      Found
	health Health
	line   string
} {
	noTime := func(s *Status) { s.WrittenAt = instant.Time{} }
	withProblem := func(f Found) Found {
		f.StatusVolume, f.StatusProblem = "default/helper-status", "not authorized"
		return f
	}
	unreadable := found("Running", nil)
	unreadable.Config[StatusKey] = `{"proto":9}`
	unreadableStopped := found("Stopped", nil)
	unreadableStopped.Config[StatusKey] = `{"proto":9}`
	return []struct {
		name   string
		f      Found
		health Health
		line   string
	}{
		{"healthy", found("Running", fresh(nil)), Healthy, "healthy: tink-helper/helper"},
		{"quiet for one late heartbeat", found("Running", fresh(func(s *Status) { s.WrittenAt = instant.At(t0.Add(-20 * time.Minute)) })), Healthy, "healthy: tink-helper/helper"},
		{"stopped, last heard from", found("Stopped", fresh(nil)), Down, "down: tink-helper/helper: the instance is stopped (last heard from 2m ago)"},
		{"stopped, nothing published", found("Stopped", nil), Down, "down: tink-helper/helper: the instance is stopped"},
		{"stopped, document without a write time", found("Stopped", fresh(noTime)), Down, "down: tink-helper/helper: the instance is stopped"},
		{"stale", found("Running", fresh(func(s *Status) { s.WrittenAt = instant.At(t0.Add(-26 * time.Minute)) })), Down, "down: tink-helper/helper: stale: last heard from 26m ago, and it publishes at least every 10m"},
		{"stale and failing: stale says it alone", found("Running", fresh(func(s *Status) {
			s.WrittenAt = instant.At(t0.Add(-3 * time.Hour))
			s.States = []observe.Record{failingCopy("lib", "nas", 2, t0.Add(-time.Hour)), skipped("lib", "r")}
			s.Draining = true
		})), Down, "down: tink-helper/helper: stale: last heard from 3h ago, and it publishes at least every 10m"},
		{"running, nothing published", found("Running", nil), Degraded, "degraded: tink-helper/helper: it is running but has not published a status document (it has just started, or it is a helper that does not publish one)"},
		{"a document it cannot read", unreadable, Degraded, "degraded: tink-helper/helper: cannot read its status: user.tink.outcome.helper.status was written for protocol 9; this tink reads 3"},
		{"a document it cannot read, stopped", unreadableStopped, Down, "down: tink-helper/helper: cannot read its status: user.tink.outcome.helper.status was written for protocol 9; this tink reads 3; the instance is stopped"},
		{"no write time", found("Running", fresh(noTime)), Degraded, "degraded: tink-helper/helper: its status document has no write time (written-at), so it cannot be shown to be fresh"},
		{"no heartbeat", found("Running", fresh(func(s *Status) { s.Heartbeat = 0 })), Degraded, "degraded: tink-helper/helper: its status document has no heartbeat (heartbeat), so it cannot be told from a helper that has stopped"},
		{"no heartbeat and a write time that would be stale", found("Running", fresh(func(s *Status) { s.Heartbeat = 0; s.WrittenAt = instant.At(t0.Add(-5 * time.Hour)) })), Degraded, "degraded: tink-helper/helper: its status document has no heartbeat (heartbeat), so it cannot be told from a helper that has stopped"},
		{"no write time and failing", found("Running", fresh(func(s *Status) { noTime(s); s.States = []observe.Record{failingCopy("lib", "nas", 1, time.Time{})} })), Degraded, "degraded: tink-helper/helper: its status document has no write time (written-at), so it cannot be shown to be fresh; 1 copy(ies) failing: lib -> nas (1x)"},
		{"a skipped volume", found("Running", fresh(func(s *Status) { s.States = []observe.Record{skipped("tenant/lib", "policy of a newer protocol")} })), Degraded, "degraded: tink-helper/helper: 1 volume(s) skipped: tenant/lib (policy of a newer protocol)"},
		{"a failing copy", found("Running", fresh(func(s *Status) { s.States = []observe.Record{failingCopy("lib", "nas", 3, time.Time{})} })), Degraded, "degraded: tink-helper/helper: 1 copy(ies) failing: lib -> nas (3x)"},
		{"skipped, failing and draining", found("Running", fresh(func(s *Status) {
			s.States = []observe.Record{skipped("a", "x"), skipped("b", "y"), failingCopy("a", "nas", 2, time.Time{}), failingCopy("b", "off", 1, time.Time{})}
			s.Draining = true
		})), Degraded, "degraded: tink-helper/helper: 2 volume(s) skipped: a (x), b (y); 2 copy(ies) failing: a -> nas (2x), b -> off (1x); draining: it is not starting new work (an upgrade is in progress, or one did not finish: remove /data/jobs/DRAIN)"},
		{"draining", found("Running", fresh(func(s *Status) { s.Draining = true })), Degraded, "degraded: tink-helper/helper: draining: it is not starting new work (an upgrade is in progress, or one did not finish: remove /data/jobs/DRAIN)"},
		{"a status volume that cannot be read, running, document on the volume absent", withProblem(found("Running", nil)), Degraded, "degraded: tink-helper/helper: cannot read its status volume default/helper-status: not authorized"},
		{"a status volume that cannot be read, stopped", withProblem(found("Stopped", nil)), Down, "down: tink-helper/helper: cannot read its status volume default/helper-status: not authorized; the instance is stopped"},
		{"a status volume that cannot be read, and a failing copy in a document that was read", withProblem(found("Running", fresh(func(s *Status) { s.States = []observe.Record{failingCopy("lib", "nas", 1, time.Time{})} }))), Degraded, "degraded: tink-helper/helper: cannot read its status volume default/helper-status: not authorized; 1 copy(ies) failing: lib -> nas (1x)"},
	}
}

func TestSplittingEvaluateChangesNoVerdict(t *testing.T) {
	for _, tc := range equivalenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(tc.f, t0)
			if r.Health != tc.health {
				t.Errorf("health = %s, want %s", r.Health, tc.health)
			}
			if got := r.Summary(); got != tc.line {
				t.Errorf("summary:\n got %q\nwant %q", got, tc.line)
			}
		})
	}
}

// What the document alone says is what Evaluate adds to its own reasons for a running helper whose document is fresh: the pure part is not a
// second opinion, it is the same lines.
func TestJudgeStatusSaysWhatEvaluateSaysAboutTheDocument(t *testing.T) {
	for _, tc := range equivalenceCases() {
		r := Evaluate(tc.f, t0)
		if r.Status == nil || tc.f.State != "Running" || tc.f.StatusProblem != "" || r.Health == Down {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			health, reasons := JudgeStatus(*r.Status, t0)
			if health != r.Health || strings.Join(reasons, "; ") != strings.Join(r.Reasons, "; ") {
				t.Errorf("JudgeStatus = %s %q, Evaluate = %s %q", health, reasons, r.Health, r.Reasons)
			}
		})
	}
}

func reconcile(what, resource string, state string) observe.Record {
	return observe.Record{What: what, Resource: resource, Site: "home", State: state, Reason: "a fixed phrase", Since: instant.At(t0.Add(-time.Hour))}
}

func judged(t *testing.T, mut func(*Status)) Report {
	t.Helper()
	return Evaluate(found("Running", fresh(mut)), t0)
}

func TestABlockedOrFailingReconcileDegradesAndASkippedOrConvergedOneDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rec    observe.Record
		health Health
		line   string
	}{
		{"a blocked derived file", reconcile("derived", "ingress-routes", observe.Blocked), Degraded, "derived/ingress-routes (home) is blocked: a fixed phrase"},
		{"a failing derived file", reconcile("derived", "ingress-routes", observe.Failing), Degraded, "derived/ingress-routes (home) is failing: a fixed phrase"},
		{"a blocked dns reconcile", reconcile("dns", "", observe.Blocked), Degraded, "dns (home) is blocked"},
		{"a failing dns reconcile", reconcile("dns", "", observe.Failing), Degraded, "dns (home) is failing"},
		{"a skipped derived file is information: no stamped file, or the instance is not running", reconcile("derived", "ingress-routes", observe.Skipped), Healthy, ""},
		{"a converged derived file", reconcile("derived", "ingress-routes", observe.Converged), Healthy, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := judged(t, func(s *Status) { s.States = []observe.Record{tc.rec} })
			if r.Health != tc.health {
				t.Fatalf("health = %s, want %s (%v)", r.Health, tc.health, r.Reasons)
			}
			if tc.line != "" && !strings.Contains(r.Summary(), tc.line) {
				t.Errorf("summary = %q, want it to contain %q", r.Summary(), tc.line)
			}
			if tc.health == Healthy && len(r.Reasons) != 0 {
				t.Errorf("nothing to say, said %v", r.Reasons)
			}
		})
	}
}

func TestAFailedLastJobDegradesAndTheNextFinishedJobClearsIt(t *testing.T) {
	job := func(id, state string, finished time.Duration) func(*Status) {
		return func(s *Status) { s.LastJob = &LastJob{ID: id, State: state, FinishedAt: instant.At(t0.Add(-finished))} }
	}
	r := judged(t, job("j7", "failed", 5*time.Minute))
	if r.Health != Degraded || !strings.Contains(r.Summary(), "the last job (j7) failed 5m ago") {
		t.Errorf("a failed job: %s %s", r.Health, r.Summary())
	}
	// the next finished job takes its place in the document, whatever its state (the daemon keeps the newest finished one)
	for _, state := range []string{"succeeded", "cancelled"} {
		if r := judged(t, job("j8", state, time.Minute)); r.Health != Healthy || len(r.Reasons) != 0 {
			t.Errorf("a later %s job clears it: %s %v", state, r.Health, r.Reasons)
		}
	}
	// a cancelled job does not count by itself, and neither does having no job
	if r := judged(t, job("j9", "cancelled", time.Minute)); r.Health != Healthy {
		t.Errorf("cancelled: %s %v", r.Health, r.Reasons)
	}
	if r := judged(t, nil); r.Health != Healthy {
		t.Errorf("no job: %s %v", r.Health, r.Reasons)
	}
	// a document that does not say when the job finished is not aged from the year 1
	r = judged(t, func(s *Status) { s.LastJob = &LastJob{ID: "j1", State: "failed"} })
	if r.Health != Degraded || !strings.Contains(r.Summary(), "the last job (j1) failed") || strings.Contains(r.Summary(), "ago") {
		t.Errorf("no finish time: %s", r.Summary())
	}
}

// A failed job whose failure is already listed as a failing copy says it twice. That is decided (the rule is one line and the duplicate is harmless,
// since notifications come from states and not from these lines): both are said.
func TestAFailedJobIsSaidEvenWhenTheFailingCopyAlreadySaysIt(t *testing.T) {
	r := judged(t, func(s *Status) {
		s.States = []observe.Record{failingCopy("lib", "nas", 1, t0.Add(-time.Hour))}
		s.LastJob = &LastJob{ID: "j2", State: "failed", FinishedAt: instant.At(t0.Add(-time.Minute))}
	})
	if r.Health != Degraded || !strings.Contains(r.Summary(), "1 copy(ies) failing") || !strings.Contains(r.Summary(), "the last job (j2) failed") {
		t.Errorf("%s", r.Summary())
	}
}

// What is outside the document still wins: a helper that has gone quiet is down, whatever its last job did.
func TestAFailedJobDoesNotHideAStaleHelper(t *testing.T) {
	r := Evaluate(found("Running", fresh(func(s *Status) {
		s.WrittenAt = instant.At(t0.Add(-3 * time.Hour))
		s.LastJob = &LastJob{ID: "j", State: "failed"}
	})), t0)
	if r.Health != Down || !strings.Contains(r.Summary(), "stale") {
		t.Errorf("%s", r.Summary())
	}
}
