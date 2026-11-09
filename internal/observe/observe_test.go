package observe

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/instant"
)

var t0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func res(state, reason string) Result {
	return Result{What: "derived", Resource: "ingress-routes", Site: "home", State: state, Reason: reason}
}

func TestTheClockKeepsSinceWhileStateAndReasonAreTheSame(t *testing.T) {
	var c Clock
	first, changed := c.Observe(res(Blocked, "the file is not there"), t0)
	if !changed || !first.Since.Equal(t0) {
		t.Fatalf("first look = %+v changed %v, want a transition at t0", first, changed)
	}
	again, changed := c.Observe(res(Blocked, "the file is not there"), t0.Add(time.Hour))
	if changed || !again.Since.Equal(t0) {
		t.Errorf("same state and reason = %+v changed %v, want since kept and no transition", again, changed)
	}
}

func TestTheClockResetsSinceWhenTheStateChanges(t *testing.T) {
	var c Clock
	c.Observe(res(Converged, ""), t0)
	got, changed := c.Observe(res(Failing, "the file could not be written"), t0.Add(time.Hour))
	if !changed || !got.Since.Equal(t0.Add(time.Hour)) {
		t.Errorf("a change of state = %+v changed %v, want since reset", got, changed)
	}
}

func TestTheClockResetsSinceWhenOnlyTheReasonChanges(t *testing.T) {
	var c Clock
	c.Observe(res(Blocked, "the file is not there"), t0)
	got, changed := c.Observe(res(Blocked, "the join has errors"), t0.Add(time.Hour))
	if !changed || !got.Since.Equal(t0.Add(time.Hour)) {
		t.Errorf("a change of reason = %+v changed %v, want since reset", got, changed)
	}
}

func TestAWriteIsNotAStateSoItDoesNotMoveSince(t *testing.T) {
	var c Clock
	c.Observe(res(Converged, ""), t0)
	r := res(Converged, "")
	r.Wrote = true
	got, changed := c.Observe(r, t0.Add(time.Hour))
	if changed || !got.Since.Equal(t0) {
		t.Errorf("a converged pass that wrote = %+v changed %v, want since kept", got, changed)
	}
}

func TestTheClockForgetsAnEntryThePassDidNotReport(t *testing.T) {
	var c Clock
	c.Observe(res(Converged, ""), t0)
	gone := res(Converged, "")
	gone.Resource = "dns-hosts"
	c.Observe(gone, t0)
	c.EndPass("derived")
	if len(c.States()) != 2 {
		t.Fatalf("after a pass that reported both: %+v", c.States())
	}
	c.Observe(res(Converged, ""), t0.Add(time.Minute))
	c.EndPass("derived")
	got := c.States()
	if len(got) != 1 || got[0].Resource != "ingress-routes" {
		t.Errorf("after a pass that reported one: %+v, want only ingress-routes", got)
	}
	// Reported again later, it is new: its since is when it came back.
	back, changed := c.Observe(gone, t0.Add(time.Hour))
	if !changed || !back.Since.Equal(t0.Add(time.Hour)) {
		t.Errorf("an entry that came back = %+v changed %v, want it new", back, changed)
	}
}

func TestAPassOnlyForgetsTheKindsItWasAskedAbout(t *testing.T) {
	var c Clock
	c.Observe(Result{What: "backup", Resource: "lib", State: Skipped, Reason: "x"}, t0)
	c.Observe(res(Converged, ""), t0)
	c.EndPass("derived") // saw the derived one
	c.EndPass("derived") // and now it did not
	got := c.States()
	if len(got) != 1 || got[0].What != "backup" {
		t.Errorf("states = %+v, want the backup entry kept by a pass over derived", got)
	}
}

func TestTheSeededClockKeepsSinceForAnEntryTheFirstPassConfirms(t *testing.T) {
	var c Clock
	stored := Record{What: "derived", Resource: "ingress-routes", Site: "home", State: Blocked, Reason: "the file is not there", Since: instant.At(t0)}
	c.Seed([]Record{stored})
	if got := c.States(); len(got) != 0 {
		t.Fatalf("a seeded entry is claimed before anything has looked: %+v", got)
	}
	got, changed := c.Observe(res(Blocked, "the file is not there"), t0.Add(48*time.Hour))
	if changed || !got.Since.Equal(t0) {
		t.Errorf("confirmed = %+v changed %v, want the stored since and no transition", got, changed)
	}
	c.EndPass("derived")
	if len(c.States()) != 1 {
		t.Errorf("the confirmed entry was dropped: %+v", c.States())
	}
}

func TestTheSeededClockResetsAnEntryTheFirstPassContradicts(t *testing.T) {
	now := t0.Add(48 * time.Hour)
	for name, r := range map[string]Result{
		"state":  res(Converged, ""),
		"reason": res(Blocked, "the join has errors"),
	} {
		var c Clock
		c.Seed([]Record{{What: "derived", Resource: "ingress-routes", Site: "home", State: Blocked, Reason: "the file is not there", Since: instant.At(t0)}})
		got, changed := c.Observe(r, now)
		if !changed || !got.Since.Equal(now) {
			t.Errorf("a contradicted %s = %+v changed %v, want since reset", name, got, changed)
		}
	}
}

func TestTheSeededClockDropsAnEntryTheFirstPassDoesNotSee(t *testing.T) {
	var c Clock
	c.Seed([]Record{{What: "derived", Resource: "gone", Site: "home", State: Failing, Reason: "r", Since: instant.At(t0)}})
	c.Observe(res(Converged, ""), t0.Add(time.Hour))
	c.EndPass("derived")
	// It comes back later: it is new, with a since of its own, not the stored one.
	gone := res(Failing, "r")
	gone.Resource = "gone"
	got, changed := c.Observe(gone, t0.Add(2*time.Hour))
	if !changed || !got.Since.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("an entry the first pass did not see = %+v changed %v, want it dropped and new when it returns", got, changed)
	}
}

func TestAClockSeededWithNothingStartsEmpty(t *testing.T) {
	var c Clock
	c.Seed(nil)
	got, changed := c.Observe(res(Blocked, "r"), t0)
	if !changed || !got.Since.Equal(t0) || len(c.States()) != 1 {
		t.Errorf("an empty seed = %+v changed %v", got, changed)
	}
}

func TestAStoredEntryWithNoSinceGetsOneFromTheFirstPassAndIsNotATransition(t *testing.T) {
	var c Clock
	c.Seed([]Record{{What: "derived", Resource: "ingress-routes", Site: "home", State: Converged}})
	got, changed := c.Observe(res(Converged, ""), t0)
	if changed || !got.Since.Equal(t0) {
		t.Errorf("= %+v changed %v, want since from this pass and no transition", got, changed)
	}
}

func TestEntriesAreKeyedByTargetToo(t *testing.T) {
	var c Clock
	a := Result{What: "backup", Resource: "lib", Target: "nas", State: Failing, Reason: "x"}
	b := a
	b.Target = "cloud"
	c.Observe(a, t0)
	_, changed := c.Observe(b, t0)
	if !changed || len(c.States()) != 2 {
		t.Errorf("two targets of one volume are two entries: %+v", c.States())
	}
}

func TestStatesComeInAFixedOrder(t *testing.T) {
	var c Clock
	for _, name := range []string{"b", "c", "a"} {
		r := res(Converged, "")
		r.Resource = name
		c.Observe(r, t0)
	}
	got := c.States()
	if got[0].Resource != "a" || got[1].Resource != "b" || got[2].Resource != "c" {
		t.Errorf("order = %+v", got)
	}
}

func TestARecordCarriesNoErrorTextAndNoFlagsOfTheResult(t *testing.T) {
	var c Clock
	r := res(Failing, "the file could not be written")
	r.Err = errors.New("token abc123 refused")
	r.Wrote = true
	r.Detail = []finding.Finding{finding.Errorf("a reason", "derived/x", "secret-looking detail")}
	rec, _ := c.Observe(r, t0)
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, bad := range []string{"abc123", "secret-looking", "wrote", "detail"} {
		if strings.Contains(text, bad) {
			t.Errorf("the record %s carries %q", text, bad)
		}
	}
	want := `{"what":"derived","resource":"ingress-routes","site":"home","state":"failing","reason":"the file could not be written","since":"2026-11-01T09:00:00Z"}`
	if text != want {
		t.Errorf("record = %s, want %s", text, want)
	}
}

func TestAFailingCopyRecordHasItsExtras(t *testing.T) {
	rec := Record{What: WhatBackup, Resource: "lib", Target: "nas", State: Failing, Reason: "the copy is failing", Since: instant.At(t0), FailedAt: instant.At(t0.Add(time.Hour)), Failures: 3}
	b, _ := json.Marshal(rec)
	want := `{"what":"backup","resource":"lib","target":"nas","state":"failing","reason":"the copy is failing","since":"2026-11-01T09:00:00Z","failed-at":"2026-11-01T10:00:00Z","failures":3}`
	if string(b) != want {
		t.Errorf("record = %s, want %s", b, want)
	}
}

func TestAResultThatWroteIsAHealedEventAndARestartIsARestartedEvent(t *testing.T) {
	r := res(Converged, "")
	if got := EventsOf(r, t0); len(got) != 0 {
		t.Errorf("a pass that did nothing made events %+v", got)
	}
	r.Wrote = true
	got := EventsOf(r, t0)
	if len(got) != 1 || got[0].Event != Healed || got[0].Resource != "ingress-routes" || got[0].Site != "home" || !got[0].At.Equal(t0) {
		t.Errorf("a write = %+v, want one healed event", got)
	}
	r.Restarted = true
	got = EventsOf(r, t0)
	if len(got) != 2 || got[0].Event != Healed || got[1].Event != Restarted {
		t.Errorf("a write and a restart = %+v", got)
	}
}

func TestTheEventListKeepsTheMostRecentTwenty(t *testing.T) {
	var list []Event
	for i := 0; i < MaxEvents+5; i++ {
		list = Append(list, Event{Event: Healed, What: "derived", Resource: string(rune('a' + i)), At: instant.At(t0.Add(time.Duration(i) * time.Minute))})
	}
	if len(list) != MaxEvents {
		t.Fatalf("%d events, want %d", len(list), MaxEvents)
	}
	if list[0].Resource != string(rune('a'+5)) || list[len(list)-1].Resource != string(rune('a'+MaxEvents+4)) {
		t.Errorf("kept %q .. %q, want the most recent", list[0].Resource, list[len(list)-1].Resource)
	}
}

func TestAStoredEventListKeepsOnlyTheFixedWordsAndIsBounded(t *testing.T) {
	var stored []Event
	for i := 0; i < 30; i++ {
		stored = append(stored, Event{Event: Healed, What: "derived", At: instant.At(t0)})
	}
	stored = append(stored, Event{Event: "file contents: password=hunter2", What: "derived"})
	got := KnownEvents(stored)
	if len(got) != MaxEvents {
		t.Errorf("%d events, want %d", len(got), MaxEvents)
	}
	for _, e := range got {
		if e.Event != Healed {
			t.Errorf("an event with the word %q was kept", e.Event)
		}
	}
}
