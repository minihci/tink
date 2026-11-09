package backupmeta

import (
	"reflect"
	"testing"
	"time"

	"github.com/minihci/tink/internal/instant"
)

var t10 = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

func failCfg(target string, at time.Time, n string, extra map[string]string) map[string]string {
	c := map[string]string{FailedAt(target): at.UTC().Format(time.RFC3339)}
	if n != "" {
		c[Failures(target)] = n
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
		got := RetryAfter(CopyFailure{FailedAt: t10, N: tc.n}, tc.interval).Sub(t10)
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
		"an unreadable time":            {map[string]string{FailedAt("t"): "yesterday"}, false, 0},
		"time and count":                {failCfg("t", t10, "3", nil), true, 3},
		"no count means one":            {failCfg("t", t10, "", nil), true, 1},
		"a nonsense count means one":    {failCfg("t", t10, "many", nil), true, 1},
		"a zero count means one":        {failCfg("t", t10, "0", nil), true, 1},
		"newer than the last success":   {failCfg("t", t10, "2", map[string]string{CopiedAt("t"): last.Format(time.RFC3339)}), true, 2},
		"older than the last success":   {failCfg("t", last.Add(-time.Hour), "2", map[string]string{CopiedAt("t"): last.Format(time.RFC3339)}), false, 0},
		"the same instant as a success": {failCfg("t", last, "2", map[string]string{CopiedAt("t"): last.Format(time.RFC3339)}), false, 0},
		"another target's failure":      {failCfg("other", t10, "4", nil), false, 0},
	} {
		got, ok := FailureOf(tc.cfg, "t")
		if ok != tc.failing || (ok && got.N != tc.n) {
			t.Errorf("%s: failing=%v n=%d, want failing=%v n=%d", name, ok, got.N, tc.failing, tc.n)
		}
	}
}

func TestFailureOfReadsTheOnsetSeparatelyFromTheLastAttempt(t *testing.T) {
	onset, lastTry := t10.Add(-3*time.Hour), t10
	for name, tc := range map[string]struct {
		since string
		want  time.Time
	}{
		"the onset is its own stamp":                 {instant.Format(onset), onset},
		"no onset: the last attempt is all we know":  {"", lastTry},
		"an unreadable onset":                        {"since lunch", lastTry},
		"an onset after the last attempt is not one": {instant.Format(lastTry.Add(time.Hour)), lastTry},
	} {
		cfg := failCfg("t", lastTry, "3", nil)
		if tc.since != "" {
			cfg[FailingSince("t")] = tc.since
		}
		got, ok := FailureOf(cfg, "t")
		if !ok || !got.FailedAt.Equal(lastTry) || !got.Since.Equal(tc.want) {
			t.Errorf("%s: %+v (failing %v), want failed-at %v and since %v", name, got, ok, lastTry, tc.want)
		}
	}
}

func TestTheFailureKeysReadAsOneFamily(t *testing.T) {
	want := []string{
		"user.tink.backup.copy.nas.failed-at",
		"user.tink.backup.copy.nas.failing-since",
		"user.tink.backup.copy.nas.failures",
	}
	if got := FailureStamps("nas"); !reflect.DeepEqual(got, want) {
		t.Errorf("FailureStamps = %v, want %v", got, want)
	}
	if got := CopiedAt("nas"); got != "user.tink.backup.copy.nas.copied-at" {
		t.Errorf("CopiedAt = %q", got)
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
