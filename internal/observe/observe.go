// Package observe is the one record of what the daemon found, and the one rule for how long it has been true.
//
// A reconcile pass (join.ReconcileDNS, join.ReconcileDerived) and a look at the backup volumes both say "this thing is in this state, because of this
// reason". Before this package each said it in a type of its own, with state words of its own, and a hand-written rule in the daemon decided when a state
// had begun. Now there is
//
//   - Result, what one look at one thing found: returned by both reconcilers, and the only thing they return;
//   - Record, a state as the status document stores it: the same core, plus since, and no error text and no file content;
//   - Event, one entry of the short list of what the daemon did (healed, restarted);
//   - Clock, the rule: a state keeps its since while the state and the reason are unchanged, and a stored document seeds it, so a restart does not
//     make every standing problem look new.
//
// The words of a state are Converged, Blocked, Skipped and Failing. A rewrite of a file is not a state: Result.Wrote says it, and the event list records it.
//
// This is a leaf: it imports the instant and finding packages and nothing else of tink's, so join, daemon and helper can all use it and none of them has
// to import another.
package observe

import (
	"sort"
	"sync"
	"time"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/instant"
)

// The words of a state. They are fixed, because the status document carries them and holds no error text.
const (
	Converged = "converged" // what is kept is as it should be, whether or not this pass had to write it
	Blocked   = "blocked"   // a refusal or a finding stopped it, on purpose; nothing was written
	Skipped   = "skipped"   // there is nothing to keep, or it was left alone for now (no stamped file; the instance is not running; a volume that is not copied)
	Failing   = "failing"   // the server, a helper or an instance could not be read, written or restarted
)

// WhatBackup is the What of the states that come from the volumes' backup copies; the reconcilers name theirs ("dns", "derived").
const WhatBackup = "backup"

// The events. They are a fixed set of words, so that an event list cannot carry anything a person did not decide to say.
const (
	Healed    = "healed"    // a derived file was rewritten
	Restarted = "restarted" // an instance was restarted after its file was rewritten
)

// MaxEvents is how many events the status document keeps: the most recent.
const MaxEvents = 20

// A Result is what one look at one thing found. State and Reason are the words the status document carries, and Reason is the fixed phrase of the finding
// behind the state (a literal where it is raised, never built from what was found), empty for a state that is fine. Wrote, Restarted, Detail and Err are for
// the log and the event list: none of them reaches a state record.
type Result struct {
	// What is the thing that is kept ("dns", "derived", "backup"), Resource which one of them ("ingress-routes", a volume; "" when the result is about the
	// whole site) and Site the site it is for ("" is the unnamed site). Target is the other end of a copy.
	What, Resource, Site, Target string
	State, Reason                string
	// Wrote says the pass rewrote a file; Restarted says it restarted the instance the file is on. Each is worth an event.
	Wrote, Restarted bool
	// Detail is findings and notes for the log, and Err the cause, for the log. Neither is stored: errors from Incus can echo a credential, and a config
	// value is the wrong place for one.
	Detail []finding.Finding
	Err    error
}

// A Record is a state as the status document stores it: what was found and since when. Since is when the state began, not when it was last looked at.
// A failing copy adds the time of its latest failure and how many in a row (FailedAt, Failures).
type Record struct {
	What     string       `json:"what"`
	Resource string       `json:"resource,omitempty"`
	Site     string       `json:"site,omitempty"`
	Target   string       `json:"target,omitempty"`
	State    string       `json:"state"`
	Reason   string       `json:"reason,omitempty"`
	Since    instant.Time `json:"since,omitzero"`
	FailedAt instant.Time `json:"failed-at,omitzero"`
	Failures int          `json:"failures,omitempty"`
}

// An Event is one thing the daemon did: a fixed word, what it was done to, and when. It carries no content of a file.
type Event struct {
	Event    string       `json:"event"`
	What     string       `json:"what"`
	Resource string       `json:"resource,omitempty"`
	Site     string       `json:"site,omitempty"`
	At       instant.Time `json:"at"`
}

// EventsOf are the events a result says happened, at now: healed when it rewrote a file, restarted when it restarted the instance.
func EventsOf(r Result, now time.Time) []Event {
	var out []Event
	ev := Event{What: r.What, Resource: r.Resource, Site: r.Site, At: instant.At(now)}
	if r.Wrote {
		ev.Event = Healed
		out = append(out, ev)
	}
	if r.Restarted {
		ev.Event = Restarted
		out = append(out, ev)
	}
	return out
}

// Append adds events to a list and keeps the most recent MaxEvents of it. It does not change its arguments.
func Append(list []Event, more ...Event) []Event {
	out := append(append(make([]Event, 0, len(list)+len(more)), list...), more...)
	if len(out) > MaxEvents {
		out = out[len(out)-MaxEvents:]
	}
	return out
}

// KnownEvents is a stored list as a reader may trust it: only events with one of the fixed words, and at most the most recent MaxEvents. The list comes
// from a document this daemon wrote, but a document is text anyone with the right to edit the volume can change, and this list is the one place the daemon
// would otherwise copy words it did not choose.
func KnownEvents(stored []Event) []Event {
	var out []Event
	for _, e := range stored {
		if e.Event == Healed || e.Event == Restarted {
			out = append(out, Event{Event: e.Event, What: e.What, Resource: e.Resource, Site: e.Site, At: e.At})
		}
	}
	return Append(nil, out...)
}

type key struct{ what, resource, site, target string }

func keyOf(r Result) key { return key{r.What, r.Resource, r.Site, r.Target} }

func keyOfRecord(r Record) key { return key{r.What, r.Resource, r.Site, r.Target} }

// A Clock says how long each state has been true. The zero Clock is ready to use, and it is safe for several goroutines (a reconcile pass, the backup
// scheduler, the status worker).
//
// The rule is one sentence. Observe keeps a state's Since while its state and its reason are unchanged, and resets it when either changes. A look that
// reports a thing is a pass's way of saying "it is still so"; EndPass forgets whatever a pass did not report, so a thing that is gone does not stay in
// the document. Seed gives the clock what a stored document said before a restart: such an entry keeps its Since if the first pass reports the same
// state and reason, loses it if the pass reports something else, and is dropped if the pass does not report it. Seeded entries are not part of
// States until they are confirmed: a document must not claim, before anything has looked, what an earlier life of the daemon last saw.
type Clock struct {
	mu   sync.Mutex
	cur  map[key]Record
	seed map[key]Record
	seen map[key]bool
}

// Observe records what a pass found at now and returns the state record for it. changed says this is a transition: the thing is new, or its state or
// its reason differs from what the clock held. A thing the clock held from a stored document and the pass confirms is not a transition, so a restart
// does not announce every standing problem again.
func (c *Clock) Observe(r Result, now time.Time) (rec Record, changed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := keyOf(r)
	prev, ok := c.cur[k]
	if !ok {
		prev, ok = c.seed[k]
	}
	rec = Record{What: r.What, Resource: r.Resource, Site: r.Site, Target: r.Target, State: r.State, Reason: r.Reason, Since: instant.At(now)}
	if ok && prev.State == r.State && prev.Reason == r.Reason {
		if !prev.Since.IsZero() {
			rec.Since = prev.Since
		}
	} else {
		changed = true
	}
	if c.cur == nil {
		c.cur, c.seen = map[key]Record{}, map[key]bool{}
	}
	delete(c.seed, k)
	c.cur[k], c.seen[k] = rec, true
	return rec, changed
}

// EndPass ends a pass over the given kinds of thing (the What of each; none means every kind) and forgets the entries of those kinds that the pass did not
// report, seeded ones included. The next pass starts from nothing reported.
func (c *Clock) EndPass(whats ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	in := func(w string) bool {
		if len(whats) == 0 {
			return true
		}
		for _, x := range whats {
			if x == w {
				return true
			}
		}
		return false
	}
	for k := range c.cur {
		if in(k.what) && !c.seen[k] {
			delete(c.cur, k)
		}
	}
	for k := range c.seed {
		if in(k.what) {
			delete(c.seed, k)
		}
	}
	for k := range c.seen {
		if in(k.what) {
			delete(c.seen, k)
		}
	}
}

// Seed loads the states of a stored document. What it held before is replaced. A caller that could not read the document seeds nothing, and the clock
// starts empty.
func (c *Clock) Seed(stored []Record) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seed = make(map[key]Record, len(stored))
	for _, r := range stored {
		c.seed[keyOfRecord(r)] = r
	}
}

// States are the entries the clock holds that a pass has reported, in a fixed order, so that the document built from them is the same text for the same
// facts (the publisher writes only when the text changes).
func (c *Clock) States() []Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Record, 0, len(c.cur))
	for _, r := range c.cur {
		out = append(out, r)
	}
	SortRecords(out)
	return out
}

// SortRecords puts records in the order a document lists them: by what, resource, site and target.
func SortRecords(rs []Record) {
	sort.Slice(rs, func(i, j int) bool {
		a, b := rs[i], rs[j]
		if a.What != b.What {
			return a.What < b.What
		}
		if a.Resource != b.Resource {
			return a.Resource < b.Resource
		}
		if a.Site != b.Site {
			return a.Site < b.Site
		}
		return a.Target < b.Target
	})
}

// Name is a record, or a result's thing, as a line says it: "derived/ingress-routes", "backup/lib -> nas".
func (r Record) Name() string { return Name(r.What, r.Resource, r.Target) }

// Name joins what, resource and target the way a person reads them.
func Name(what, resource, target string) string {
	name := what
	if resource != "" {
		name += "/" + resource
	}
	if target != "" {
		name += " -> " + target
	}
	return name
}

// SiteName says a site for a person: the unnamed site is not an empty string.
func SiteName(site string) string {
	if site == "" {
		return "the unnamed site"
	}
	return site
}
