package bootstrap

import (
	"fmt"

	"github.com/lxc/incus/v7/shared/api"
	yaml "go.yaml.in/yaml/v4"
)

// parseProfileYAML renders path (the same ${VAR} substitution every
// template gets) and parses the result as an api.ProfilePut -- the
// top-level `name:` key in the rendered YAML is simply ignored, since
// ProfilePut has no Name field (Incus's own type split: create needs a
// name, update doesn't).
func parseProfileYAML(path string, cfg Config) (api.ProfilePut, error) {
	rendered, err := render(path, cfg)
	if err != nil {
		return api.ProfilePut{}, err
	}
	var put api.ProfilePut
	if err := yaml.Unmarshal([]byte(rendered), &put); err != nil {
		return api.ProfilePut{}, fmt.Errorf("parsing rendered %s as profile YAML: %w", path, err)
	}
	return put, nil
}
