package incusapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

func conflict() error { return api.StatusErrorf(http.StatusPreconditionFailed, "ETag doesn't match") }

func TestRetryOnConflictRunsAgainAfterA412AndStopsOnSuccess(t *testing.T) {
	calls := 0
	err := RetryOnConflict(func() error {
		calls++
		if calls == 1 {
			return conflict()
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Errorf("err = %v after %d calls, want nil after 2", err, calls)
	}
}

func TestRetryOnConflictSeesAWrapped412(t *testing.T) {
	calls := 0
	err := RetryOnConflict(func() error {
		calls++
		if calls == 1 {
			return errors.Join(errors.New("updating profile"), conflict())
		}
		return nil
	})
	if err != nil || calls != 2 {
		t.Errorf("a 412 under a wrapping error is still a conflict: err = %v after %d calls", err, calls)
	}
}

func TestRetryOnConflictDoesNotRetryAnyOtherError(t *testing.T) {
	for _, e := range []error{errors.New("boom"), api.StatusErrorf(http.StatusNotFound, "gone"), api.StatusErrorf(http.StatusInternalServerError, "oops")} {
		calls := 0
		err := RetryOnConflict(func() error { calls++; return e })
		if err != e || calls != 1 {
			t.Errorf("%v: err = %v after %d calls, want that error after 1", e, err, calls)
		}
	}
}

func TestRetryOnConflictGivesUpAfterTheAttemptsAndReturnsTheConflict(t *testing.T) {
	calls := 0
	err := RetryOnConflict(func() error { calls++; return conflict() })
	if !api.StatusErrorCheck(err, http.StatusPreconditionFailed) || calls != conflictAttempts {
		t.Errorf("err = %v after %d calls, want the 412 after %d", err, calls, conflictAttempts)
	}
}
