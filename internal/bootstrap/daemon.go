package bootstrap

import (
	"errors"
	"fmt"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/incusapi"
)

const daemonConfigDescription = "applied daemon server config (OIDC + authorization scriptlet)"

func applyDaemonConfig(r *runner, opts Options) error {
	rendered, err := renderServerConfig(
		opts.RepoRoot+"/daemon/server-config.yaml",
		opts.RepoRoot+"/daemon/authorization.star",
		opts.Config,
	)
	if err != nil {
		return err
	}
	put, err := parseServerConfig(rendered)
	if err != nil {
		return err
	}
	if r.dryRun { // no daemon is needed to say what would be written
		return putServerConfig(r, nil, put)
	}
	server, err := incusapi.Connect(opts.Socket)
	if err != nil {
		return fmt.Errorf("connecting to incus: %w", err)
	}
	return putServerConfig(r, server, put)
}

// parseServerConfig reads the rendered server-config.yaml as the api.ServerPut that Incus's config edit reads it as. A document with no config keys
// is refused: written as it is, it would reset every key of the server's config, which is never what a template that lost its `config:` line meant.
func parseServerConfig(rendered string) (api.ServerPut, error) {
	var put api.ServerPut
	if err := yaml.Unmarshal([]byte(rendered), &put); err != nil {
		return api.ServerPut{}, fmt.Errorf("parsing the rendered server config as YAML: %w", err)
	}
	if len(put.Config) == 0 {
		return api.ServerPut{}, errors.New("the rendered server config has no config keys: applying it would reset the server's whole config")
	}
	return put, nil
}

// putServerConfig replaces the server's config with put, which is what `incus config edit` does with a document on stdin (it calls UpdateServer
// with no ETag, and so does this): configs/daemon/server-config.yaml already documents why that is safe here, because this host's server config
// is exclusively this repo's concern. Every key the document leaves out is reset, except the user.* keys, which stay (checked on Incus 7.5.1, the
// same through the CLI and through this call). No ETag, because the write does not depend on a read of the current config, so a 412 would have
// nothing to redo (see incusapi.RetryOnConflict).
func putServerConfig(r *runner, server incus.InstanceServer, put api.ServerPut) error {
	if r.dryRun {
		r.note("would replace the server's config with the %d rendered keys (%s)", len(put.Config), daemonConfigDescription)
		return nil
	}
	if err := server.UpdateServer(put, ""); err != nil {
		return fmt.Errorf("%s: replacing the server config: %w", daemonConfigDescription, err)
	}
	r.note("%s", daemonConfigDescription)
	return nil
}
