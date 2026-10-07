package incusconf

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

func TestWithBuiltinsFillsOnlyWhatIsMissing(t *testing.T) {
	conf := &cliconfig.Config{Remotes: map[string]cliconfig.Remote{
		"docker-oci": {Addrs: []string{"https://registry.example"}, Protocol: "oci", Public: true}, // the user's own
		"tron":       {Addrs: []string{"https://tron:8443"}, Protocol: "incus"},
	}}
	WithBuiltins(conf)
	if got := conf.Remotes["docker-oci"].Addrs[0]; got != "https://registry.example" {
		t.Errorf("a remote the client defines must win over the built-in, got %q", got)
	}
	for _, name := range []string{"ghcr", "images", "tron"} {
		if _, ok := conf.Remotes[name]; !ok {
			t.Errorf("remote %q missing after WithBuiltins", name)
		}
	}
	if conf.Remotes["ghcr"].Protocol != "oci" || !conf.Remotes["ghcr"].Public {
		t.Errorf("ghcr must be a public OCI remote: %+v", conf.Remotes["ghcr"])
	}
}

func TestWithBuiltinsOnANilMap(t *testing.T) {
	conf := &cliconfig.Config{}
	WithBuiltins(conf)
	if len(conf.Remotes) != len(Builtin) {
		t.Errorf("got %d remotes, want %d", len(conf.Remotes), len(Builtin))
	}
}

// A machine that has never run `incus remote add` has no configuration file at all: that is the case this
// package exists for.
func TestLoadWithNoConfigFile(t *testing.T) {
	t.Setenv("INCUS_CONF", filepath.Join(t.TempDir(), "nothing-here"))
	conf, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"docker-oci", "ghcr", "images", "local"} {
		if _, ok := conf.Remotes[name]; !ok {
			t.Errorf("with no config file the remote %q must still resolve", name)
		}
	}
}

func TestLoadKeepsTheClientsOwnDefinition(t *testing.T) {
	dir := t.TempDir()
	cfg := "default-remote: local\nremotes:\n  docker-oci:\n    addr: https://mirror.example\n    protocol: oci\n    public: true\n  local:\n    addr: unix://\n    protocol: incus\n    public: false\naliases: {}\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INCUS_CONF", dir)
	conf, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := conf.Remotes["docker-oci"].Addrs[0]; got != "https://mirror.example" {
		t.Errorf("the client's own docker-oci must win, got %q", got)
	}
	if _, ok := conf.Remotes["ghcr"]; !ok {
		t.Error("a built-in the file does not define must still be added")
	}
}
