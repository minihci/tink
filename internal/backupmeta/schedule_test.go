package backupmeta

import (
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextRun(t *testing.T) {
	tests := []struct {
		schedule, after, want string
	}{
		{"0 3 * * *", "2026-10-06 12:00", "2026-10-07 03:00"},
		{"0 3 * * *", "2026-10-06 02:59", "2026-10-06 03:00"},
		{"0 3 * * *", "2026-10-06 03:00", "2026-10-07 03:00"}, // strictly after
		{"*/15 * * * *", "2026-10-06 12:07", "2026-10-06 12:15"},
		{"@daily", "2026-10-06 12:00", "2026-10-07 00:00"},
		{"@hourly", "2026-10-06 12:30", "2026-10-06 13:00"},
		{"@weekly", "2026-10-06 12:00", "2026-10-11 00:00"},        // the next Sunday
		{"@daily,@hourly", "2026-10-06 12:30", "2026-10-06 13:00"}, // the earliest of a list
		{" 0 3 * * * ", "2026-10-06 12:00", "2026-10-07 03:00"},
	}
	for _, tc := range tests {
		got, err := NextRun(tc.schedule, at(tc.after))
		if err != nil || !got.Equal(at(tc.want)) {
			t.Errorf("NextRun(%q, %s) = %v, %v; want %s", tc.schedule, tc.after, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "daily", "0 3 * *", "@sometimes", "99 99 * * *"} {
		if _, err := NextRun(bad, at("2026-10-06 12:00")); err == nil {
			t.Errorf("NextRun(%q) must reject it", bad)
		}
	}
}

func TestExpiryAfter(t *testing.T) {
	from := at("2026-01-31 10:00")
	tests := []struct{ expr, want string }{
		{"14d", "2026-02-14 10:00"},
		{"1w 3d", "2026-02-10 10:00"},
		{"1y", "2027-01-31 10:00"},
		{"12H", "2026-01-31 22:00"},
		{"90M", "2026-01-31 11:30"},
	}
	for _, tc := range tests {
		got, err := ExpiryAfter(from, tc.expr)
		if err != nil || !got.Equal(at(tc.want)) {
			t.Errorf("ExpiryAfter(%q) = %v, %v; want %s", tc.expr, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "forever", "0d", "1d 2d"} {
		if _, err := ExpiryAfter(from, bad); err == nil {
			t.Errorf("ExpiryAfter(%q) must reject it", bad)
		}
	}
}

func TestCopyIsDue(t *testing.T) {
	cfg := func(last string) map[string]string {
		if last == "" {
			return nil
		}
		return map[string]string{CopyStampAt("nas"): at(last).Format(time.RFC3339)}
	}
	tests := []struct {
		name, last, now string
		want            bool
	}{
		{"never ran", "", "2026-10-06 12:00", true},
		{"ran today after the daily slot", "2026-10-06 04:05", "2026-10-06 23:00", false},
		{"the next slot has passed", "2026-10-06 04:05", "2026-10-07 04:01", true},
		{"exactly the next slot", "2026-10-06 04:05", "2026-10-07 04:00", true},
		{"just before the next slot", "2026-10-06 04:05", "2026-10-07 03:59", false},
	}
	for _, tc := range tests {
		got, err := CopyIsDue("0 4 * * *", cfg(tc.last), "nas", at(tc.now))
		if err != nil || got != tc.want {
			t.Errorf("%s: CopyIsDue = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	// a stamp tink did not write is as good as none
	if due, _ := CopyIsDue("0 4 * * *", map[string]string{CopyStampAt("nas"): "yesterday-ish"}, "nas", at("2026-10-06 12:00")); !due {
		t.Error("an unreadable stamp must not suppress a copy")
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
