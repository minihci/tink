package helper

import (
	"fmt"
	"sync"
	"time"

	"github.com/minihci/tink/internal/instant"
)

// Publisher writes the status document, but only when it is worth an event: when something in it changed, or when the heartbeat is
// due. Every write is a lifecycle event in the log of anything listening and about 90 ms of the daemon's CPU (measured on the lab
// host), so writing on every tick would cost 1,440 events a day, and on change plus a 10-minute heartbeat about 150.
type Publisher struct {
	// Patch sets keys on whatever holds the document, and nothing else: the helper's own instance config (incusapi.PatchInstanceConfig)
	// or its status volume's (incusapi.MergeVolumeConfig).
	Patch func(config map[string]string) error
	// Read returns the stored text of the document, from the same place Patch writes it (the helper's own instance config, or its status volume's), and
	// "" when nothing is stored there. It is how a daemon that has just started reads what it said before (Load). Nil means the place cannot be read.
	Read func() (string, error)
	// NoMarker leaves the helper marker out of what is written: the document is on a volume, and the marker belongs on the instance,
	// where `helper install` put it. The helper holds no right to write the instance.
	NoMarker bool
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

// Publish stamps s (its protocol, its write time and its heartbeat) and writes it if it differs from the last one written or the
// heartbeat is due. It reports whether it wrote. A failed write is returned and is not remembered as done, so the next call tries
// again; the first successful write also marks the instance as a helper (MarkerKey), so a helper started by hand is found too (unless
// NoMarker: with the document on a volume, the instance is marked by whoever made it).
func (p *Publisher) Publish(s Status) (wrote bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	s.Proto = StatusProto
	s.WrittenAt = instant.At(now)
	s.Heartbeat = instant.Duration(p.heartbeat())

	key := changeKey(s)
	if !p.failed && p.lastKey == key && now.Sub(p.lastWrite) < p.heartbeat() {
		return false, nil
	}
	text, err := Encode(s)
	if err != nil {
		return false, err
	}
	cfg := map[string]string{StatusKey: text}
	if !p.marked && !p.NoMarker {
		cfg[MarkerKey] = fmt.Sprint(s.JobProto)
	}
	if err := p.Patch(cfg); err != nil {
		p.failed = true
		return false, err
	}
	p.lastKey, p.lastWrite, p.marked, p.failed = key, now, true, false
	return true, nil
}

// Load reads the document that is stored now, which is what an earlier life of this daemon last published. It reads the key Publish writes, from the place
// Publish writes it, so it needs no right that publishing does not already hold. A place with no document is an empty Status and no error; a document that
// cannot be read (not JSON, no protocol, a newer protocol, a place that cannot be reached) is an error, and the caller starts empty: there is no version
// handling, and what cannot be read seeds nothing.
func (p *Publisher) Load() (Status, error) {
	if p.Read == nil {
		return Status{}, nil
	}
	text, err := p.Read()
	if err != nil {
		return Status{}, err
	}
	if text == "" {
		return Status{}, nil
	}
	return Parse(text)
}
