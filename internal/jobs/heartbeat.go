package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// Heartbeat is what the helper's loop writes every tick, so anything that can read the jobs directory (and, in
// time, `plan` and `status`) can tell a live helper from one that stopped, even when its logs are gone. The file
// lives in the jobs directory itself, beside the jobs and ignored as one: it is a file, and jobs are directories.
type Heartbeat struct {
	Time    time.Time `json:"time"`
	PID     int       `json:"pid"`
	Version string    `json:"version,omitempty"`
	// Zone is the time zone schedules are evaluated in.
	Zone string `json:"zone,omitempty"`
}

const heartbeatFile = "heartbeat"

// Beat records that the helper is alive at now.
func (s Store) Beat(h Heartbeat) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".heartbeat-*")
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
	return os.Rename(name, filepath.Join(s.Dir, heartbeatFile))
}

// LastBeat reads the most recent heartbeat; the error satisfies errors.Is(err, os.ErrNotExist) if there has never been one.
func (s Store) LastBeat() (Heartbeat, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir, heartbeatFile))
	if err != nil {
		return Heartbeat{}, err
	}
	var h Heartbeat
	if err := json.Unmarshal(b, &h); err != nil {
		return Heartbeat{}, errors.New("heartbeat: " + err.Error())
	}
	return h, nil
}
