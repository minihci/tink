// Package incusconf loads the Incus client configuration (the file the `incus` CLI keeps, by default
// ~/.config/incus/config.yml, or $INCUS_CONF) for tink, adding a few built-in image remotes.
//
// Image remote names such as `docker-oci:` are not something an Incus server knows: they are names in the
// CLIENT's configuration, resolved client-side. A machine that has never run `incus remote add` (a laptop
// driving a server over its API, say) would otherwise fail to create an instance from `docker-oci:library/alpine:3`
// with "Image not found", although the server could pull it. The built-ins are only a fallback: a remote the
// client's own configuration defines, under the same name, always wins.
package incusconf

import (
	"fmt"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

// Builtin are the image remotes tink assumes when the client configuration does not define them. They
// are public registries, so there is nothing to authenticate.
var Builtin = map[string]cliconfig.Remote{
	"docker-oci": {Addrs: []string{"https://docker.io"}, Protocol: "oci", Public: true},
	"ghcr":       {Addrs: []string{"https://ghcr.io"}, Protocol: "oci", Public: true},
	"images":     {Addrs: []string{"https://images.linuxcontainers.org"}, Protocol: "simplestreams", Public: true},
}

// Load reads the Incus client configuration and fills in any missing built-in image remote.
func Load() (*cliconfig.Config, error) {
	conf, err := cliconfig.LoadConfig("")
	if err != nil {
		return nil, fmt.Errorf("loading the Incus client configuration: %w", err)
	}
	WithBuiltins(conf)
	return conf, nil
}

// WithBuiltins adds the built-in remotes the configuration lacks, and never replaces one it has.
func WithBuiltins(conf *cliconfig.Config) {
	if conf.Remotes == nil {
		conf.Remotes = map[string]cliconfig.Remote{}
	}
	for name, r := range Builtin {
		if _, ok := conf.Remotes[name]; !ok {
			conf.Remotes[name] = r
		}
	}
}
