package incusapi

import (
	"fmt"

	incus "github.com/lxc/incus/v7/client"
)

// MergeServerConfig sets and unsets keys of the server's own config and keeps every other key as it was.
//
// Like MergeVolumeConfig it is a read, a change and a whole-object write with the ETag that was read, done again from a new read if Incus answers 412
// (RetryOnConflict). A key in unset that is not set is not an error. The server's config holds keys that are not this caller's to drop (the listen address,
// the trust and the authorization settings), which is why it merges and never replaces.
func MergeServerConfig(s incus.InstanceServer, set map[string]string, unset []string) error {
	return RetryOnConflict(func() error {
		srv, etag, err := s.GetServer()
		if err != nil {
			return fmt.Errorf("reading the server config: %w", err)
		}
		put := srv.Writable()
		merged := make(map[string]string, len(put.Config)+len(set))
		for k, v := range put.Config {
			merged[k] = v
		}
		for _, k := range unset {
			delete(merged, k)
		}
		for k, v := range set {
			merged[k] = v
		}
		put.Config = merged
		if err := s.UpdateServer(put, etag); err != nil {
			return fmt.Errorf("updating the server config: %w", err)
		}
		return nil
	})
}
