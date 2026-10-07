package daemon

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/jobs"
)

// Live is what the workers learn that the status document reports: the scheduler's view of the volumes and the ingress pass's last
// result. The workers write it as they go; the status worker reads it and publishes only what changed.
type Live struct {
	mu      sync.Mutex
	known   bool // the scheduler has completed a pass, so skipped and failing are facts and not "not looked yet"
	skipped []helper.Skip
	failing []helper.Failing
	ingress *helper.IngressState
}

func (l *Live) setBackup(skipped []helper.Skip, failing []helper.Failing) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.skipped, l.failing, l.known = skipped, failing, true
}

// backupKnown says whether the scheduler has looked at the volumes at least once.
func (l *Live) backupKnown() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.known
}

func (l *Live) setIngress(ok bool, warnings int, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ingress = &helper.IngressState{OK: ok, At: at.UTC(), Warnings: warnings}
}

func (l *Live) snapshot() (skipped []helper.Skip, failing []helper.Failing, ingress *helper.IngressState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ingress != nil {
		i := *l.ingress
		ingress = &i
	}
	return append([]helper.Skip(nil), l.skipped...), append([]helper.Failing(nil), l.failing...), ingress
}

// maxReason keeps a skip reason to a line. The reasons that come from tink's own parsing of a policy are short; this is a bound,
// not a rule.
const maxReason = 160

// skipsFrom turns what a look at the volumes could not do into the reasons the status document carries. A policy that does not
// parse is explained by tink's own message (it names a field or a protocol, never a credential). Anything that came from Incus
// itself gets a fixed phrase: its error text can echo a credential, and a config value is the wrong place for one.
func skipsFrom(discover, assess map[string]error) []helper.Skip {
	var out []helper.Skip
	for what, err := range discover {
		reason := truncate(err.Error(), maxReason)
		if strings.HasPrefix(what, "pool ") {
			reason = "the pool could not be listed"
		}
		out = append(out, helper.Skip{Volume: what, Reason: reason})
	}
	for what := range assess {
		reason := "the volume could not be read"
		if strings.Contains(what, " -> ") {
			reason = "the copy's schedule could not be read"
		}
		out = append(out, helper.Skip{Volume: what, Reason: reason})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Volume < out[j].Volume })
	return out
}

func failingFrom(in []backuprun.FailingCopy) []helper.Failing {
	out := make([]helper.Failing, len(in))
	for i, f := range in {
		out[i] = helper.Failing{Volume: f.Volume, Target: f.Target, Count: f.Count, Since: f.Since.UTC()}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Volume != out[j].Volume {
			return out[i].Volume < out[j].Volume
		}
		return out[i].Target < out[j].Target
	})
	return out
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// StatusOptions is what publishing the status document needs.
type StatusOptions struct {
	Publisher *helper.Publisher
	// Interval is how often the document is built and offered to the publisher, which writes it only when it changed or the
	// heartbeat is due (default 10 seconds: offering is free, so a change is noticed quickly).
	Interval time.Duration
	Version  string
	Zone     *time.Location
	// Store is the job directory, for the last finished job; the zero Store means this daemon runs no jobs.
	Store jobs.Store
	// Remotes reads the Incus remotes this process can reach, for the status document. Nil leaves them out of it (so a test of something else
	// does not depend on the machine's own Incus configuration); a read that fails does too, since "none" would be a claim.
	Remotes func() ([]helper.Remote, error)
	// WaitForBackup holds the first publication until the scheduler has completed a pass. Before that the document would say "nothing
	// skipped, nothing failing" about volumes nobody has looked at, which reads as healthy: a helper that cannot reach Incus at all (a
	// revoked certificate, a proxy not up yet) must publish nothing, not a clean bill.
	WaitForBackup bool
}

func (o StatusOptions) interval() time.Duration {
	if o.Interval > 0 {
		return o.Interval
	}
	return 10 * time.Second
}

// buildStatus is the document as it stands now.
func buildStatus(o StatusOptions, live *Live, started, now time.Time) helper.Status {
	skipped, failing, ingress := live.snapshot()
	s := helper.Status{
		Version:     o.Version,
		JobProto:    jobs.Proto,
		PolicyProto: backupmeta.PolicyProto,
		Started:     started.UTC(),
		Skipped:     skipped,
		Failing:     failing,
		Ingress:     ingress,
	}
	zone := o.Zone
	if zone == nil {
		zone = now.Location()
	}
	s.TZ = zoneName(zone, now)
	if o.Remotes != nil {
		if remotes, err := o.Remotes(); err == nil {
			s.Remotes = remotes
		}
	}
	if o.Store.Dir != "" {
		s.LastJob = lastJob(o.Store)
		s.Draining = o.Store.Draining()
		if running, queued, err := o.Store.Counts(); err == nil {
			s.Running, s.Queued = running, queued
		}
	}
	return s
}

// zoneName names a time zone so a person can read it. A zone loaded by name says it ("America/Denver"); the process's local zone
// only says "Local", so it is named by $TZ when that is set, and by its current abbreviation when not.
func zoneName(z *time.Location, now time.Time) string {
	if name := z.String(); name != "Local" {
		return name
	}
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	abbr, _ := now.In(z).Zone()
	return "Local (" + abbr + ")"
}

// lastJob is the newest finished job, or nil when none has finished (or the directory cannot be read: a status document that
// leaves a field out is better than none).
func lastJob(store jobs.Store) *helper.LastJob {
	list, err := store.List()
	if err != nil {
		return nil
	}
	for i := len(list) - 1; i >= 0; i-- {
		if list[i].State.Finished() {
			return &helper.LastJob{ID: list[i].ID, State: string(list[i].State), Finished: list[i].Finished.UTC()}
		}
	}
	return nil
}

// runStatus offers the status document to the publisher until ctx is done. A write that fails is logged once, when it starts
// failing and when it recovers, not every interval.
func runStatus(ctx context.Context, out io.Writer, o StatusOptions, live *Live, started time.Time) error {
	var state SchedulerState
	publish := func() {
		if o.WaitForBackup && !live.backupKnown() {
			return
		}
		now := time.Now()
		state.begin()
		defer state.end(func(format string, args ...any) { fmt.Fprintf(out, "tink daemon: "+format+"\n", args...) })
		if _, err := o.Publisher.Publish(buildStatus(o, live, started, now)); err != nil {
			state.note("status", fmt.Sprintf("publishing the status document: %v", err), func(format string, args ...any) {
				fmt.Fprintf(out, "tink daemon: "+format+"\n", args...)
			})
		}
	}
	publish()
	t := time.NewTicker(o.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			publish()
		}
	}
}
