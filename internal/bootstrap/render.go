package bootstrap

import (
	"fmt"
	"os"
	"strings"
)

// render substitutes ${VAR}/$VAR placeholders in the file at path using
// cfg's own field names -- byte-compatible with the same template files
// incus-host/scripts/deploy.sh's sed-based render() already uses, so
// nothing about the .yaml templates themselves needs to change. Unlike
// sed, an unrecognized ${VAR} is an error, not silently-left-untouched
// output -- a typo'd variable name should fail loudly, not render a
// literal "${TYPO}" into a live profile.
func render(path string, cfg Config) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", path, err)
	}

	fields := cfg.fields()
	var expandErr error
	result := os.Expand(string(content), func(name string) string {
		field, known := fields[name]
		if !known {
			if expandErr == nil {
				expandErr = fmt.Errorf("%s: unrecognized template variable ${%s}", path, name)
			}
			return ""
		}
		return *field
	})
	if expandErr != nil {
		return "", expandErr
	}

	return result, nil
}

// renderServerConfig renders serverConfigPath the same way render() does,
// then splices scriptletPath's content in place of the
// __AUTHORIZATION_SCRIPTLET__ sentinel line, indented to sit correctly
// under the YAML config's authorization.scriptlet key -- the same two-step
// bash does with sed's `r` (read file) address command, since a plain
// substitution can't hold a multi-line replacement either way.
func renderServerConfig(serverConfigPath, scriptletPath string, cfg Config) (string, error) {
	rendered, err := render(serverConfigPath, cfg)
	if err != nil {
		return "", err
	}

	scriptlet, err := os.ReadFile(scriptletPath)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", scriptletPath, err)
	}

	const sentinel = "__AUTHORIZATION_SCRIPTLET__"
	if !strings.Contains(rendered, sentinel) {
		return "", fmt.Errorf("%s: missing %s sentinel line", serverConfigPath, sentinel)
	}

	var indented strings.Builder
	for _, line := range strings.Split(strings.TrimRight(string(scriptlet), "\n"), "\n") {
		indented.WriteString("    ")
		indented.WriteString(line)
		indented.WriteString("\n")
	}

	return strings.Replace(rendered, sentinel+"\n", indented.String(), 1), nil
}
