package daemon

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/instant"
	"github.com/minihci/tink/internal/observe"
)

var stateT0 = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func derivedResult(state, reason string) observe.Result {
	return observe.Result{What: "derived", Resource: "ingress-routes", Site: "home", State: state, Reason: reason}
}

// storedBy is the text a daemon with this Live would have stored: the document, with the protocol the publisher stamps.
func storedBy(t *testing.T, l *Live) string {
	t.Helper()
	st := buildStatus(StatusOptions{Zone: time.UTC}, l, stateT0, stateT0)
	st.Proto = helper.StatusProto
	text, err := helper.Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func TestARewriteIsAHealedEventAndARestartARestartedEventWithNoContent(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() >= 1 })
	if got := h.events(); len(got) != 0 {
		t.Fatalf("a pass that wrote nothing made events: %+v", got)
	}

	rewrote := derivedResult(observe.Converged, "")
	rewrote.Wrote = true
	rewrote.Err = errors.New("secret-looking detail")
	rewrote.Detail = []finding.Finding{finding.Notef("the file was rewritten", "derived/ingress-routes", "file text: password=hunter2")}
	h.result.Store([]observe.Result{rewrote})
	h.s.emit("instance-updated")
	eventually(t, "a healed event", func() bool { return len(h.events()) == 1 })

	restarted := rewrote
	restarted.Restarted = true
	h.result.Store([]observe.Result{restarted})
	h.s.emit("instance-updated")
	eventually(t, "a healed and a restarted event", func() bool { return len(h.events()) == 3 })

	got := h.events()
	if got[0].Event != observe.Healed || got[1].Event != observe.Healed || got[2].Event != observe.Restarted {
		t.Errorf("events = %+v, want healed, then healed and restarted", got)
	}
	for _, e := range got {
		if e.What != "derived" || e.Resource != "ingress-routes" || e.Site != "home" || e.At.IsZero() {
			t.Errorf("event = %+v, want what, resource, site and a time", e)
		}
	}
	st := buildStatus(StatusOptions{Zone: time.UTC}, h.r.Live, stateT0, stateT0)
	text, err := helper.Encode(st)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"secret-looking", "hunter2", "password"} {
		if strings.Contains(text, bad) {
			t.Errorf("the status document carries %q: %s", bad, text)
		}
	}
	if !strings.Contains(text, `"events":[{"event":"healed","what":"derived","resource":"ingress-routes","site":"home","at":`) {
		t.Errorf("the events are not in the document: %s", text)
	}
}

func TestTheEventListIsBoundedToTheMostRecent(t *testing.T) {
	var l Live
	for i := 0; i < observe.MaxEvents+7; i++ {
		l.addEvents(observe.Event{Event: observe.Healed, What: "derived", Resource: string(rune('a' + i)), At: instant.At(stateT0.Add(time.Duration(i) * time.Minute))})
	}
	_, got := l.snapshot()
	if len(got) != observe.MaxEvents || got[len(got)-1].Resource != string(rune('a'+observe.MaxEvents+6)) {
		t.Errorf("%d events, the last %+v: want the most recent %d", len(got), got[len(got)-1], observe.MaxEvents)
	}
}

// What a daemon said before it restarted is what it reads back: the events, and the since of every state the first pass confirms.
func TestTheEventsAndTheStatesSurviveARestart(t *testing.T) {
	old := stateT0.Add(-24 * time.Hour) // before every reading the harness's clock gives
	var first Live
	first.clock.Observe(derivedResult(observe.Blocked, "the file is not there"), old)
	first.clock.Observe(observe.Result{What: "dns", Site: "home", State: observe.Converged}, old)
	first.clock.Observe(observe.Result{What: "derived", Resource: "gone", Site: "home", State: observe.Failing, Reason: "the file could not be read"}, old)
	first.addEvents(observe.Event{Event: observe.Healed, What: "derived", Resource: "ingress-routes", Site: "home", At: instant.At(old)})
	stored, err := helper.Parse(storedBy(t, &first))
	if err != nil {
		t.Fatal(err)
	}

	var second Live
	second.Seed(stored)
	// one pass that confirms the blocked file, contradicts the dns entry and does not see the failing one
	h := startWith(t, &second, derivedResult(observe.Blocked, "the file is not there"), observe.Result{What: "dns", Site: "home", State: observe.Failing, Reason: "the server could not be read"})
	eventually(t, "the first pass", func() bool { return len(h.states()) == 2 })

	states := map[string]observe.Record{}
	for _, r := range h.states() {
		states[r.Name()] = r
	}
	if len(states) != 2 {
		t.Fatalf("states = %+v, want the confirmed one and the contradicted one, and not the one the pass did not see", states)
	}
	if routes := states["derived/ingress-routes"]; !routes.Since.Equal(old) {
		t.Errorf("a state the first pass confirms keeps its since: %+v", routes)
	}
	if dns := states["dns"]; !dns.Since.After(old) {
		t.Errorf("a state the first pass contradicts starts over: %+v", dns)
	}
	if ev := h.events(); len(ev) != 1 || ev[0].Event != observe.Healed || !ev[0].At.Equal(old) {
		t.Errorf("events = %+v, want the one from before the restart", ev)
	}
	// a state the restart confirmed is not announced again; the one it contradicted is
	log := h.log.text()
	if strings.Contains(log, "ingress-routes") {
		t.Errorf("a standing problem was logged again after a restart:\n%s", log)
	}
	if !strings.Contains(log, "reconcile dns") {
		t.Errorf("a changed state was not logged:\n%s", log)
	}
	// and the one the pass did not see is gone for good: it is new, with a since of its own, if it comes back
	h.result.Store([]observe.Result{{What: "derived", Resource: "gone", Site: "home", State: observe.Failing, Reason: "the file could not be read"}})
	h.s.emit("instance-updated")
	eventually(t, "it comes back", func() bool {
		for _, r := range h.states() {
			if r.Resource == "gone" {
				return r.Since.After(old)
			}
		}
		return false
	})
}

func TestADocumentThatCannotBeReadSeedsNothing(t *testing.T) {
	for name, text := range map[string]string{
		"not json":         "not json",
		"no protocol":      `{"states":[{"what":"derived","state":"blocked","since":"2026-11-01T09:00:00Z"}]}`,
		"a newer protocol": `{"proto":99,"states":[{"what":"derived","resource":"ingress-routes","site":"home","state":"blocked","reason":"the file is not there","since":"2026-11-01T09:00:00Z"}]}`,
	} {
		var live Live
		p := &helper.Publisher{Read: func() (string, error) { return text, nil }}
		if st, err := p.Load(); err == nil {
			live.Seed(st)
			t.Errorf("%s: Load read %+v", name, st)
		}
		h := startWith(t, &live, derivedResult(observe.Blocked, "the file is not there"))
		eventually(t, "a pass", func() bool { return len(h.states()) == 1 })
		if st := h.states()[0]; st.Since.Equal(stateT0) {
			t.Errorf("%s: since = %v: a document that cannot be read must start the clock empty", name, st.Since)
		}
	}
}

// Run seeds the Live from the publisher's stored document before the first pass, and starts empty when the read fails.
func TestRunSeedsTheClockFromTheStoredDocumentBeforeTheFirstPass(t *testing.T) {
	var prev Live
	prev.clock.Observe(derivedResult(observe.Blocked, "the file is not there"), stateT0)
	stored := storedBy(t, &prev)

	for name, read := range map[string]func() (string, error){
		"a stored document": func() (string, error) { return stored, nil },
		"a read that fails": func() (string, error) { return "", errors.New("the volume is not there") },
	} {
		var mu sync.Mutex
		var written []string
		pub := &helper.Publisher{Read: read, NoMarker: true, Heartbeat: time.Hour, Patch: func(c map[string]string) error {
			mu.Lock()
			defer mu.Unlock()
			written = append(written, c[helper.StatusKey])
			return nil
		}}
		s := newStream()
		later := stateT0.Add(48 * time.Hour)
		rc := &Reconciler{Watch: s.watch, Debounce: 10 * time.Millisecond, Resync: time.Hour, Backoff: time.Millisecond, Now: func() time.Time { return later },
			Reconcile: func(context.Context) []observe.Result {
				return []observe.Result{derivedResult(observe.Blocked, "the file is not there")}
			}}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- Run(ctx, io.Discard, RunOptions{Reconciler: rc, RestartBackoff: time.Millisecond, Status: &StatusOptions{Publisher: pub, Interval: 5 * time.Millisecond}})
		}()
		var got helper.Status
		eventually(t, name+": a document with the state", func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, w := range written {
				if st, err := helper.Parse(w); err == nil && len(st.States) == 1 {
					got = st
					return true
				}
			}
			return false
		})
		cancel()
		<-done
		want := stateT0
		if name == "a read that fails" {
			want = later
		}
		if !got.States[0].Since.Equal(want) {
			t.Errorf("%s: since = %v, want %v", name, got.States[0].Since, want)
		}
	}
}

func TestALogLineIsWrittenOnlyOnATransition(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() >= 1 })
	step := func(r observe.Result, want int) {
		t.Helper()
		before := h.passes.Load()
		h.result.Store([]observe.Result{r})
		h.s.emit("instance-updated")
		eventually(t, "a pass", func() bool { return h.passes.Load() > before })
		time.Sleep(20 * time.Millisecond)
		if got := strings.Count(h.log.text(), "reconcile "); got != want {
			t.Errorf("after %+v: %d log lines about a reconcile, want %d:\n%s", r, got, want, h.log.text())
		}
	}
	dns := func(state, reason string) observe.Result {
		return observe.Result{What: "dns", State: state, Reason: reason}
	}
	step(dns(observe.Converged, ""), 1) // the first look found the same thing the harness's first pass did
	step(dns(observe.Blocked, "the apply was refused"), 2)
	step(dns(observe.Blocked, "the apply was refused"), 2)
	step(dns(observe.Blocked, "the join has errors"), 3) // a new reason is a transition
	step(dns(observe.Blocked, "the join has errors"), 3)
	step(dns(observe.Converged, ""), 4)
}

func TestAThingThePassNoLongerReportsIsForgotten(t *testing.T) {
	h := start(t)
	eventually(t, "the first pass", func() bool { return h.passes.Load() >= 1 })
	h.result.Store([]observe.Result{derivedResult(observe.Converged, ""), {What: "derived", Resource: "dns-hosts", Site: "home", State: observe.Converged}})
	h.s.emit("instance-updated")
	eventually(t, "both reported", func() bool { return len(h.states()) == 3 }) // and the harness's first dns result
	h.result.Store([]observe.Result{derivedResult(observe.Converged, "")})
	h.s.emit("instance-updated")
	eventually(t, "one forgotten", func() bool {
		for _, r := range h.states() {
			if r.Resource == "dns-hosts" {
				return false
			}
		}
		return true
	})
}
