package incusapi

import (
	"net/http"

	"github.com/lxc/incus/v7/shared/api"
)

// conflictAttempts is how many times RetryOnConflict runs fn before it gives up on an object that keeps changing under it.
const conflictAttempts = 3

// RetryOnConflict runs fn, and runs it again if Incus refuses its write with 412 (the object changed between fn's read and its write, so
// the ETag fn sent no longer matches). It returns fn's first result that is not a 412, or the last 412 once conflictAttempts are used.
//
// fn must be the whole read-modify-write, not the write alone: a retry that reuses what the first attempt read would send the same stale
// ETag again, and one that reuses the modified copy would put back what the other writer just replaced. Whatever fn changes must be
// worked out from what it read each time (a counter is read and added to, not carried across attempts).
//
// Every whole-object PUT with an ETag goes through here; a test fails if one does not (cmd/tink/conflict_test.go). A PATCH needs no ETag
// and no retry (PatchInstanceConfig).
func RetryOnConflict(fn func() error) error {
	var err error
	for attempt := 0; attempt < conflictAttempts; attempt++ {
		if err = fn(); err == nil || !api.StatusErrorCheck(err, http.StatusPreconditionFailed) {
			return err
		}
	}
	return err
}
