// Package jobs is the helper's work queue, kept as a directory so that anything able to put files in it (the
// scheduler in the same process, or a client pushing files through the Incus file API) can start work without a
// listening port or a daemon API of its own.
//
//	<dir>/<id>/request.json   what to do
//	<dir>/<id>/bundle/        optional: a stack, and every file it reads, for this request only (without one, a backup
//	                          job works from the policies on the volumes)
//	<dir>/<id>/READY          created LAST: nothing in a job directory is read before it exists
//	<dir>/<id>/status.json    written by the executor, atomically
//	<dir>/<id>/log            bounded, scrubbed of secrets
//	<dir>/<id>/cancel         created by a client to ask the job to stop
//
// READY exists because the Incus file API writes a file in place: a reader can see a half-written file, or a
// directory with only some of its files. A job is read only once READY says the writer has finished.
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
	"strings"
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
	// Entries are the stack files inside bundle/ to load, when the request carries a bundle.
	Entries []string `json:"entries,omitempty"`
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
	// BundleDir is the directory of the request's own stack files (it may not exist).
	BundleDir string
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

// CleanBundlePath validates a path inside a bundle and returns it in its cleaned form: relative, with no
// way out of the directory, and not one of the protocol's own names.
func CleanBundlePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("an empty path")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("path %q contains a NUL", p)
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("path %q is absolute", p)
	}
	c := filepath.Clean(filepath.FromSlash(p))
	if c == "." || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q leaves the directory it is delivered in", p)
	}
	return c, nil
}

// writeFiles writes files under dir (creating directories), refusing any path that is not confined.
func writeFiles(dir string, files map[string][]byte) error {
	for name, data := range files {
		c, err := CleanBundlePath(name)
		if err != nil {
			return err
		}
		full := filepath.Join(dir, c)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(full, data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

// Enqueue creates a job: its bundle files, then request.json, then READY, last. It is how the scheduler starts
// work; a remote client does the same with the file API. It returns the new job's id.
func (s Store) Enqueue(req Request, bundle map[string][]byte, now time.Time) (string, error) {
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
	if len(bundle) > 0 {
		if err := writeFiles(filepath.Join(dir, "bundle"), bundle); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
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
	if err := os.WriteFile(filepath.Join(dir, "READY"), nil, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return id, nil
}

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
