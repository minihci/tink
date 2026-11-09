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
