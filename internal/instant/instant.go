// Package instant is the one place tink writes and reads a point in time that is stored or exchanged: a stamp on an Incus object, a field of
// the status document, a jobs document, the heartbeat file. The rule is the glossary's ("Time words"): RFC 3339, in UTC, to whole seconds,
// so `2026-11-01T09:30:00Z` and nothing else, however the value was made. A JSON `time.Time` would write nanoseconds and the zone of its
// value, and a stamp written with `Format(time.RFC3339)` would keep whatever zone the caller forgot to convert; both are the thing this
// package exists to prevent.
//
// Time is a JSON type built on Format and Parse; Duration is the same for a length of time, written as Go's Duration.String gives it
// (`10m0s`) and read with time.ParseDuration. Calendar cadences (`daily`, `weekly`) are not durations and are not here.
//
// It is a leaf: it imports nothing of tink's, so any package can use it.
package instant

import (
	"encoding/json"
	"fmt"
	"time"
)

// Format is t as an instant: RFC 3339, UTC, whole seconds. The fraction is dropped, not rounded, so an instant never reads as later than the
// moment it names.
func Format(t time.Time) string {
	return t.UTC().Truncate(time.Second).Format(time.RFC3339)
}

// Parse reads an instant. It accepts RFC 3339 in any zone and with or without a fraction (a hand-edited value should not be refused for its
// style) and returns it in UTC, to whole seconds, so what is read is what Format would have written.
func Parse(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not an RFC 3339 instant", s)
	}
	return t.UTC().Truncate(time.Second), nil
}

// Time is an instant in a JSON document. It writes through Format, reads through Parse, and the zero value is left out of the document
// (with `omitzero`, which Go's encoding/json honours for a type with an IsZero method).
type Time struct{ time.Time }

// At is t as a Time, normalised the way Format would write it.
func At(t time.Time) Time {
	if t.IsZero() {
		return Time{}
	}
	return Time{t.UTC().Truncate(time.Second)}
}

// MarshalJSON writes the instant as a JSON string. The zero value is written as null, for a document that does not omit it.
func (t Time) MarshalJSON() ([]byte, error) {
	if t.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(Format(t.Time))
}

// UnmarshalJSON reads a JSON string through Parse. null and an empty string are the zero value.
func (t *Time) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*t = Time{}
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("an instant must be a string: %w", err)
	}
	if s == "" {
		*t = Time{}
		return nil
	}
	parsed, err := Parse(s)
	if err != nil {
		return err
	}
	*t = Time{parsed}
	return nil
}

// Duration is a length of time in a JSON document, written as a string the way Duration.String gives it (`10m0s`) and read with
// time.ParseDuration. A bare number is refused: it would not say whether it is seconds or nanoseconds.
type Duration time.Duration

// D is the time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// MarshalJSON writes the duration as a JSON string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON reads a JSON string through time.ParseDuration.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("a duration must be a string such as \"10m0s\": %w", err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// IsZero lets `omitzero` leave out a duration that was not set.
func (d Duration) IsZero() bool { return d == 0 }
