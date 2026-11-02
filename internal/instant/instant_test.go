package instant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFormatIsUTCWholeSeconds(t *testing.T) {
	zone := time.FixedZone("CET", 3600)
	at := time.Date(2026, 11, 1, 10, 30, 15, 987_000_000, zone)
	if got, want := Format(at), "2026-11-01T09:30:15Z"; got != want {
		t.Errorf("Format = %q, want %q (UTC, fraction dropped)", got, want)
	}
	// dropped, not rounded: .9999 must not read as the next second
	if got := Format(time.Date(2026, 11, 1, 0, 0, 0, 999_999_999, time.UTC)); got != "2026-11-01T00:00:00Z" {
		t.Errorf("Format rounded up: %q", got)
	}
}

func TestParseRoundTripsAndNormalises(t *testing.T) {
	at := time.Date(2026, 11, 1, 9, 30, 15, 0, time.UTC)
	got, err := Parse(Format(at))
	if err != nil || !got.Equal(at) || got.Location() != time.UTC {
		t.Fatalf("round trip = %v, %v; want %v in UTC", got, err, at)
	}
	// a hand-written value in another zone, with a fraction, reads as what Format would have written
	got, err = Parse("2026-11-01T10:30:15.75+01:00")
	if err != nil || !got.Equal(at) || got.Location() != time.UTC || got.Nanosecond() != 0 {
		t.Errorf("Parse(+01:00 with fraction) = %v, %v; want %v", got, err, at)
	}
	for _, bad := range []string{"", "yesterday", "2026-11-01", "2026-11-01 09:30:15"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) succeeded", bad)
		}
	}
}

type doc struct {
	At    Time     `json:"at,omitzero"`
	Every Duration `json:"every,omitzero"`
}

func TestTimeJSON(t *testing.T) {
	zone := time.FixedZone("CET", 3600)
	b, err := json.Marshal(doc{At: At(time.Date(2026, 11, 1, 10, 30, 15, 123_456_789, zone)), Every: Duration(10 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"at":"2026-11-01T09:30:15Z","every":"10m0s"}`; string(b) != want {
		t.Errorf("marshal = %s, want %s", b, want)
	}
	var back doc
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.At.Equal(time.Date(2026, 11, 1, 9, 30, 15, 0, time.UTC)) || back.Every.D() != 10*time.Minute {
		t.Errorf("round trip = %+v", back)
	}
}

func TestZeroValuesAreLeftOut(t *testing.T) {
	b, err := json.Marshal(doc{})
	if err != nil || string(b) != `{}` {
		t.Errorf("zero doc = %s, %v; want {}", b, err)
	}
	if got := At(time.Time{}); !got.IsZero() {
		t.Errorf("At(zero) = %v", got)
	}
	var d doc
	if err := json.Unmarshal([]byte(`{"at":null}`), &d); err != nil || !d.At.IsZero() {
		t.Errorf("null = %+v, %v", d, err)
	}
	if err := json.Unmarshal([]byte(`{"at":""}`), &d); err != nil || !d.At.IsZero() {
		t.Errorf("empty = %+v, %v", d, err)
	}
}

func TestRefusals(t *testing.T) {
	for in, want := range map[string]string{
		`{"at":"soon"}`:     "not an RFC 3339 instant",
		`{"at":12345}`:      "must be a string",
		`{"every":600}`:     "must be a string",
		`{"every":"10"}`:    "not a duration",
		`{"every":"often"}`: "not a duration",
	} {
		var d doc
		err := json.Unmarshal([]byte(in), &d)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to say %q", in, err, want)
		}
	}
}
