package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHeartbeat(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "jobs")} // does not exist yet: the first beat creates it
	if _, err := s.LastBeat(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no heartbeat yet: %v", err)
	}
	if err := s.Beat(Heartbeat{Time: t0, PID: 42, Version: "v1", Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Beat(Heartbeat{Time: t0.Add(time.Minute), PID: 42, Version: "v1", Zone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	hb, err := s.LastBeat()
	if err != nil || !hb.Time.Equal(t0.Add(time.Minute)) || hb.PID != 42 || hb.Zone != "UTC" {
		t.Errorf("%v %+v", err, hb)
	}
	// the heartbeat file is not a job, and does not get in the way of listing or pruning
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("the heartbeat must not be listed as a job: %v", list)
	}
	e := &Executor{Store: s, Now: func() time.Time { return t0.Add(100 * time.Hour) }}
	if err := e.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LastBeat(); err != nil {
		t.Errorf("pruning must leave the heartbeat alone: %v", err)
	}
	// a beat leaves no temp file behind
	entries, _ := os.ReadDir(s.Dir)
	for _, en := range entries {
		if en.Name() != "heartbeat" {
			t.Errorf("stray file %s", en.Name())
		}
	}
}
