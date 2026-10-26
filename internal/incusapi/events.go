package incusapi

import (
	"context"
	"encoding/json"
	"fmt"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// WatchLifecycle subscribes to the lifecycle events of every project and calls handle with the action of each ("instance-updated", "profile-updated", ...).
// It returns nil when ctx is done and an error when the stream ends before that (the server went away, the connection dropped), so the caller can
// subscribe again. Nothing is replayed after a reconnect: the caller reconciles in full when it resubscribes, which is why the events carry nothing but
// the action.
//
// ready is called once, after the subscription is active and before any event is handled, so a caller that reconciles "from now on" can do its full pass
// knowing nothing after it will be missed. handle runs on the client's own goroutine and must not block.
func WatchLifecycle(ctx context.Context, server incus.InstanceServer, ready func(), handle func(action string)) error {
	l, err := server.GetEventsAllProjectsByType([]string{api.EventTypeLifecycle})
	if err != nil {
		return fmt.Errorf("subscribing to the Incus event stream: %w", err)
	}
	defer l.Disconnect()
	if _, err := l.AddHandler([]string{api.EventTypeLifecycle}, func(e api.Event) {
		var lc api.EventLifecycle
		if json.Unmarshal(e.Metadata, &lc) == nil && lc.Action != "" {
			handle(lc.Action)
		}
	}); err != nil {
		return fmt.Errorf("subscribing to the Incus event stream: %w", err)
	}
	if ready != nil {
		ready()
	}
	ended := make(chan error, 1)
	go func() { ended <- l.Wait() }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-ended:
		if err == nil {
			err = fmt.Errorf("the Incus event stream ended")
		}
		return fmt.Errorf("the Incus event stream: %w", err)
	}
}
