package resolve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// The tests here are about a daemon that stops answering: a call that changes it is accepted and never finishes. Each must come back at its deadline
// (a short one, set through opTimeout) rather than hang, and say that the outcome is unknown.

const shortDeadline = 50 * time.Millisecond

// within fails the test, instead of hanging it, when f has not returned by limit.
func within(t *testing.T, limit time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { f(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
		t.Fatalf("still running after %s: the deadline did not end the wait", limit)
	}
}

// wantUnknown checks err is the abandoned-at-the-deadline error of a change: it says what was being done, that the outcome is unknown, and how to find out.
func wantUnknown(t *testing.T, err error, doing string) {
	t.Helper()
	var te *incusapi.TimeoutError
	if !errors.As(err, &te) || !errors.Is(err, incusapi.ErrTimeout) {
		t.Fatalf("err = %v, want an *incusapi.TimeoutError", err)
	}
	for _, want := range []string{doing, "outcome is unknown", "tink plan"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to say %q", err, want)
		}
	}
}

// hangServer never finishes a change. Every method it does not define panics through the nil embedded interface, which is the point: a deadline
// that let the code go on to the next call would show up as one.
type hangServer struct {
	incus.InstanceServer
	release chan struct{}
	mu      sync.Mutex
	calls   []string
}

func newHangServer() *hangServer { return &hangServer{release: make(chan struct{})} }

func (s *hangServer) record(c string) {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	s.mu.Unlock()
}

func (s *hangServer) hang(c string) error { s.record(c); <-s.release; return nil }

func (s *hangServer) UseProject(string) incus.InstanceServer { return s }
func (s *hangServer) CreateProject(api.ProjectsPost) error   { return s.hang("create-project") }
func (s *hangServer) CreateProfile(api.ProfilesPost) error   { return s.hang("create-profile") }
func (s *hangServer) CreateStoragePoolVolume(string, api.StorageVolumesPost) error {
	return s.hang("create-volume")
}
func (s *hangServer) GetProfile(string) (*api.Profile, string, error) {
	return &api.Profile{}, "", nil
}
func (s *hangServer) UpdateProfile(string, api.ProfilePut, string) error {
	return s.hang("update-profile")
}
func (s *hangServer) GetProject(string) (*api.Project, string, error) {
	s.record("get-project")
	<-s.release
	return nil, "", errors.New("released")
}

type blockedOp struct {
	incus.Operation
	release chan struct{}
}

func (o blockedOp) Wait() error { <-o.release; return nil }
func (o blockedOp) WaitContext(ctx context.Context) error {
	select {
	case <-o.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (o blockedOp) Get() api.Operation { return api.Operation{Metadata: map[string]any{}} }

func (s *hangServer) UpdateInstanceState(_ string, p api.InstanceStatePut, _ string) (incus.Operation, error) {
	s.record("state-" + p.Action)
	return blockedOp{release: s.release}, nil
}

func (s *hangServer) RebuildInstance(string, api.InstanceRebuildPost) (incus.Operation, error) {
	s.record("rebuild")
	return blockedOp{release: s.release}, nil
}

func (s *hangServer) GetInstance(string) (*api.Instance, string, error) {
	return &api.Instance{ExpandedDevices: map[string]map[string]string{
		"data": {"type": "disk", "pool": "default", "source": "vol"},
	}}, "", nil
}

func (s *hangServer) CreateStoragePoolVolumeSnapshot(string, string, string, api.StorageVolumeSnapshotsPost) (incus.Operation, error) {
	s.record("snapshot")
	return blockedOp{release: s.release}, nil
}

func TestCreateOneReturnsAtTheDeadlineWhenTheServerNeverAnswers(t *testing.T) {
	for _, r := range []Resource{
		{Kind: KindProject, Name: "p"},
		{Kind: KindProfile, Name: "pr"},
		{Kind: KindStorageVolume, Name: "v"},
	} {
		t.Run(string(r.Kind), func(t *testing.T) {
			s := newHangServer()
			defer close(s.release)
			var err error
			within(t, 5*time.Second, func() { err = createOne(s, r, volumeEnv{opTimeout: shortDeadline}) })
			wantUnknown(t, err, "creating "+string(r.Kind)+"/"+r.Name)
		})
	}
}

func TestUpdateOneReturnsAtTheDeadlineWhenTheServerNeverAnswers(t *testing.T) {
	s := newHangServer()
	defer close(s.release)
	var err error
	within(t, 5*time.Second, func() {
		err = updateOne(s, Resource{Kind: KindProfile, Name: "pr", Config: map[string]string{"a": "b"}}, volumeEnv{opTimeout: shortDeadline})
	})
	wantUnknown(t, err, "updating profile/pr")
}

// Planning only reads, so a read the server never answers is abandoned too. Its error does not claim an unknown outcome: nothing was being changed.
func TestPlanReturnsAtTheDeadlineWhenAReadNeverAnswers(t *testing.T) {
	s := newHangServer()
	defer close(s.release)
	var err error
	within(t, 5*time.Second, func() {
		_, err = PlanWithOptions(s, []Resource{{Kind: KindProject, Name: "p"}}, PlanOptions{Offline: true, opTimeout: shortDeadline})
	})
	if !errors.Is(err, incusapi.ErrTimeout) {
		t.Fatalf("err = %v, want it to wrap incusapi.ErrTimeout", err)
	}
	var te *incusapi.TimeoutError
	if errors.As(err, &te) || strings.Contains(err.Error(), "outcome is unknown") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("err = %q: a read has no unknown outcome, and must say nothing was changed", err)
	}
}

// A guest exec has a deadline of its own and is not cut short by the one on the server's other calls.
func TestPlanDoesNotBoundAGuestExec(t *testing.T) {
	if !boundsPlan(Resource{Kind: KindProject}) || boundsPlan(Resource{Kind: KindExec}) || boundsPlan(Resource{Kind: KindIncus}) {
		t.Error("planning a project is bounded; an exec (own deadline) and an incus command (a local process) are not")
	}
}

// A push the server never answered is not a push that failed: the instance is not started on top of it.
func TestCreateInOrderStopsAtAnAbandonedPush(t *testing.T) {
	var calls []string
	rec := func(c string) { calls = append(calls, c) }
	timeout := &incusapi.TimeoutError{What: "placing file/a", After: time.Second}
	_, err := createInOrder(instanceSteps{
		create: func() error { rec("create"); return nil },
		config: func() error { rec("config"); return nil },
		place:  func(f Resource) error { rec("place-" + f.Name); return timeout },
		start:  func() error { rec("start"); return nil },
	}, []Resource{{Name: "a"}, {Name: "b"}})
	if !errors.Is(err, incusapi.ErrTimeout) {
		t.Fatalf("err = %v, want the timeout", err)
	}
	if got := strings.Join(calls, " "); got != "create config place-a" {
		t.Errorf("calls = %q, want it to stop at the push that was abandoned", got)
	}
	// An ordinary failed push is still skipped, as it always was.
	calls = nil
	placed, err := createInOrder(instanceSteps{
		create: func() error { return nil },
		config: func() error { return nil },
		place: func(f Resource) error {
			if f.Name == "a" {
				return errors.New("refused")
			}
			return nil
		},
		start: func() error { rec("start"); return nil },
	}, []Resource{{Name: "a"}, {Name: "b"}})
	if err != nil || len(placed) != 1 || placed[0] != "b" || len(calls) != 1 {
		t.Errorf("placed %v, err %v, calls %v: a failed push is skipped and the instance is started", placed, err, calls)
	}
}

// When a level has a resource whose outcome is unknown, the apply says so in words of its own, and does not begin the next level.
func TestApplyStopsAtAResourceWhoseOutcomeIsUnknown(t *testing.T) {
	levels := [][]Resource{
		{{Kind: KindInstance, Name: "db"}, {Kind: KindProject, Name: "p"}},
		{{Kind: KindInstance, Name: "app"}},
	}
	var started []string
	var mu sync.Mutex
	notes, err := applyLevels(context.Background(), levels, func(r Resource, note func(string, ...any)) (outcome, error) {
		mu.Lock()
		started = append(started, r.Name)
		mu.Unlock()
		if r.Name == "db" {
			return outConverged, &incusapi.TimeoutError{What: "creating instance/db", After: time.Minute}
		}
		return outChanged, nil
	})
	if !errors.Is(err, incusapi.ErrTimeout) {
		t.Fatalf("err = %v, want it to wrap the timeout", err)
	}
	for _, s := range started {
		if s == "app" {
			t.Errorf("app was started although db's outcome is unknown: %v", started)
		}
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{"instance/db: OUTCOME UNKNOWN", "tink plan", "1 failed"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes do not say %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "p: OUTCOME UNKNOWN") || strings.Contains(joined, "project/p: OUTCOME") {
		t.Errorf("a resource that did not time out is not unknown:\n%s", joined)
	}
}

// --- rebuild ---

func unknown(what string) error { return &incusapi.TimeoutError{What: what, After: time.Minute} }

// A step of a rebuild that was abandoned may still be running on the server, so nothing that would collide with it is done: the instance is not
// restarted on top of a rebuild, and the report says the outcome is unknown rather than "nothing was changed" or "restored".
func TestRunRebuildAbandonedStepLeavesTheInstanceAlone(t *testing.T) {
	tests := []struct {
		name     string
		fail     string
		snapshot bool
		wantSeq  string
		wantSays []string
		wantNot  []string
	}{
		{"stop", "stop", false, "running > snapshots > pull > stop",
			[]string{"outcome is unknown", "stop", "tink plan"}, []string{"nothing was changed", "restored"}},
		{"snapshot", "snapshot-volumes", true, "running > snapshots > pull > stop > snapshot-volumes",
			[]string{"outcome is unknown", "left stopped"}, []string{"restored", "nothing was changed"}},
		{"rebuild", "rebuild", false, "running > snapshots > pull > stop > rebuild",
			[]string{"outcome is unknown", "root filesystem", "tink plan"}, []string{"restored", "nothing was changed"}},
		{"config", "config", false, "running > snapshots > pull > stop > rebuild > config",
			[]string{"outcome is unknown", "left stopped"}, []string{"nothing was changed"}},
		{"start", "start", false, "running > snapshots > pull > stop > rebuild > config > start",
			[]string{"outcome is unknown"}, []string{"nothing was changed"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeOps{running: true, fail: map[string]error{tc.fail: unknown(tc.fail + " of ha")}}
			err := runRebuild(context.Background(), ops, rebuildResource(tc.snapshot), fixedNow, func(string, ...any) {})
			if err == nil || !errors.Is(err, incusapi.ErrTimeout) {
				t.Fatalf("err = %v, want it to wrap the timeout", err)
			}
			if ops.seq() != tc.wantSeq {
				t.Errorf("calls = %q, want %q", ops.seq(), tc.wantSeq)
			}
			for _, s := range tc.wantSays {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("err = %q, want it to say %q", err, s)
				}
			}
			for _, s := range tc.wantNot {
				if strings.Contains(err.Error(), s) {
					t.Errorf("err = %q, must not say %q", err, s)
				}
			}
		})
	}
}

// The calls themselves: each of the rebuild's changes has a deadline.
func TestRebuildOpsReturnAtTheDeadlineWhenTheServerNeverFinishes(t *testing.T) {
	for _, tc := range []struct {
		name string
		do   func(o incusRebuildOps) error
	}{
		{"stop", func(o incusRebuildOps) error { return o.Stop("ha") }},
		{"snapshot", func(o incusRebuildOps) error { _, err := o.SnapshotVolumes("ha", "snap"); return err }},
		{"rebuild", func(o incusRebuildOps) error { return o.Rebuild("ha", "abc") }},
		{"start", func(o incusRebuildOps) error { return o.Start("ha") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newHangServer()
			defer close(s.release)
			var err error
			within(t, 5*time.Second, func() { err = tc.do(incusRebuildOps{server: s, opTimeout: shortDeadline}) })
			wantUnknown(t, err, "")
		})
	}
}

// The pull is the one step that is cancelled at its deadline, because it changes no instance: the wait ends, and the server's download with it.
func TestPullingTheImageOfARebuildEndsAtTheDeadline(t *testing.T) {
	host := newRegistry(t, nil)
	pushIndex(t, host, "team/app:1", "amd64")
	srv := newPullServer()
	srv.hang = true
	var err error
	within(t, 5*time.Second, func() {
		_, err = incusRebuildOps{server: srv, env: envFor(host), opTimeout: shortDeadline}.PullImage(context.Background(), "reg:team/app:1")
	})
	if !errors.Is(err, incusapi.ErrTimeout) || errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the deadline, not a Ctrl-C", err)
	}
	if !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("err = %q: it must say the pull was cancelled (nothing is left running)", err)
	}
}

func TestDefaultDeadlines(t *testing.T) {
	quick, slow := opLimit(0, false), opLimit(0, true)
	if quick <= 0 || slow <= quick {
		t.Errorf("quick %s, slow %s: both must be set, and creating must be allowed longer than a config change", quick, slow)
	}
	if opLimit(time.Second, true) != time.Second || opLimit(time.Second, false) != time.Second {
		t.Error("an override replaces both")
	}
}
