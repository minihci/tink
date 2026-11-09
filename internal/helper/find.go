package helper

import (
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/ownership"
)

// Found is a helper instance, as listed.
type Found struct {
	Project string
	Name    string
	// State is the instance's own state: "Running", "Stopped", ...
	State  string
	Config map[string]string
	// StatusVolume is "POOL/VOLUME" when the instance points at a volume that holds its status document (StatusVolumeKey), else "". With
	// it set, Config[StatusKey] is what Find read from that volume, and never what the instance itself carries.
	StatusVolume string
	// StatusProblem is why that volume could not be read, when it could not. Find does not fail the listing for it: Evaluate says it,
	// and the helper is degraded, neither healthy nor merely "not yet heard from".
	StatusProblem string
	// Devices are the instance's own devices (not its profiles'): which way it reaches the host's API is one of them (see APIVia).
	Devices map[string]map[string]string
}

// Label is how the helper is named in messages.
func (f Found) Label() string { return f.Project + "/" + f.Name }

// Find lists the helper instances on the server, in every project: the instances marked with MarkerKey. There should be one; the
// caller decides what to do about none or several. A helper whose instance points at a status volume (StatusVolumeKey) has its status
// document read from there and placed in Config[StatusKey], so the rest of this package reads it the same wherever it was kept; a
// volume that cannot be read is carried on the result (StatusProblem), not returned as an error.
func Find(s incus.InstanceServer) ([]Found, error) {
	all, err := s.GetInstancesAllProjects(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("listing instances to find the helper: %w", err)
	}
	var out []Found
	for _, inst := range all {
		if _, ok := inst.Config[MarkerKey]; ok {
			f := Found{Project: inst.Project, Name: inst.Name, State: inst.Status, Config: inst.Config, Devices: inst.Devices}
			withStatusVolume(s, &f)
			out = append(out, f)
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

// Report is the judgement of one helper, with the findings that justify it. Degraded is what warning findings add up to and Down what error findings do
// (HealthOf), so the health and the findings cannot disagree.
type Report struct {
	Health   Health
	Found    Found
	Status   *Status
	Findings []finding.Finding
}

// HealthOf is the health a list of findings adds up to: Down when any is an error, else Degraded when any is a warning, else Healthy. A note says
// something is worth reading and is not a problem.
func HealthOf(fs []finding.Finding) Health {
	switch {
	case finding.HasErrors(fs):
		return Down
	case finding.Count(fs, finding.Warning) > 0:
		return Degraded
	}
	return Healthy
}

// add puts findings on the report and raises its health to what they say.
func (r *Report) add(fs ...finding.Finding) {
	r.Findings = append(r.Findings, fs...)
	if h := HealthOf(fs); h > r.Health {
		r.Health = h
	}
}

// Details are the findings' details, one line each, in the order they were raised: what `status --check` joins and what a person reads. The reasons
// are not in them (a finding's reason is its key, and its detail says it in words).
func (r Report) Details() []string {
	out := make([]string, len(r.Findings))
	for i, f := range r.Findings {
		out[i] = f.Detail
	}
	return out
}

// Summary is the one line `status --check` prints.
func (r Report) Summary() string {
	if len(r.Findings) == 0 {
		return fmt.Sprintf("%s: %s", r.Health, r.Found.Label())
	}
	return fmt.Sprintf("%s: %s: %s", r.Health, r.Found.Label(), strings.Join(r.Details(), "; "))
}

// Subject is how a finding about the helper's instance names it, in tink's form.
func (f Found) Subject() string {
	return ownership.Address(ownership.KindInstance, f.Project, "", f.Name)
}

// Evaluate judges a helper from what it published and the state of its instance. It is two parts. The outside part needs the server's answer and
// is here: whether the instance is running, whether the status volume could be read, and whether the document has gone quiet for longer than it
// said it would (a document cannot say that of itself). The rest is what the document says, and is JudgeStatus's: it is a function of the document
// and the time, so anything that holds the document can run it.
//
// A helper that has stopped or has gone quiet is Down; one that is running and speaking but skipping volumes or failing copies is Degraded; a
// running one that has published nothing yet, or something unreadable, is Degraded (it may be new, or old); and so is one whose status volume could
// not be read, with that said as the reason.
func Evaluate(f Found, now time.Time) Report {
	r := Report{Found: f}
	subject := f.Subject()

	// A status volume that could not be read comes first: whatever the instance's state, the document cannot be judged, and saying
	// nothing about it would read as "no status yet" or, worse, as well.
	if f.StatusProblem != "" {
		r.add(finding.Warnf(ReasonStatusVolumeUnreadable, subject, "cannot read its status volume %s: %s", f.StatusVolume, f.StatusProblem))
	}

	text, published := f.Config[StatusKey]
	var st Status
	if published {
		var err error
		if st, err = Parse(text); err != nil {
			r.add(finding.Warnf(ReasonStatusUnreadable, subject, "cannot read its status: %v", err))
			published = false
		} else {
			r.Status = &st
		}
	}

	if f.State != "Running" {
		detail := fmt.Sprintf("the instance is %s", strings.ToLower(f.State))
		if r.Status != nil && !st.WrittenAt.IsZero() {
			detail += fmt.Sprintf(" (last heard from %s ago)", age(now.Sub(st.WrittenAt.Time)))
		}
		r.add(finding.Errorf(ReasonNotRunning, subject, "%s", detail))
		return r
	}
	if r.Status == nil {
		if !published && len(r.Findings) == 0 {
			r.add(finding.Warnf(ReasonNoStatus, subject, "it is running but has not published a status document (it has just started, or it is a helper that does not publish one)"))
		}
		return r
	}

	// Silent for longer than it said it would be: only someone looking from outside, at a time of their own, can say that. A document that does
	// not say when it was written or how often is JudgeStatus's to call degraded, and is not called stale here.
	if hb := st.Heartbeat.D(); !st.WrittenAt.IsZero() && hb > 0 {
		if silent := now.Sub(st.WrittenAt.Time); silent > hb*staleNumerator/staleDenominator {
			r.add(finding.Errorf(ReasonStale, subject, "stale: last heard from %s ago, and it publishes at least every %s", age(silent), age(hb)))
			return r
		}
	}

	// The document does not know which instance it came from: a finding about the document itself is about the helper's instance.
	_, fs := JudgeStatus(st, now)
	for i := range fs {
		if fs[i].Subject == "" {
			fs[i].Subject = subject
		}
	}
	r.add(fs...)
	return r
}

// CheckTrust adds what the host's trust store says about a helper that enrolled with a certificate of its own (install records the
// address it dialled on the instance): if the certificate named TrustName is not there, the helper cannot act, whatever its last
// document says, and the document stays fresh for 2.5 heartbeats after it is revoked. Pass the certificates as listed; a helper that
// uses no certificate of its own (the socket fallback) has nothing to check.
func (r *Report) CheckTrust(certs []api.Certificate) {
	if r.Found.Config[MarkerKey+".remote"] == "" {
		return
	}
	for _, c := range certs {
		if c.Name == TrustName {
			return
		}
	}
	r.add(finding.Errorf(ReasonCertificateNotTrusted, r.Found.Subject(), "its certificate (%s) is not in the host's trust store: it was revoked, or never enrolled (tink helper install --reissue enrols it again)", TrustName))
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
