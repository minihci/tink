package helper

import (
	"fmt"
	"sync"
	"time"
)

// Publisher writes the status document, but only when it is worth an event: when something in it changed, or when the heartbeat is
// due. Every write is a lifecycle event in the log of anything listening and about 90 ms of the daemon's CPU (measured on the lab
// host), so writing on every tick would cost 1,440 events a day, and on change plus a 10-minute heartbeat about 150.
type Publisher struct {
	// Patch sets keys on the helper's own instance config, and nothing else (incusapi.PatchInstanceConfig).
	Patch func(config map[string]string) error
	// Heartbeat is how often to write when nothing changed (default DefaultHeartbeat).
	Heartbeat time.Duration
	// Now is a test seam.
	Now func() time.Time

	mu        sync.Mutex
	lastKey   string
	lastWrite time.Time
	marked    bool
	// failed is set when a write that was due did not get through: the instance does not have what the helper last meant to say, so the
	// next call writes whether or not anything changed, instead of waiting for the next heartbeat.
	failed bool
}

func (p *Publisher) heartbeat() time.Duration {
	if p.Heartbeat > 0 {
		return p.Heartbeat
	}
	return DefaultHeartbeat
}

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Publish stamps s (its protocol, its tick and its heartbeat) and writes it if it differs from the last one written or the
// heartbeat is due. It reports whether it wrote. A failed write is returned and is not remembered as done, so the next call tries
// again; the first successful write also marks the instance as a helper (MarkerKey), so a helper started by hand is found too.
func (p *Publisher) Publish(s Status) (wrote bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.Proto = StatusProto
	s.Tick = now.UTC()
	s.HeartbeatSeconds = int(p.heartbeat() / time.Second)

	key := changeKey(s)
	if !p.failed && p.lastKey == key && now.Sub(p.lastWrite) < p.heartbeat() {
		return false, nil
	}
	text, err := Encode(s)
	if err != nil {
		return false, err
	}
	cfg := map[string]string{StatusKey: text}
	if !p.marked {
		cfg[MarkerKey] = fmt.Sprint(s.JobProto)
	}
	if err := p.Patch(cfg); err != nil {
		p.failed = true
		return false, err
	}
	p.lastKey, p.lastWrite, p.marked, p.failed = key, now, true, false
	return true, nil
}
