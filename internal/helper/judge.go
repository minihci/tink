package helper

import (
	"fmt"
	"time"

	"github.com/minihci/tink/internal/jobs"
	"github.com/minihci/tink/internal/observe"
)

// JudgeStatus judges what a status document says, and nothing else: it is a function of the document and the time, with no connection to Incus, so
// whatever holds the document can run it. What needs the server (is the instance running, could the status volume be read, has the document gone
// quiet for longer than its heartbeat, is the helper's certificate still trusted) is Evaluate's and CheckTrust's.
//
// A document that does not say when it was written, or how often, cannot be shown to be fresh, and must not read as healthy: that would hide a
// helper that has stopped, whether the document came from another version, was edited by hand, or is damaged. There is no version logic here: the
// rule is about what the document says, not about who wrote it. Then the volumes it will not copy, the copies that are failing, a derived file or the
// dns records that are blocked or failing, a last job that failed, and a drain that was never ended.
//
// Two things the document carries are deliberately not a problem. A reconcile state of skipped means there is nothing to keep (no stamped file) or the
// instance it is on is not running, which is noticed on its own, so it stays information. And a last job that was cancelled was stopped on purpose.
// A failed last job is always a problem, even when the copy it was about is also listed as failing: one simple rule, and the next finished job (any
// state) replaces it in the document, which is what clears it.
//
// It returns the health those lines add up to, and the lines.
func JudgeStatus(st Status, now time.Time) (Health, []string) {
	var health Health
	var reasons []string
	degrade := func(format string, args ...any) {
		health = Degraded
		reasons = append(reasons, fmt.Sprintf(format, args...))
	}

	switch hb := st.Heartbeat.D(); {
	case st.WrittenAt.IsZero():
		degrade("its status document has no write time (%s), so it cannot be shown to be fresh", WrittenAtField)
	case hb <= 0:
		degrade("its status document has no heartbeat (%s), so it cannot be told from a helper that has stopped", HeartbeatField)
	}
	if skipped := st.Backup(observe.Skipped); len(skipped) > 0 {
		degrade("%d volume(s) skipped: %s", len(skipped), skipList(skipped))
	}
	if failing := st.Backup(observe.Failing); len(failing) > 0 {
		degrade("%d copy(ies) failing: %s", len(failing), failList(failing))
	}
	for _, rec := range st.States {
		if rec.What == observe.WhatBackup {
			continue
		}
		switch rec.State {
		case observe.Blocked, observe.Failing:
			degrade("%s", reconcileLine(rec))
		}
	}
	if j := st.LastJob; j != nil && j.State == string(jobs.Failed) {
		degrade("the last job (%s) failed%s", j.ID, ago(j.FinishedAt.Time, now))
	}
	if st.Draining {
		degrade("draining: it is not starting new work (an upgrade is in progress, or one did not finish: remove /data/jobs/DRAIN)")
	}
	return health, reasons
}

// reconcileLine says one reconcile state that is blocked or failing: what it keeps, for which site, and the fixed reason the daemon gave.
func reconcileLine(r observe.Record) string {
	line := fmt.Sprintf("%s (%s) is %s", r.Name(), observe.SiteName(r.Site), r.State)
	if r.Reason != "" {
		line += ": " + r.Reason
	}
	return line
}

// ago is " 5m ago" for a time that was recorded, and nothing for one that was not (a document that does not say must not be aged from the year 1).
func ago(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	return " " + age(now.Sub(t)) + " ago"
}
