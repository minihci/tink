package resolve

import (
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
)

var t10 = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

func failCfg(target string, at time.Time, n string, extra map[string]string) map[string]string {
	c := map[string]string{backupmeta.CopyFailAt(target): at.UTC().Format(time.RFC3339)}
	if n != "" {
		c[backupmeta.CopyFailCount(target)] = n
	}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func TestCopyWarningsAboutFailures(t *testing.T) {
	r := Resource{Name: "lib", Backup: &backupmeta.VolumeBackup{Copies: []backupmeta.BackupCopy{{Target: "t", Schedule: "0 3 * * *", Retain: "30d"}}}}
	lastOK := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)

	// never succeeded, and failing: say so, and not the plain "never run"
	w := copyWarnings(r, failCfg("t", t10.Add(-20*time.Minute), "3", nil), t10)
	if len(w) != 1 || !strings.Contains(w[0], "never succeeded") || !strings.Contains(w[0], "3 attempt(s)") || strings.Contains(w[0], "never run") {
		t.Errorf("never succeeded and failing: %q", w)
	}
	// succeeded earlier, failing now: failing, and not overdue yet (the next 03:00 is tomorrow)
	w = copyWarnings(r, failCfg("t", t10.Add(-5*time.Minute), "1", map[string]string{backupmeta.CopyStampAt("t"): lastOK.Format(time.RFC3339)}), t10)
	if len(w) != 1 || !strings.Contains(w[0], "is failing") || !strings.Contains(w[0], "1 attempt(s)") {
		t.Errorf("failing: %q", w)
	}
	// failing AND overdue: both are said
	longAgo := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	w = copyWarnings(r, failCfg("t", t10.Add(-5*time.Minute), "30", map[string]string{backupmeta.CopyStampAt("t"): longAgo.Format(time.RFC3339)}), t10)
	if len(w) != 2 || !strings.Contains(w[0], "is failing") || !strings.Contains(w[1], "overdue") {
		t.Errorf("failing and overdue: %q", w)
	}
	// healthy: nothing
	if w := copyWarnings(r, map[string]string{backupmeta.CopyStampAt("t"): lastOK.Format(time.RFC3339)}, t10); len(w) != 0 {
		t.Errorf("a healthy copy must not warn: %q", w)
	}
	// the failure text never reaches a warning, because it is never stored: only a time and a count are
	for _, k := range []string{backupmeta.CopyFailAt("t"), backupmeta.CopyFailCount("t")} {
		if !strings.HasPrefix(k, "user.tink.backup.copy.t.fail.") {
			t.Errorf("unexpected failure key %q", k)
		}
	}
}

// The reasons name their times in one zone, with its name, whichever way they were computed.
