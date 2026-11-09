// Package helper is what tink knows about its helper instance from the outside: the status document the helper publishes about
// itself, and how to read it back and judge whether the helper is well. It is in a package of its own because the daemon writes
// it and `plan` and `tink helper status` read it, and neither side should need the other's code.
//
// The document is a config key (StatusKey), where it can be read with one API call, costs no lifecycle event to read, and is still
// there when the helper has stopped (docs/helper-design.md, "The status document"). It is kept on a custom storage volume that the
// helper's instance points at (statusvol.go), because the permission to write it must not be an edit right on an instance: that also
// covers the instance's devices, and is a route to root. A helper installed before that keeps it on its own instance config, and is
// read as it was.
package helper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/minihci/tink/internal/instant"
	"github.com/minihci/tink/internal/observe"
	"github.com/minihci/tink/internal/ownership"
)

const (
	// StatusKey is the config key the helper publishes its status document under: on its status volume, or, for a helper installed
	// before there was one, on its own instance.
	StatusKey = MarkerKey + ".status"
	// MarkerKey marks an instance as a helper and says which job protocol it speaks. `helper install` sets it, so a helper is
	// found even before it has ever published anything.
	MarkerKey = ownership.OutcomePrefix + "helper"
	// WrittenAtField and HeartbeatField are the document's names for when it was written and how often, as a health reason says them.
	WrittenAtField, HeartbeatField = "written-at", "heartbeat"
	// StatusProto is the version of the document. A reader refuses a newer one rather than guessing, and does nothing special for an older
	// one: it is judged by what it says, and a document that does not say when it was written is never read as healthy (JudgeStatus).
	// 3 replaced the three lists (skipped, failing, reconciles) with one, States.
	StatusProto = 3

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
	JobProto    int    `json:"job-proto,omitempty"`
	PolicyProto int    `json:"policy-proto,omitempty"`
	TZ          string `json:"tz,omitempty"`

	StartedAt instant.Time `json:"started-at,omitzero"`
	// WrittenAt is when this was written; Heartbeat is how often it is written when nothing changes, so a reader can tell stale
	// from quiet without knowing the helper's configuration. A document without a WrittenAt cannot be judged fresh (JudgeStatus).
	WrittenAt instant.Time     `json:"written-at,omitzero"`
	Heartbeat instant.Duration `json:"heartbeat,omitzero"`

	// States is what the daemon found and since when, one record for each thing it keeps or watches (observe.Record): a derived file that is converged
	// or blocked, a volume it will not copy (a policy of a protocol it does not read, one that does not parse, a pool it cannot list), a copy that has
	// failed since its last success. It is the one list a reader iterates: health judges every one of them (JudgeStatus: a skipped volume, a failing copy, a reconcile that is blocked or failing), and `plan`
	// and `tink helper status` read them too. Since is when a state began, not when it was last looked at.
	States  []observe.Record `json:"states,omitempty"`
	LastJob *LastJob         `json:"last-job,omitempty"`
	// Events are the last few things the daemon did, newest last: a file it put back (healed) and an instance it restarted. A fixed set of words and no
	// content, at most observe.MaxEvents.
	Events []observe.Event `json:"events,omitempty"`

	// Running and Queued are the jobs in the job directory right now, and Draining says the helper is not starting any: an upgrade
	// waits for Running to reach zero.
	Running  int  `json:"running-jobs,omitempty"`
	Queued   int  `json:"queued-jobs,omitempty"`
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

// Backup returns the records of the volumes' copies that are in the given state, in the document's order. These are the ones of the volumes, which health judges one finding at a time (JudgeStatus): a
// volume the helper will not copy is a record with the what "backup" and the state skipped (the resource is the volume), and a copy that is failing is one
// with the state failing, its target, the time of its latest failure and how many failures in a row.
func (s Status) Backup(state string) []observe.Record {
	var out []observe.Record
	for _, r := range s.States {
		if r.What == observe.WhatBackup && r.State == state {
			out = append(out, r)
		}
	}
	return out
}

// LastJob is the most recent finished job.
type LastJob struct {
	ID         string       `json:"id"`
	State      string       `json:"state"`
	FinishedAt instant.Time `json:"finished-at,omitzero"`
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
// written (the write time).
func changeKey(s Status) string {
	s.WrittenAt = instant.Time{}
	text, _ := Encode(s)
	return text
}
