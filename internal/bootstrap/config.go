package bootstrap

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Config mirrors deploy.env's fields exactly (see incus-host/deploy.env.example
// for what each one means and how to find its value on a given host).
type Config struct {
	IncusUIDomain      string
	AuthDomain         string
	BridgeNetwork      string
	IncusAPIAddr       string
	AuthelialStaticIP  string
	IncusUIStaticIP    string
	StoragePool        string
	AdminUsername      string
	AdminEmail         string
	ImageRegistry      string
	ImageRegistryToken string
}

// deployEnvKeys maps deploy.env's KEY names to where they land in Config,
// so LoadConfig and template rendering agree on the same variable set.
func (c *Config) fields() map[string]*string {
	return map[string]*string{
		"INCUS_UI_DOMAIN":      &c.IncusUIDomain,
		"AUTH_DOMAIN":          &c.AuthDomain,
		"BRIDGE_NETWORK":       &c.BridgeNetwork,
		"INCUS_API_ADDR":       &c.IncusAPIAddr,
		"AUTHELIA_STATIC_IP":   &c.AuthelialStaticIP,
		"INCUS_UI_STATIC_IP":   &c.IncusUIStaticIP,
		"STORAGE_POOL":         &c.StoragePool,
		"ADMIN_USERNAME":       &c.AdminUsername,
		"ADMIN_EMAIL":          &c.AdminEmail,
		"IMAGE_REGISTRY":       &c.ImageRegistry,
		"IMAGE_REGISTRY_TOKEN": &c.ImageRegistryToken,
	}
}

// LoadConfig parses a deploy.env-shaped file: flat KEY=VALUE lines, '#'
// comments, blank lines ignored. Deliberately not a bash `source` (which
// is what deploy.sh itself still does) -- a plain parser means a
// malformed or tampered deploy.env can't execute anything, only ever set
// a value tink already knows the name of.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	fields := cfg.fields()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("%s:%d: expected KEY=VALUE, got %q", path, lineNum, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		field, known := fields[key]
		if !known {
			return Config{}, fmt.Errorf("%s:%d: unrecognized key %q", path, lineNum, key)
		}
		*field = value
	}
	if err := scanner.Err(); err != nil {
		return Config{}, fmt.Errorf("reading %s: %w", path, err)
	}

	return cfg, nil
}

// requiredFields are the deploy.env keys that must be set for apply to
// proceed -- everything except the registry token, which is genuinely
// optional (only needed for a private registry).
var requiredFields = []string{
	"INCUS_UI_DOMAIN", "AUTH_DOMAIN", "BRIDGE_NETWORK", "INCUS_API_ADDR",
	"AUTHELIA_STATIC_IP", "INCUS_UI_STATIC_IP", "STORAGE_POOL",
	"ADMIN_USERNAME", "ADMIN_EMAIL", "IMAGE_REGISTRY",
}

// Validate reports every missing required field at once, rather than
// failing on the first one -- so a fresh deploy.env's problems can all be
// fixed in one pass instead of one error at a time.
func (c Config) Validate() error {
	fields := c.fields()
	var missing []string
	for _, name := range requiredFields {
		if *fields[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("deploy.env missing required value(s): %s", strings.Join(missing, ", "))
	}
	return nil
}
