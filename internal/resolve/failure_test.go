package resolve

import (
	"strings"
	"testing"
	"time"
)

var t10 = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

func failCfg(target string, at time.Time, n string, extra map[string]string) map[string]string {
	c := map[string]string{CopyFailAt(target): at.UTC().Format(time.RFC3339)}
	if n != "" {
		c[CopyFailCount(target)] = n
	}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func TestRetryAfterDoublesUpToTheInterval(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		n        int
		interval time.Duration
		want     time.Duration
	}{
		{1, day, 5 * time.Minute},
		{2, day, 10 * time.Minute},
		{3, day, 20 * time.Minute},
		{4, day, 40 * time.Minute},
		{5, day, 80 * time.Minute},
		{9, day, 21*time.Hour + 20*time.Minute}, // 5m * 2^8
		{10, day, day},                          // 5m * 2^9 = 42h, capped at the interval
		{1000, day, day},
		{1, time.Minute, time.Minute}, // an every-minute schedule: the delay is capped at its interval, never longer
		{3, time.Hour, 20 * time.Minute},
		{6, time.Hour, time.Hour}, // 5m * 2^5 = 160m, capped at the hourly interval
		{0, day, 5 * time.Minute}, // a count that is not a count behaves as the first failure
	} {
		got := RetryAfter(CopyFailure{At: t10, N: tc.n}, tc.interval).Sub(t10)
		if got != tc.want {
			t.Errorf("n=%d interval=%s: retry after %s, want %s", tc.n, tc.interval, got, tc.want)
		}
	}
}

func TestFailureOf(t *testing.T) {
	last := t10.Add(-time.Hour)
	for name, tc := range map[string]struct {
		cfg     map[string]string
		failing bool
		n       int
	}{
		"no stamp":                      {map[string]string{}, false, 0},
		"an unreadable time":            {map[string]string{CopyFailAt("t"): "yesterday"}, false, 0},
		"time and count":                {failCfg("t", t10, "3", nil), true, 3},
		"no count means one":            {failCfg("t", t10, "", nil), true, 1},
		"a nonsense count means one":    {failCfg("t", t10, "many", nil), true, 1},
		"a zero count means one":        {failCfg("t", t10, "0", nil), true, 1},
		"newer than the last success":   {failCfg("t", t10, "2", map[string]string{CopyStampAt("t"): last.Format(time.RFC3339)}), true, 2},
		"older than the last success":   {failCfg("t", last.Add(-time.Hour), "2", map[string]string{CopyStampAt("t"): last.Format(time.RFC3339)}), false, 0},
		"the same instant as a success": {failCfg("t", last, "2", map[string]string{CopyStampAt("t"): last.Format(time.RFC3339)}), false, 0},
		"another target's failure":      {failCfg("other", t10, "4", nil), false, 0},
	} {
		got, ok := FailureOf(tc.cfg, "t")
		if ok != tc.failing || (ok && got.N != tc.n) {
			t.Errorf("%s: failing=%v n=%d, want failing=%v n=%d", name, ok, got.N, tc.failing, tc.n)
		}
	}
}

func TestCopyDueBacksOffAfterFailures(t *testing.T) {
	const daily = "0 3 * * *"
	lastOK := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC) // the 03:00 run yesterday: today's 03:00 is due by 10:00

	for name, tc := range map[string]struct {
		schedule string
		cfg      map[string]string
		now      time.Time
		due      bool
		reason   string
	}{
		"never run, never failed: due":                 {daily, map[string]string{}, t10, true, ""},
		"never run, failed 2 minutes ago: backing off": {daily, failCfg("t", t10.Add(-2*time.Minute), "1", nil), t10, false, "backing off after 1 failed"},
		"never run, failed 6 minutes ago: retry":       {daily, failCfg("t", t10.Add(-6*time.Minute), "1", nil), t10, true, ""},
		"due by schedule, failed twice 10 minutes ago: wait for 10m then due": {daily,
			failCfg("t", t10.Add(-10*time.Minute), "2", map[string]string{CopyStampAt("t"): lastOK.Format(time.RFC3339)}), t10, true, ""},
		"due by schedule, failed twice 9 minutes ago: still backing off": {daily,
			failCfg("t", t10.Add(-9*time.Minute), "2", map[string]string{CopyStampAt("t"): lastOK.Format(time.RFC3339)}), t10, false, "backing off after 2 failed"},
		"not due by schedule: the schedule is the reason, not the failure": {daily,
			failCfg("t", t10.Add(-time.Hour), "7", map[string]string{CopyStampAt("t"): t10.Add(-2 * time.Hour).Format(time.RFC3339)}), t10, false, "not yet due"},
		"a stale failure older than a success is ignored": {daily,
			failCfg("t", lastOK.Add(-time.Hour), "9", map[string]string{CopyStampAt("t"): lastOK.Format(time.RFC3339)}), t10, true, ""},
		"many failures are capped at the schedule's interval: hourly, 10 failures, 30 min after": {"@hourly",
			failCfg("t", t10.Add(-30*time.Minute), "10", map[string]string{CopyStampAt("t"): t10.Add(-3 * time.Hour).Format(time.RFC3339)}), t10, false, "backing off"},
		"...and an hour after it is due again": {"@hourly",
			failCfg("t", t10.Add(-time.Hour), "10", map[string]string{CopyStampAt("t"): t10.Add(-3 * time.Hour).Format(time.RFC3339)}), t10, true, ""},
	} {
		d, err := CopyDue(tc.schedule, tc.cfg, "t", tc.now)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if d.Due != tc.due || (tc.reason != "" && !strings.Contains(d.Reason, tc.reason)) || (tc.due && d.Reason != "") {
			t.Errorf("%s: due=%v reason=%q; want due=%v reason containing %q", name, d.Due, d.Reason, tc.due, tc.reason)
		}
		if got, _ := CopyIsDue(tc.schedule, tc.cfg, "t", tc.now); got != tc.due {
			t.Errorf("%s: CopyIsDue disagrees with CopyDue", name)
		}
	}
	if d, _ := CopyDue(daily, failCfg("t", t10.Add(-time.Minute), "1", nil), "t", t10); d.Failure == nil || !d.RetryAt.Equal(t10.Add(-time.Minute).Add(FirstRetryDelay)) {
		t.Errorf("the decision must say when the retry is: %+v", d)
	}
}

func TestCopyWarningsAboutFailures(t *testing.T) {
	r := Resource{Name: "lib", Backup: &VolumeBackup{Copies: []BackupCopy{{Target: "t", Schedule: "0 3 * * *", Retain: "30d"}}}}
	lastOK := time.Date(2026, 10, 7, 3, 0, 0, 0, time.UTC)

	// never succeeded, and failing: say so, and not the plain "never run"
	w := copyWarnings(r, failCfg("t", t10.Add(-20*time.Minute), "3", nil), t10)
	if len(w) != 1 || !strings.Contains(w[0], "never succeeded") || !strings.Contains(w[0], "3 attempt(s)") || strings.Contains(w[0], "never run") {
		t.Errorf("never succeeded and failing: %q", w)
	}
	// succeeded earlier, failing now: failing, and not overdue yet (the next 03:00 is tomorrow)
	w = copyWarnings(r, failCfg("t", t10.Add(-5*time.Minute), "1", map[string]string{CopyStampAt("t"): lastOK.Format(time.RFC3339)}), t10)
	if len(w) != 1 || !strings.Contains(w[0], "is failing") || !strings.Contains(w[0], "1 attempt(s)") {
		t.Errorf("failing: %q", w)
	}
	// failing AND overdue: both are said
	longAgo := time.Date(2026, 10, 4, 3, 0, 0, 0, time.UTC)
	w = copyWarnings(r, failCfg("t", t10.Add(-5*time.Minute), "30", map[string]string{CopyStampAt("t"): longAgo.Format(time.RFC3339)}), t10)
	if len(w) != 2 || !strings.Contains(w[0], "is failing") || !strings.Contains(w[1], "overdue") {
		t.Errorf("failing and overdue: %q", w)
	}
	// healthy: nothing
	if w := copyWarnings(r, map[string]string{CopyStampAt("t"): lastOK.Format(time.RFC3339)}, t10); len(w) != 0 {
		t.Errorf("a healthy copy must not warn: %q", w)
	}
	// the failure text never reaches a warning, because it is never stored: only a time and a count are
	for _, k := range []string{CopyFailAt("t"), CopyFailCount("t")} {
		if !strings.HasPrefix(k, "user.tink.backup.copy.t.fail.") {
			t.Errorf("unexpected failure key %q", k)
		}
	}
}

// The reasons name their times in one zone, with its name, whichever way they were computed.
func TestCopyDueReasonsUseOneZoneAndNameIt(t *testing.T) {
	mdt := time.FixedZone("MDT", -6*3600)
	now := time.Date(2026, 10, 7, 0, 50, 0, 0, mdt) // 06:50 UTC
	// backing off: failed at 06:46 UTC, retry after 06:51 UTC = 00:51 MDT
	d, _ := CopyDue("@hourly", failCfg("t", time.Date(2026, 10, 7, 6, 46, 0, 0, time.UTC), "1", nil), "t", now)
	if !strings.Contains(d.Reason, "00:51 MDT") {
		t.Errorf("backoff reason = %q, want the retry time in MDT", d.Reason)
	}
	// not yet due: last success 06:46 UTC (00:46 MDT), next @hourly is 01:00 MDT
	d, _ = CopyDue("@hourly", map[string]string{CopyStampAt("t"): "2026-10-07T06:46:00Z"}, "t", now)
	if !strings.Contains(d.Reason, "01:00 MDT") {
		t.Errorf("schedule reason = %q, want the next run in MDT", d.Reason)
	}
}
