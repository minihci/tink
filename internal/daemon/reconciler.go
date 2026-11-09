package daemon

import (
	"context"
	"fmt"
	"time"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/observe"
)

// A Reconciler keeps derived state in step with the stamps on the server. It is level-triggered: a pass reads the server, works out what should be true,
// compares, and writes only what differs, so a pass that finds nothing changed writes nothing and an event is only a hint that it is worth looking. It never
// depends on seeing every event: nothing is replayed after a reconnect, so every subscription is followed by a full pass, and a periodic resync is the
// backstop for the rest.
//
// What it wakes on is what a published name's records are derived from (an instance, its profile, a network) and nothing it does itself: reading a file
// through the API emits two events and writing one emits another, and none of them is in the set, so a pass does not wake the next. (Measured on Incus
// 7.5.1, #272.) A pass that changes nothing writes nothing, so it makes no instance-updated of its own.
type Reconciler struct {
	// Watch subscribes to the server's lifecycle events and calls handle with the action of each. It calls ready once the subscription is active, returns nil when
	// ctx is done, and returns an error when the stream ends before that. incusapi.WatchLifecycle is the real one.
	Watch func(ctx context.Context, ready func(), handle func(action string)) error
	// Reconcile does one full pass and says what it found, one entry per thing it keeps and per site.
	Reconcile func(ctx context.Context) []observe.Result

	// Debounce is the quiet time after the last event before a pass (default 2 seconds), and MaxWait the longest a steady stream of events can put one off
	// (default 10). Resync is the longest between passes (default 5 minutes). Backoff is the first wait before subscribing again (default 2 seconds, doubling up
	// to a minute).
	Debounce, MaxWait, Resync, Backoff time.Duration

	// Live, if set, holds the clock every pass reports to, and the list of what the daemon did, for the status document. Without one the reconciler
	// keeps a clock of its own, so it still logs only a transition, and keeps no events.
	Live *Live
	Logf func(format string, args ...any)
	// Now is a test seam; nil is the real clock.
	Now func() time.Time

	own observe.Clock // used when Live is nil
}

func (r *Reconciler) clock() *observe.Clock {
	if r.Live != nil {
		return &r.Live.clock
	}
	return &r.own
}

// wakeActions are the lifecycle events that can change what a published name's records are derived from: an instance appearing, going, being renamed or
// edited (a stamp, a role, an address), and the profiles and networks the join reads. Everything else, file reads and writes and exec included, is not in it.
var wakeActions = map[string]bool{
	api.EventLifecycleInstanceCreated: true,
	api.EventLifecycleInstanceDeleted: true,
	api.EventLifecycleInstanceRenamed: true,
	api.EventLifecycleInstanceUpdated: true,
	api.EventLifecycleProfileUpdated:  true,
	api.EventLifecycleProfileDeleted:  true,
	api.EventLifecycleNetworkUpdated:  true,
	api.EventLifecycleNetworkDeleted:  true,
}

// WakesReconcile says whether a lifecycle action is a reason to look at the server again.
func WakesReconcile(action string) bool { return wakeActions[action] }

func (r *Reconciler) debounce() time.Duration { return orDuration(r.Debounce, 2*time.Second) }
func (r *Reconciler) maxWait() time.Duration  { return orDuration(r.MaxWait, 10*time.Second) }
func (r *Reconciler) resync() time.Duration   { return orDuration(r.Resync, 5*time.Minute) }
func (r *Reconciler) backoff() time.Duration  { return orDuration(r.Backoff, 2*time.Second) }

func orDuration(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Reconciler) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Run watches the server and reconciles until ctx is done. The first pass follows the first subscription, not the start: a pass that ran before the
// subscription could miss a change made between the two.
func (r *Reconciler) Run(ctx context.Context) error {
	// the watcher stops when Run returns for any reason, a panic included, so a restarted Run does not leave a second subscription behind
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	wake := make(chan struct{}, 1)
	poke := func() {
		select {
		case wake <- struct{}{}:
		default: // one is already waiting; they coalesce
		}
	}
	go r.watch(ctx, poke)

	resync := time.NewTicker(r.resync())
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-resync.C:
		case <-wake:
			if !r.settle(ctx, wake) {
				return nil
			}
		}
		r.pass(ctx)
	}
}

// settle waits for the events to go quiet, so a burst (an apply touches many things) is one pass. It reports false when ctx ended while it waited.
func (r *Reconciler) settle(ctx context.Context, wake <-chan struct{}) bool {
	quiet := time.NewTimer(r.debounce())
	defer quiet.Stop()
	limit := time.NewTimer(r.maxWait())
	defer limit.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-wake:
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(r.debounce())
		case <-quiet.C:
			return true
		case <-limit.C:
			return true
		}
	}
}

// watch keeps a subscription up, subscribing again with a growing wait when the stream ends, and pokes the loop once each subscription is active.
func (r *Reconciler) watch(ctx context.Context, poke func()) {
	wait := r.backoff()
	for ctx.Err() == nil {
		began := r.now()
		err := r.Watch(ctx, poke, func(action string) {
			if WakesReconcile(action) {
				poke()
			}
		})
		if ctx.Err() != nil {
			return
		}
		r.logf("the event stream ended (%v); subscribing again in %s", err, wait)
		if r.now().Sub(began) > time.Minute {
			wait = r.backoff() // it was up for a while: this is a new trouble, not the same one
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if wait *= 2; wait > time.Minute {
			wait = time.Minute
		}
	}
}

// pass runs one reconcile and records what it found. Each result goes to the clock, which says whether it is a transition: the log gets a line then, and
// not on every pass that finds the same thing. A pass that rewrote a file or restarted an instance says so in the log too, because it did something, and
// adds an event for it to the status document. Whatever the pass did not report is forgotten.
func (r *Reconciler) pass(ctx context.Context) {
	results := r.Reconcile(ctx)
	now := r.now()
	clock := r.clock()
	var whats []string
	var events []observe.Event
	for _, res := range results {
		_, changed := clock.Observe(res, now)
		whats = append(whats, res.What)
		events = append(events, observe.EventsOf(res, now)...)
		if changed || res.Wrote {
			r.say(res)
		}
	}
	if r.Live != nil {
		r.Live.addEvents(events...)
	}
	if len(whats) > 0 {
		clock.EndPass(whats...)
	}
}

// say writes the line for a result, and the lines of its findings and its error, which are for the log and nothing else.
func (r *Reconciler) say(res observe.Result) {
	msg := fmt.Sprintf("reconcile %s, %s: %s", observe.Name(res.What, res.Resource, res.Target), observe.SiteName(res.Site), res.State)
	if res.Wrote {
		msg += ", rewrote it"
	}
	if res.Reason != "" {
		msg += " (" + res.Reason + ")"
	}
	r.logf("%s", msg)
	for _, d := range res.Detail {
		r.logf("  %s", d)
	}
	if res.Err != nil {
		r.logf("  %s", res.Err)
	}
}
