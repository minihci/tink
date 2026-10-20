package resolve

import (
	"net/http"
	"sync"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// etagInstanceServer models the part of Incus a trigger-hash write meets: a whole-instance PUT carries the etag it read and is refused with 412 if
// the instance changed since, and a PATCH of config keys merges and needs no etag. Reads wait (up to a moment) for a second reader, so two writers
// that read before writing both hold the same etag, which is the interleaving a shared dependency level allows.
type etagInstanceServer struct {
	incus.InstanceServer
	mu      sync.Mutex
	config  map[string]string
	version int
	readers int
	both    chan struct{}
}

func newEtagInstanceServer() *etagInstanceServer {
	return &etagInstanceServer{config: map[string]string{}, both: make(chan struct{})}
}

func (s *etagInstanceServer) UseProject(string) incus.InstanceServer { return s }

func (s *etagInstanceServer) GetInstance(name string) (*api.Instance, string, error) {
	s.mu.Lock()
	cfg := map[string]string{}
	for k, v := range s.config {
		cfg[k] = v
	}
	etag := string(rune('a' + s.version))
	s.readers++
	if s.readers == 2 {
		close(s.both)
	}
	s.mu.Unlock()
	select {
	case <-s.both:
	case <-time.After(200 * time.Millisecond):
	}
	return &api.Instance{Name: name, InstancePut: api.InstancePut{Config: cfg}}, etag, nil
}

func (s *etagInstanceServer) UpdateInstance(name string, put api.InstancePut, etag string) (incus.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if etag != string(rune('a'+s.version)) {
		return nil, api.StatusErrorf(http.StatusPreconditionFailed, "ETag doesn't match")
	}
	s.config = put.Config
	s.version++
	return noopOperation{}, nil
}

func (s *etagInstanceServer) RawQuery(method, path string, data any, etag string) (*api.Response, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if method == "PATCH" {
		for k, v := range data.(map[string]any)["config"].(map[string]string) {
			s.config[k] = v
		}
		s.version++
	}
	return &api.Response{Type: "sync", StatusCode: 200}, "", nil
}

type noopOperation struct{ incus.Operation }

func (noopOperation) Wait() error { return nil }

// Two exec resources with triggers on one instance share a dependency level and so record their hashes at the same time: neither write may be lost
// or refused because the other changed the instance first.
func TestRecordTriggerHashTwoConcurrentWritesOnOneInstanceBothLand(t *testing.T) {
	s := newEtagInstanceServer()
	rs := []Resource{
		{Kind: KindExec, Name: "first", Instance: "web", Triggers: []string{"a"}},
		{Kind: KindExec, Name: "second", Instance: "web", Triggers: []string{"b"}},
	}
	errs := make([]error, len(rs))
	var wg sync.WaitGroup
	for i := range rs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = recordTriggerHash(s, rs[i])
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("%s: %v", rs[i].Name, err)
		}
	}
	for _, r := range rs {
		if got := s.config[triggerHashConfigKey(r.Name)]; got != triggerHash(r.Triggers) {
			t.Errorf("%s: stored hash %q, want %q: a concurrent write lost it", r.Name, got, triggerHash(r.Triggers))
		}
	}
}
