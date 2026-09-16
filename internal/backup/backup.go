// Package backup will implement tink's mongo backup/snapshot capability —
// discussed but not yet settled on a DESIGN.md the way ingress and
// bootstrap are (candidates on the table: mongodump, Incus-native volume
// snapshots, an off-host encrypted target, and a disposable-container
// restore test — see the platform notes for the full discussion). Expect
// this shape to change before it's real; it exists here now only so a
// third capability slots in without restructuring the other two.
package backup

import "errors"

// ErrNotImplemented is returned until this capability has an actual design.
var ErrNotImplemented = errors.New("backup: not yet designed or implemented")

// Snapshot will trigger a mongo backup once this capability is designed.
func Snapshot() error {
	return ErrNotImplemented
}
