package helper

import (
	"fmt"
	"time"

	"github.com/minihci/tink/internal/observe"
)

// JudgeStatus judges what a status document says, and nothing else: it is a function of the document and the time, with no connection to Incus, so
// whatever holds the document can run it. What needs the server (is the instance running, could the status volume be read, has the document gone
// quiet for longer than its heartbeat, is the helper's certificate still trusted) is Evaluate's and CheckTrust's.
//
// A document that does not say when it was written, or how often, cannot be shown to be fresh, and must not read as healthy: that would hide a
// helper that has stopped, whether the document came from another version, was edited by hand, or is damaged. There is no version logic here: the
// rule is about what the document says, not about who wrote it. Then the volumes it will not copy, the copies that are failing, and a drain that was
// never ended.
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
	if st.Draining {
		degrade("draining: it is not starting new work (an upgrade is in progress, or one did not finish: remove /data/jobs/DRAIN)")
	}
	return health, reasons
}
