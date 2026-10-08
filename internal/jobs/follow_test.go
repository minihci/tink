package jobs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFollowPrintsOnlyWhatIsNewAndReturnsTheFinalStatus(t *testing.T) {
	m := newMemFS()
	s := Store{FS: m}
	id, _ := s.Enqueue(Request{Kind: "backup-run"}, t0)

	step := 0
	var slept []time.Duration
	opt := FollowOptions{Sleep: func(_ context.Context, d time.Duration) {
		slept = append(slept, d)
		step++
		switch step {
		case 1:
			m.files[id+"/status.json"] = []byte(`{"proto":1,"id":"` + id + `","state":"running"}`)
			m.files[id+"/log"] = []byte("copy 1\n")
		case 2: // nothing changes
		case 3: // nothing changes
		case 4:
			m.files[id+"/log"] = []byte("copy 1\ncopy 2\n")
			m.files[id+"/status.json"] = []byte(`{"proto":1,"id":"` + id + `","state":"succeeded"}`)
		}
	}}
	var out bytes.Buffer
	st, err := Follow(context.Background(), s, id, &out, opt)
	if err != nil || st.State != Succeeded {
		t.Fatalf("%+v %v", st, err)
	}
	if out.String() != "copy 1\ncopy 2\n" {
		t.Errorf("each line once, in order: %q", out.String())
	}
	// 2s on a change, then backing off while nothing changes, and back to 2s after one
	want := []time.Duration{2 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("polls: %v", slept)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Errorf("wait %d: %v, want %v (all: %v)", i, slept[i], want[i], slept)
		}
	}
}

func TestFollowBacksOffToItsCeiling(t *testing.T) {
	m := newMemFS()
	s := Store{FS: m}
	id, _ := s.Enqueue(Request{Kind: "backup-run"}, t0)
	n := 0
	var last time.Duration
	opt := FollowOptions{Sleep: func(_ context.Context, d time.Duration) {
		last = d
		if n++; n == 20 {
			m.files[id+"/status.json"] = []byte(`{"proto":1,"id":"` + id + `","state":"failed","error":"x"}`)
		}
	}}
	st, err := Follow(context.Background(), s, id, &bytes.Buffer{}, opt)
	if err != nil || st.State != Failed || last != 30*time.Second {
		t.Fatalf("%+v %v last wait %v", st, err, last)
	}
}

func TestDetachingLeavesTheJobAlone(t *testing.T) {
	m := newMemFS()
	s := Store{FS: m}
	id, _ := s.Enqueue(Request{Kind: "backup-run"}, t0)
	ctx, cancel := context.WithCancel(context.Background())
	opt := FollowOptions{Sleep: func(context.Context, time.Duration) { cancel() }}
	if _, err := Follow(ctx, s, id, &bytes.Buffer{}, opt); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if _, ok := m.files[id+"/cancel"]; ok {
		t.Fatal("detaching must not cancel the job")
	}
}

func TestFollowRidesOutAGapAndGivesUpAfterPatience(t *testing.T) {
	m := newMemFS()
	s := Store{FS: m}
	id, _ := s.Enqueue(Request{Kind: "backup-run"}, t0)
	now := t0
	broken := &flakyFS{memFS: m}
	fs := Store{FS: broken}
	round := 0
	opt := FollowOptions{Patience: time.Minute, Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) {
		now = now.Add(d)
		round++
		switch round {
		case 1:
			broken.down = true
		case 3:
			broken.down = false
		case 4:
			m.files[id+"/status.json"] = []byte(`{"proto":1,"id":"` + id + `","state":"succeeded"}`)
		}
	}}
	var out bytes.Buffer
	st, err := Follow(context.Background(), fs, id, &out, opt)
	if err != nil || st.State != Succeeded {
		t.Fatalf("a short gap must be ridden out: %+v %v", st, err)
	}
	if strings.Count(out.String(), "cannot reach") != 1 {
		t.Errorf("say so once, not on every poll: %q", out.String())
	}

	// and when it does not come back
	m2 := newMemFS()
	id2, _ := (Store{FS: m2}).Enqueue(Request{Kind: "backup-run"}, t0)
	b2 := &flakyFS{memFS: m2}
	now = t0
	opt2 := FollowOptions{Patience: time.Minute, Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) {
		now = now.Add(d)
		b2.down = true
	}}
	if _, err := Follow(context.Background(), Store{FS: b2}, id2, &bytes.Buffer{}, opt2); err == nil || !strings.Contains(err.Error(), "lost contact") {
		t.Fatalf("%v", err)
	}
}

func TestFollowingAJobThatIsNotThereFailsAtOnce(t *testing.T) {
	if _, err := Follow(context.Background(), Store{FS: newMemFS()}, "20260101T000000Z-aaaaaa", &bytes.Buffer{}, FollowOptions{}); err == nil {
		t.Fatal("no such job")
	}
}

type flakyFS struct {
	*memFS
	down bool
}

func (f *flakyFS) ReadFile(n string) ([]byte, error) {
	if f.down {
		return nil, errors.New("connection refused")
	}
	return f.memFS.ReadFile(n)
}

func (f *flakyFS) Exists(n string) (bool, error) {
	if f.down {
		return false, errors.New("connection refused")
	}
	return f.memFS.Exists(n)
}
