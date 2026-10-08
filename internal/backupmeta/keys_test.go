package backupmeta

import (
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

func TestCopyOf(t *testing.T) {
	if got := CopyOf("", "", "lib"); got != "default/default/lib" {
		t.Errorf("CopyOf = %q", got)
	}
	if got := CopyOf("immich", "fast", "lib"); got != "immich/fast/lib" {
		t.Errorf("CopyOf = %q", got)
	}
}
