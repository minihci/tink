package helper

import (
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// Found is a helper instance, as listed.
type Found struct {
	Project string
	Name    string
	// State is the instance's own state: "Running", "Stopped", ...
	State  string
	Config map[string]string
}

// Label is how the helper is named in messages.
func (f Found) Label() string { return f.Project + "/" + f.Name }

// Find lists the helper instances on the server, in every project: the instances marked with MarkerKey. There should be one; the
// caller decides what to do about none or several.
func Find(s incus.InstanceServer) ([]Found, error) {
	all, err := s.GetInstancesAllProjects(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("listing instances to find the helper: %w", err)
	}
	var out []Found
	for _, inst := range all {
		if _, ok := inst.Config[MarkerKey]; ok {
			out = append(out, Found{Project: inst.Project, Name: inst.Name, State: inst.Status, Config: inst.Config})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label() < out[j].Label() })
	return out, nil
}

// Health is how well the helper is, in the three states a monitor acts on.
type Health int

const (
	// Healthy: running, recently heard from, nothing skipped, nothing failing.
	Healthy Health = 0
	// Degraded: running and heard from, but not doing everything it should, or saying nothing useful.
	Degraded Health = 1
	// Down: stopped, not found, or silent for longer than it should be. It is the state the helper cannot report itself.
	Down Health = 2
)

func (h Health) String() string {
	switch h {
	case Healthy:
		return "healthy"
	case Degraded:
		return "degraded"
	default:
		return "down"
	}
}

// Report is the judgement of one helper, with the lines that justify it.
type Report struct {
	Health  Health
	Found   Found
	Status  *Status
	Reasons []string
}

// Summary is the one line `status --check` prints.
func (r Report) Summary() string {
	if len(r.Reasons) == 0 {
		return fmt.Sprintf("%s: %s", r.Health, r.Found.Label())
	}
	return fmt.Sprintf("%s: %s: %s", r.Health, r.Found.Label(), strings.Join(r.Reasons, "; "))
}

// Evaluate judges a helper from what it published and the state of its instance. A helper that has stopped or has gone quiet is
// Down; one that is running and speaking but skipping volumes or failing copies is Degraded; a running one that has published
// nothing yet, or something unreadable, is Degraded (it may be new, or old).
func Evaluate(f Found, now time.Time) Report {
	r := Report{Found: f}
	down := func(format string, args ...any) {
		r.Health = Down
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
	}
	degrade := func(format string, args ...any) {
		if r.Health < Degraded {
			r.Health = Degraded
		}
		r.Reasons = append(r.Reasons, fmt.Sprintf(format, args...))
	}

	text, published := f.Config[StatusKey]
	var st Status
	if published {
		var err error
		if st, err = Parse(text); err != nil {
			degrade("cannot read its status: %v", err)
			published = false
		} else {
			r.Status = &st
		}
	}

	if f.State != "Running" {
		down("the instance is %s", strings.ToLower(f.State))
		if r.Status != nil && !st.Tick.IsZero() {
			r.Reasons[len(r.Reasons)-1] += fmt.Sprintf(" (last heard from %s ago)", age(now.Sub(st.Tick)))
		}
		return r
	}
	if r.Status == nil {
		if !published && len(r.Reasons) == 0 {
			degrade("it is running but has not published a status document (it has just started, or it is a helper that does not publish one)")
		}
		return r
	}

	if hb := time.Duration(st.HeartbeatSeconds) * time.Second; hb > 0 && !st.Tick.IsZero() {
		if silent := now.Sub(st.Tick); silent > hb*staleNumerator/staleDenominator {
			down("stale: last heard from %s ago, and it publishes at least every %s", age(silent), age(hb))
			return r
		}
	}
	if len(st.Skipped) > 0 {
		degrade("%d volume(s) skipped: %s", len(st.Skipped), skipList(st.Skipped))
	}
	if len(st.Failing) > 0 {
		degrade("%d copy(ies) failing: %s", len(st.Failing), failList(st.Failing))
	}
	if st.Ingress != nil && !st.Ingress.OK {
		degrade("the last ingress reconcile failed")
	}
	return r
}

func skipList(s []Skip) string {
	parts := make([]string, len(s))
	for i, x := range s {
		parts[i] = fmt.Sprintf("%s (%s)", x.Volume, x.Reason)
	}
	return strings.Join(parts, ", ")
}

func failList(f []Failing) string {
	parts := make([]string, len(f))
	for i, x := range f {
		parts[i] = fmt.Sprintf("%s -> %s (%dx)", x.Volume, x.Target, x.Count)
	}
	return strings.Join(parts, ", ")
}

// age is a duration short enough to read in a line: "42s", "7m", "3h", "2d".
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
