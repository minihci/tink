// Package jobs is the helper's work queue, kept as a directory so that anything able to put files in it (the
// scheduler in the same process, or a client pushing files through the Incus file API) can start work without a
// listening port or a daemon API of its own.
//
//	<dir>/<id>/request.json   what to do (a backup job works from the copy policies on the volumes)
//	<dir>/<id>/READY          created LAST: nothing in a job directory is read before it exists
//	<dir>/<id>/status.json    written by the executor, atomically
//	<dir>/<id>/log            bounded, scrubbed of secrets
//	<dir>/<id>/cancel         created by a client to ask the job to stop
//
// READY exists because the Incus file API writes a file in place: a reader can see a half-written file. A job is read
// only once READY says the writer has finished.
package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Proto is the version of this directory protocol: what a client and the helper must agree on. It is a small integer
// and not the tink version, so a laptop and a server on different tink releases still work together until the
// protocol itself changes.
const Proto = 1

// Origins of a request.
const (
	OriginSchedule = "schedule"
	OriginTrigger  = "trigger"
)

// State of a job.
type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
	Cancelled State = "cancelled"
)

// Finished reports whether the state is final.
func (s State) Finished() bool { return s == Succeeded || s == Failed || s == Cancelled }

// Request is request.json.
type Request struct {
	Proto   int       `json:"proto"`
	Kind    string    `json:"kind"`
	Origin  string    `json:"origin"`
	Created time.Time `json:"created"`
	// Args are specific to Kind.
	Args json.RawMessage `json:"args,omitempty"`
}

// Status is status.json.
type Status struct {
	Proto    int       `json:"proto"`
	ID       string    `json:"id"`
	Kind     string    `json:"kind,omitempty"`
	Origin   string    `json:"origin,omitempty"`
	State    State     `json:"state"`
	Created  time.Time `json:"created,omitzero"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`
	// Error is why a failed job failed, scrubbed of secrets.
	Error string `json:"error,omitempty"`
	// Summary is what the job did, specific to Kind.
	Summary json.RawMessage `json:"summary,omitempty"`
}

// Job is a request found in the directory, with what the executor needs to run it.
type Job struct {
	ID      string
	Dir     string
	Request Request
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidID reports whether id can name a job directory. Anything else in the directory is not a job.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// NewID makes a sortable job id: the time, then a random suffix so two made in one second differ.
func NewID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
}

// Store is a jobs directory.
type Store struct{ Dir string }

func (s Store) jobDir(id string) string { return filepath.Join(s.Dir, id) }

// Enqueue creates a job: request.json, then READY, last. It is how the scheduler starts work; a remote client does the same
// with the file API. It returns the new job's id.
func (s Store) Enqueue(req Request, now time.Time) (string, error) {
	req.Proto = Proto
	if req.Created.IsZero() {
		req.Created = now.UTC()
	}
	if req.Kind == "" {
		return "", errors.New("a job needs a kind")
	}
	id := NewID(now)
	dir := s.jobDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "request.json"), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	if beforeREADY != nil {
		beforeREADY(dir)
	}
	if err := os.WriteFile(filepath.Join(dir, "READY"), nil, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return id, nil
}

// beforeREADY is a test seam: Enqueue calls it with the job's directory after request.json is written and before READY is, which is the
// moment at which a reader must not be able to see the job, and the request must already be complete.
var beforeREADY func(dir string)

// Cancel asks a job to stop. It takes effect between the job's steps; an operation already in flight finishes.
func (s Store) Cancel(id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a job id", id)
	}
	if _, err := os.Stat(filepath.Join(s.jobDir(id), "READY")); err != nil {
		return fmt.Errorf("no such job %q", id)
	}
	return os.WriteFile(filepath.Join(s.jobDir(id), "cancel"), nil, 0o600)
}

// Status reads a job's status. A job that is READY and has no status yet is queued.
func (s Store) Status(id string) (Status, error) {
	if !ValidID(id) {
		return Status{}, fmt.Errorf("%q is not a job id", id)
	}
	dir := s.jobDir(id)
	if _, err := os.Stat(filepath.Join(dir, "READY")); err != nil {
		return Status{}, fmt.Errorf("no such job %q", id)
	}
	b, err := os.ReadFile(filepath.Join(dir, "status.json"))
	if errors.Is(err, os.ErrNotExist) {
		st := Status{Proto: Proto, ID: id, State: Queued}
		if rb, err := os.ReadFile(filepath.Join(dir, "request.json")); err == nil {
			var r Request
			if json.Unmarshal(rb, &r) == nil {
				st.Kind, st.Origin, st.Created = r.Kind, r.Origin, r.Created
			}
		}
		return st, nil
	}
	if err != nil {
		return Status{}, err
	}
	var st Status
	if err := json.Unmarshal(b, &st); err != nil {
		return Status{}, fmt.Errorf("job %s: status.json: %w", id, err)
	}
	return st, nil
}

// Log reads a job's log.
func (s Store) Log(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%q is not a job id", id)
	}
	b, err := os.ReadFile(filepath.Join(s.jobDir(id), "log"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// List returns every READY job's status, oldest first.
func (s Store) List() ([]Status, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		st, err := s.Status(e.Name())
		if err != nil {
			continue // not READY yet, or unreadable: not a job to show
		}
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// DrainFile is the file whose presence makes the daemon drain: the scheduler queues nothing new and the executor starts nothing, while a
// job already running finishes. `tink helper upgrade` creates it (through the instance file API, since the directory is on the helper's
// own volume), waits for the running work to end, replaces the helper, and removes it.
const DrainFile = "DRAIN"

// Draining reports whether the directory is being drained.
func (s Store) Draining() bool {
	_, err := os.Stat(filepath.Join(s.Dir, DrainFile))
	return err == nil
}

// Counts is how many jobs are running and how many are queued, which is what a drain waits on.
func (s Store) Counts() (running, queued int, err error) {
	list, err := s.List()
	if err != nil {
		return 0, 0, err
	}
	for _, st := range list {
		switch st.State {
		case Running:
			running++
		case Queued:
			queued++
		}
	}
	return running, queued, nil
}

// Pending reports whether a job of this kind is already queued or running, whoever queued it: a scheduler uses it so
// it never queues the same work twice, and work an operator has just queued is not queued again behind it.
func (s Store) Pending(kind string) (bool, error) {
	jobs, err := s.List()
	if err != nil {
		return false, err
	}
	for _, st := range jobs {
		if !st.State.Finished() && st.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}

func (s Store) readRequest(id string) (Request, error) {
	b, err := os.ReadFile(filepath.Join(s.jobDir(id), "request.json"))
	if err != nil {
		return Request{}, err
	}
	var r Request
	if err := json.Unmarshal(b, &r); err != nil {
		return Request{}, err
	}
	// A request made by an earlier tink could carry a stack ("entries", with its files in a bundle/ directory) to run for that job only.
	// That is gone: jobs work from the copy policies on the volumes. Running such a request as an ordinary job would copy something other
	// than what was asked for, so it is refused.
	var earlier struct {
		Entries []string `json:"entries"`
	}
	if json.Unmarshal(b, &earlier) == nil && len(earlier.Entries) > 0 {
		return Request{}, errors.New("it carries a stack (a bundle of stack files), which tink no longer runs: jobs work from the copy policies on the volumes " +
			"(`tink backup run -f FILE` runs one stack's copies)")
	}
	return r, nil
}

func (s Store) writeStatus(st Status) error {
	st.Proto = Proto
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := s.jobDir(st.ID)
	tmp, err := os.CreateTemp(dir, ".status-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	// rename is atomic on the same filesystem: a reader sees the old status or the new, never half
	return os.Rename(name, filepath.Join(dir, "status.json"))
}
