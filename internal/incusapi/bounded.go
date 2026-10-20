package incusapi

import (
	"errors"
	"fmt"
	"time"
)

// ErrTimeout is wrapped by the error of a call that was abandoned at its deadline (see Bounded).
var ErrTimeout = errors.New("no answer from the Incus server before the deadline")

// TimeoutError is what Bounded returns when the deadline passes first. It says, in its text, the one thing the caller cannot know: whether the work
// was done.
type TimeoutError struct {
	What  string
	After time.Duration
}

func (e *TimeoutError) Error() string {
	return fmt.Sprintf("%s: no answer from the Incus server after %s, so the outcome is unknown: the server may still be doing it, and may finish it later "+
		"(tink did not cancel it: cancelling an Incus operation does not undo what it has done). Run `tink plan` to see what state it reached before applying again",
		e.What, e.After)
}

func (e *TimeoutError) Is(target error) bool { return target == ErrTimeout }

// Bounded runs fn and returns its result, or, if fn has not returned within timeout, a *TimeoutError wrapping ErrTimeout. what names the work for that
// error ("creating instance/web"). A timeout of zero or less is no bound: fn runs on the caller's goroutine, as if Bounded were not there.
//
// Abandoning is all a deadline does. fn is NOT cancelled: it keeps running in its goroutine (a call to Incus has no way to be cancelled that also undoes
// it: the client's WaitContext cancels the operation on the server, and an instance create or a rebuild stopped part way is not rolled back), so after a
// *TimeoutError the work may be done, running, or about to be done, and the caller must treat the resource as being in an unknown state: not start what
// depends on it, not undo it, and say so. fn's own result, if it ever comes, is dropped. The goroutine ends when the call does, or with the process.
//
// For a call that only reads, abandoning it is plainly harmless, and Bounded is still how it ends: the message is then more cautious than it needs to be.
func Bounded[T any](timeout time.Duration, what string, fn func() (T, error)) (T, error) {
	if timeout <= 0 {
		return fn()
	}
	type result struct {
		v   T
		err error
	}
	done := make(chan result, 1) // buffered: an abandoned fn must be able to finish
	go func() {
		v, err := fn()
		done <- result{v, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.v, r.err
	case <-timer.C:
		var zero T
		return zero, &TimeoutError{What: what, After: timeout}
	}
}

// BoundedDo is Bounded for a call that returns only an error.
func BoundedDo(timeout time.Duration, what string, fn func() error) error {
	_, err := Bounded(timeout, what, func() (struct{}, error) { return struct{}{}, fn() })
	return err
}
