package resolve

// Deadlines on the resolver's calls to the Incus server (issue #123). A guest exec has its own (incusapi.DefaultExecTimeout, timeout: on the
// resource); this is every other call, so a daemon that accepts a request and never answers ends the run at a known time instead of at a second Ctrl-C.
//
// What a deadline may safely do depends on the call, and the answer for a change is "less than it seems". Abandoning a wait does not stop the server
// working, and cancelling the operation does not undo it: an instance create or a rebuild stopped part way is left part way. So for every call that
// CHANGES the server, the deadline only stops waiting (incusapi.Bounded): the operation is not cancelled, the error says the outcome is unknown and
// that `tink plan` will show what the server reached, and whatever depends on the call is not started (a step that is abandoned ends the steps after
// it, and the apply starts no later level). Cancelling is done in exactly one place, the pull of a rebuild's image, because it changes no instance.
// Calls that only read are abandoned the same way, with a message that says nothing was changed.
//
// There are two lengths. Most calls are quick (a config write, a start, a stop, a file push) and get five minutes: far longer than any of them takes
// on a healthy server, so a deadline that fires means the server is not answering, not that it is slow. Creating an instance (the server may fetch the
// image first), importing an image, snapshotting a volume, rebuilding and installing the helper can really take a long time, and get thirty minutes;
// a deadline that cuts off a healthy one is a worse failure than waiting, since the outcome is then unknown, which is the situation to avoid.
//
// They are not settings. The design has one place for a deadline a stack author sets, the timeout: of a kind: exec resource, because only a guest
// command has a duration the author knows; nothing else on the server does, and a knob for each would be a large new surface for a failure that has one
// answer. PlanOptions and volumeEnv carry an unexported override so a test can use a short one.

import "time"

const (
	defaultOpTimeout     = 5 * time.Minute
	defaultSlowOpTimeout = 30 * time.Minute
)

// opLimit is the deadline of one call: override when it is set (a test), else the quick or the slow default.
func opLimit(override time.Duration, slow bool) time.Duration {
	switch {
	case override > 0:
		return override
	case slow:
		return defaultSlowOpTimeout
	}
	return defaultOpTimeout
}

// limit is the deadline of a call made for env: slow for the calls that can really take long (see above).
func (e volumeEnv) limit(slow bool) time.Duration { return opLimit(e.opTimeout, slow) }

// boundsPlan reports whether planning r has a deadline of its own to add. An exec has its own (timeout:, for the guest command), and an incus
// resource runs a local process, not a call to the server.
func boundsPlan(r Resource) bool { return r.Kind != KindExec && r.Kind != KindIncus }
