// Package helper is what tink knows about its helper instance from the outside: the status document the helper publishes about
// itself, and how to read it back and judge whether the helper is well. It is in a package of its own because the daemon writes
// it and `plan` and `tink helper status` read it, and neither side should need the other's code.
//
// The document lives on the helper's own instance config (docs/helper-design.md, "The status document"), where it can be read with
// one API call, costs no lifecycle event to read, and is still there when the helper has stopped.
package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

const (
	// StatusKey is the instance config key the helper publishes its status document under.
	StatusKey = "user.tink.helper.status"
	// MarkerKey marks an instance as a helper and says which job protocol it speaks. `helper install` sets it, so a helper is
	// found even before it has ever published anything.
	MarkerKey = "user.tink.helper"
	// StatusProto is the version of the document. A reader refuses a newer one rather than guessing.
	StatusProto = 1

	// DefaultHeartbeat is how often the helper writes when nothing has changed.
	DefaultHeartbeat = 10 * time.Minute
	// staleAfter is how many heartbeats may be missed before the helper is called stale: 2.5, so one late write is not an alarm
	// and two missed ones are.
	staleNumerator, staleDenominator = 5, 2
)

// Status is what the helper says about itself. It holds no error text: errors from Incus and its drivers can echo credentials (a
// TrueNAS API key has appeared in one), and a config value is the wrong place for them. Reasons are short and fixed; the detail
// stays in the job's log.
type Status struct {
	Proto int `json:"proto"`
	// Version is the tink version the helper runs.
	Version string `json:"version,omitempty"`
	// JobProto is the job-directory protocol it speaks; PolicyProto is the newest copy-policy protocol it can read. A writer of
	// policies compares against the latter so that it never writes one the helper would silently skip.
	JobProto    int    `json:"job_proto,omitempty"`
	PolicyProto int    `json:"policy_proto,omitempty"`
	TZ          string `json:"tz,omitempty"`

	Started time.Time `json:"started,omitzero"`
	// Tick is when this was written; HeartbeatSeconds is how often it is written when nothing changes, so a reader can tell stale
	// from quiet without knowing the helper's configuration.
	Tick             time.Time `json:"tick,omitzero"`
	HeartbeatSeconds int       `json:"heartbeat_seconds,omitempty"`

	// Skipped are volumes the helper will not copy, and why: a policy of a protocol it does not read, one that does not parse, a
	// pool it cannot list. A volume that stops being copied for one of these must not do it silently.
	Skipped []Skip `json:"skipped,omitempty"`
	// Failing are copies that have failed since their last success.
	Failing []Failing     `json:"failing,omitempty"`
	LastJob *LastJob      `json:"last_job,omitempty"`
	Ingress *IngressState `json:"ingress,omitempty"`

	// Running and Queued are the jobs in the job directory right now, and Draining says the helper is not starting any: an upgrade
	// waits for Running to reach zero.
	Running  int  `json:"running_jobs,omitempty"`
	Queued   int  `json:"queued_jobs,omitempty"`
	Draining bool `json:"draining,omitempty"`

	// Remotes are the Incus servers the helper can reach by name: its own host, and any added with `tink helper remote add`. A copy
	// to a remote backup target is made through one of these, so a policy naming a remote that is not here fails on every attempt.
	// Nil means the helper did not say (an older one, or its configuration could not be read); an empty list says it has none.
	Remotes []Remote `json:"remotes"`
}

// Remote is one entry of the helper's Incus client configuration. It holds no credential: those are on the helper's config volume.
type Remote struct {
	Name    string `json:"name"`
	Addr    string `json:"addr,omitempty"`
	Project string `json:"project,omitempty"`
}

// Skip is a volume the helper leaves alone, and why.
type Skip struct {
	Volume string `json:"volume"`
	Reason string `json:"reason"`
}

// Failing is a copy that is failing: how many attempts in a row, and since when.
type Failing struct {
	Volume string    `json:"volume"`
	Target string    `json:"target"`
	Count  int       `json:"count"`
	Since  time.Time `json:"since,omitzero"`
}

// LastJob is the most recent finished job.
type LastJob struct {
	ID       string    `json:"id"`
	State    string    `json:"state"`
	Finished time.Time `json:"finished,omitzero"`
}

// IngressState is the ingress reconcile's last pass. It is present only when the helper runs the ingress half.
type IngressState struct {
	OK       bool      `json:"ok"`
	At       time.Time `json:"at,omitzero"`
	Warnings int       `json:"warnings,omitempty"`
}

// Encode is the one-line text that is stored.
func Encode(s Status) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// Parse reads a stored document. Fields it does not know are ignored (a newer helper may add some within a protocol); a newer
// protocol is refused.
func Parse(text string) (Status, error) {
	var s Status
	if err := json.Unmarshal([]byte(text), &s); err != nil {
		return Status{}, fmt.Errorf("%s is not a status document: %w", StatusKey, err)
	}
	if s.Proto == 0 {
		return Status{}, fmt.Errorf("%s is not a status document: it has no protocol", StatusKey)
	}
	if s.Proto > StatusProto {
		return Status{}, fmt.Errorf("%s was written for protocol %d; this tink reads %d", StatusKey, s.Proto, StatusProto)
	}
	return s, nil
}

// changeKey is what decides whether a write is worth an event: the document without the things that change every time it is
// written (the tick, and when the ingress pass ran).
func changeKey(s Status) string {
	s.Tick = time.Time{}
	if s.Ingress != nil {
		i := *s.Ingress
		i.At = time.Time{}
		s.Ingress = &i
	}
	text, _ := Encode(s)
	return text
}
