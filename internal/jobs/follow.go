package jobs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

// FollowOptions tune Follow. The zero value is right for a person at a terminal.
type FollowOptions struct {
	// Interval is how soon the first poll after a change comes; MaxInterval is how far it backs off while nothing changes.
	// Each poll of a job over the file API is two requests to the host, each leaving an event in its log, so a copy that runs for
	// hours must not be polled every second. Defaults 2s and 30s.
	Interval, MaxInterval time.Duration
	// Patience is how long reads may fail in a row (the helper restarting, a network blip) before Follow gives up. Default 5 minutes.
	Patience time.Duration
	// Sleep waits for d or until ctx ends. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration)
	// Now is the clock, for Patience. Tests replace it.
	Now func() time.Time
}

func (o FollowOptions) withDefaults() FollowOptions {
	if o.Interval <= 0 {
		o.Interval = 2 * time.Second
	}
	if o.MaxInterval < o.Interval {
		o.MaxInterval = 30 * time.Second
		if o.MaxInterval < o.Interval {
			o.MaxInterval = o.Interval
		}
	}
	if o.Patience <= 0 {
		o.Patience = 5 * time.Minute
	}
	if o.Sleep == nil {
		o.Sleep = func(ctx context.Context, d time.Duration) {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Follow prints a job's log as it grows and returns the job's final status. It ends early, with ctx's error, when ctx is cancelled:
// the job is not touched, it carries on. A job that is not there (yet) is an error at once, because a caller follows what it has
// just queued.
func Follow(ctx context.Context, s Store, id string, out io.Writer, opt FollowOptions) (Status, error) {
	opt = opt.withDefaults()
	var (
		printed   int
		lastState State
		wait      = opt.Interval
		failingAt time.Time
		warned    bool
	)
	for {
		st, err := s.Status(id)
		var log string
		if err == nil {
			log, err = s.Log(id)
		}
		if err != nil {
			if lastState == "" {
				return Status{}, err
			}
			if failingAt.IsZero() {
				failingAt = opt.Now()
			}
			if !warned {
				fmt.Fprintf(out, "[cannot reach the job just now (%v); trying again]\n", err)
				warned = true
			}
			if opt.Now().Sub(failingAt) >= opt.Patience {
				return Status{}, fmt.Errorf("lost contact with job %s for %s: %w (the job is not affected; look again with `tink helper log %s`)", id, opt.Patience, err, id)
			}
		} else {
			failingAt, warned = time.Time{}, false
			changed := st.State != lastState
			if len(log) < printed { // a log that got shorter is a different log: show it from the start
				printed = 0
			}
			if len(log) > printed {
				fmt.Fprint(out, log[printed:])
				if !strings.HasSuffix(log, "\n") && st.State.Finished() {
					fmt.Fprintln(out)
				}
				printed = len(log)
				changed = true
			}
			lastState = st.State
			if st.State.Finished() {
				return st, nil
			}
			if changed {
				wait = opt.Interval
			} else if wait *= 2; wait > opt.MaxInterval {
				wait = opt.MaxInterval
			}
		}
		opt.Sleep(ctx, wait)
		if ctx.Err() != nil {
			return Status{}, ctx.Err()
		}
	}
}
