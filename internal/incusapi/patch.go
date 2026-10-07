package incusapi

import (
	"fmt"
	"net/url"

	incus "github.com/lxc/incus/v7/client"
)

// PatchInstanceConfig sets keys on an instance's config with a PATCH of just those keys, and touches nothing else.
//
// It is what to use to publish a value (the helper's status document) onto an instance, in preference to reading the whole
// instance and writing it back: a whole-instance PUT needs the etag, conflicts with an operator's concurrent edit and has to
// retry, and could drop a field a newer server added if this client library is older than the server. A PATCH merges only the
// keys it names. Incus answers an instance PATCH synchronously (verified on 7.5.1), so there is no operation to wait on.
//
// project is the project the instance is in ("" for the connection's own).
func PatchInstanceConfig(s incus.InstanceServer, project, name string, config map[string]string) error {
	path := "/1.0/instances/" + url.PathEscape(name)
	if project != "" {
		path += "?project=" + url.QueryEscape(project)
	}
	if _, _, err := s.RawQuery("PATCH", path, map[string]any{"config": config}, ""); err != nil {
		return fmt.Errorf("updating the config of instance %s: %w", name, err)
	}
	return nil
}
