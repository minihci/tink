package bootstrap

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

type serverConfigFake struct {
	incus.InstanceServer
	puts   []api.ServerPut
	etags  []string
	putErr error
}

func (f *serverConfigFake) UpdateServer(put api.ServerPut, etag string) error {
	f.puts = append(f.puts, put)
	f.etags = append(f.etags, etag)
	return f.putErr
}

// The shipped template, rendered, is what is sent: the same config map the YAML holds, scriptlet included, so what `incus config edit` was given is what
// UpdateServer is given.
func TestParseServerConfigReadsTheShippedTemplateAsTheWholeConfig(t *testing.T) {
	rendered, err := renderServerConfig(
		filepath.Join(shippedDaemonDir, "server-config.yaml"),
		filepath.Join(shippedDaemonDir, "authorization.star"),
		Config{AuthDomain: "auth.example.com", IncusAPIAddr: "10.0.0.1:8443", IncusUIStaticIP: "10.0.0.2"},
	)
	if err != nil {
		t.Fatal(err)
	}
	put, err := parseServerConfig(rendered)
	if err != nil {
		t.Fatal(err)
	}
	if got := put.Config["core.https_address"]; got != "10.0.0.1:8443" {
		t.Errorf("core.https_address = %q", got)
	}
	if got := put.Config["oidc.issuer"]; got != "https://auth.example.com" {
		t.Errorf("oidc.issuer = %q", got)
	}
	if got, want := put.Config["authorization.scriptlet"], readShipped(t, "authorization.star"); got != want {
		t.Errorf("the scriptlet differs from authorization.star:\n got: %q\nwant: %q", got, want)
	}
}

func TestParseServerConfigRefusesADocumentThatWouldEmptyTheConfig(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":            "",
		"comments only":    "# nothing here\n",
		"no config key":    "name: x\n",
		"empty config":     "config: {}\n",
		"config is a list": "config:\n  - a\n",
	} {
		if _, err := parseServerConfig(doc); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
	_, err := parseServerConfig("config:\n  core.https_address: \":8443\"\n")
	if err != nil {
		t.Errorf("a document with a key was refused: %v", err)
	}
}

// What `incus config edit` does with a document on stdin is UpdateServer with no ETag, replacing the whole config; this is the same call.
func TestPutServerConfigReplacesTheWholeConfigWithNoETag(t *testing.T) {
	f := &serverConfigFake{}
	r := &runner{}
	put := api.ServerPut{Config: api.ConfigMap{"core.https_address": ":8443"}}
	if err := putServerConfig(r, f, put); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || f.puts[0].Config["core.https_address"] != ":8443" || len(f.puts[0].Config) != 1 {
		t.Errorf("puts = %v", f.puts)
	}
	if f.etags[0] != "" {
		t.Errorf("etag = %q: a replacement does not depend on a read, so it sends none", f.etags[0])
	}
	if len(r.actions) != 1 || r.actions[0] != daemonConfigDescription {
		t.Errorf("actions = %v", r.actions)
	}
}

func TestPutServerConfigNamesTheStepWhenIncusRefuses(t *testing.T) {
	f := &serverConfigFake{putErr: errors.New("Invalid config: unknown key")}
	r := &runner{}
	err := putServerConfig(r, f, api.ServerPut{Config: api.ConfigMap{"x": "y"}})
	if err == nil || !strings.Contains(err.Error(), "replacing the server config") || !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("err = %v", err)
	}
	if len(r.actions) != 0 {
		t.Errorf("a failed step is not noted as done: %v", r.actions)
	}
}

func TestPutServerConfigDryRunSendsNothing(t *testing.T) {
	r := &runner{dryRun: true}
	// no server at all: a dry run needs no daemon
	if err := putServerConfig(r, nil, api.ServerPut{Config: api.ConfigMap{"a": "1", "b": "2"}}); err != nil {
		t.Fatal(err)
	}
	if len(r.actions) != 1 || !strings.HasPrefix(r.actions[0], "would replace the server's config with the 2 rendered keys") {
		t.Errorf("actions = %v", r.actions)
	}
}
