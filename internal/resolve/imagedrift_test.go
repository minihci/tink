package resolve

import (
	"errors"
	"strings"
	"testing"

	"github.com/lxc/incus/v7/shared/cliconfig"
)

var testRemotes = map[string]cliconfig.Remote{
	"ghcr":       {Addrs: []string{"https://ghcr.io"}, Protocol: "oci"},
	"docker-oci": {Addrs: []string{"https://docker.io"}, Protocol: "oci"},
	"images":     {Addrs: []string{"https://images.linuxcontainers.org"}, Protocol: "simplestreams"},
}

// Instance configs copied from live instances (see the design note, section 13).
var (
	haCfg = map[string]string{"image.id": "home-assistant/home-assistant:2026.9.1", "image.description": "ghcr.io/home-assistant/home-assistant (OCI)", "image.type": "oci", "volatile.base_image": "7d7a19"}
	mqCfg = map[string]string{"image.id": "eclipse-mosquitto:2", "image.description": "docker.io/library/eclipse-mosquitto (OCI)", "image.type": "oci", "volatile.base_image": "e59c17"}
	caCfg = map[string]string{"image.description": "Debian trixie amd64 (20260723_05:24)", "image.type": "squashfs", "volatile.base_image": "548da3"}
	// created from a digest-pinned ref: Incus records the literal string of that first pull
	pinCfg = map[string]string{"image.id": "library/eclipse-mosquitto@sha256:9fec", "image.description": "docker.io/library/eclipse-mosquitto (OCI)", "image.type": "oci", "volatile.base_image": "28f231"}
)

type probe struct {
	fp    string
	err   error
	calls int
	alias map[string]string
}

func (p *probe) imageProbe() imageProbe {
	return imageProbe{
		remotes: testRemotes,
		aliasTarget: func(a string) (string, bool) {
			v, ok := p.alias[a]
			return v, ok
		},
		registryFP: func(remote, ref string) (string, error) {
			p.calls++
			return p.fp, p.err
		},
	}
}

func TestCheckImage(t *testing.T) {
	tests := []struct {
		name      string
		cfg       map[string]string
		image     string
		p         probe
		wantDrift string // substring; empty means none
		wantUnver string
		wantCalls int
	}{
		// the verified false positive: ":2" and ":2.1.2-alpine" are the same bytes
		{"same content, different spelling: no drift", mqCfg, "docker-oci:library/eclipse-mosquitto:2.1.2-alpine", probe{fp: "e59c17"}, "", "", 1},
		{"tag bump: fingerprints differ", haCfg, "ghcr:home-assistant/home-assistant:2026.9.4", probe{fp: "abc163"}, "resolves to abc163", "", 1},
		{"tag unchanged, registry agrees", haCfg, "ghcr:home-assistant/home-assistant:2026.9.1", probe{fp: "7d7a19"}, "", "", 1},
		{"floating tag whose content moved", mqCfg, "docker-oci:library/eclipse-mosquitto:2", probe{fp: "ffffff"}, "resolves to ffffff", "", 1},
		{"registry unreachable: unverified, not drift", haCfg, "ghcr:home-assistant/home-assistant:2026.9.4", probe{err: errors.New("skopeo: boom")}, "", "could not verify", 1},
		{"offline: unverified, not drift", haCfg, "ghcr:home-assistant/home-assistant:2026.9.4", probe{err: errOffline}, "", "--offline", 1},
		{"instance without fingerprint: unverified", map[string]string{"image.id": haCfg["image.id"], "image.description": haCfg["image.description"]}, "ghcr:home-assistant/home-assistant:2026.9.4", probe{fp: "x"}, "", "no fingerprint", 0},

		// digest pins
		{"pinned digest recorded on the instance: conclusive, no lookup", pinCfg, "docker-oci:library/eclipse-mosquitto:2.1.1-alpine@sha256:9fec", probe{fp: "ignored"}, "", "", 0},
		{"pinned digest, library/ spelled differently: still conclusive", pinCfg, "docker-oci:eclipse-mosquitto@sha256:9fec", probe{fp: "ignored"}, "", "", 0},
		{"pinned to a different digest: lookup decides (drift)", pinCfg, "docker-oci:library/eclipse-mosquitto@sha256:38c0", probe{fp: "e59c17"}, "resolves to e59c17", "", 1},
		{"different digest, same content (index vs platform digest): no drift", pinCfg, "docker-oci:library/eclipse-mosquitto@sha256:beef", probe{fp: "28f231"}, "", "", 1},
		{"same digest string on another registry is not a match", pinCfg, "ghcr:library/eclipse-mosquitto@sha256:9fec", probe{fp: "e59c17"}, "resolves to e59c17", "", 1},

		// not OCI / no opinion
		{"simplestreams remote: no opinion", caCfg, "images:debian/trixie", probe{}, "", "", 0},
		{"no image metadata at all (imported disk)", map[string]string{}, "ghcr:home-assistant/home-assistant:2026.9.4", probe{}, "", "", 0},
		{"non-OCI instance, OCI wanted: drift without a lookup", caCfg, "ghcr:home-assistant/home-assistant:2026.9.4", probe{}, "not an OCI image", "", 0},

		// local aliases
		{"local alias unchanged", map[string]string{"volatile.base_image": "aaa111aaa111xyz"}, "haos-x86-64-18.3", probe{alias: map[string]string{"haos-x86-64-18.3": "aaa111aaa111xyz"}}, "", "", 0},
		{"local alias repointed", map[string]string{"volatile.base_image": "aaa111aaa111xyz"}, "haos-x86-64-18.3", probe{alias: map[string]string{"haos-x86-64-18.3": "bbb222bbb222xyz"}}, "now resolves to bbb222bbb222", "", 0},
		{"local alias missing: no opinion", map[string]string{"volatile.base_image": "aaa"}, "nope", probe{}, "", "", 0},
		{"unknown remote prefix is a local alias", map[string]string{"volatile.base_image": "aaa"}, "weird:thing", probe{alias: map[string]string{"weird:thing": "aaa"}}, "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.p
			got := checkImage(tc.cfg, tc.image, p.imageProbe())
			if tc.wantDrift == "" && len(got.Drift) != 0 {
				t.Fatalf("unexpected drift: %v", got.Drift)
			}
			if tc.wantDrift != "" && (len(got.Drift) != 1 || !strings.Contains(got.Drift[0], tc.wantDrift)) {
				t.Fatalf("want drift containing %q, got %v", tc.wantDrift, got.Drift)
			}
			if tc.wantUnver == "" && len(got.Unverified) != 0 {
				t.Fatalf("unexpected unverified: %v", got.Unverified)
			}
			if tc.wantUnver != "" && (len(got.Unverified) != 1 || !strings.Contains(got.Unverified[0], tc.wantUnver)) {
				t.Fatalf("want unverified containing %q, got %v", tc.wantUnver, got.Unverified)
			}
		})
	}
	// registry call counts, checked separately because probe is copied per case above
	for _, tc := range tests {
		p := tc.p
		checkImage(tc.cfg, tc.image, p.imageProbe())
		if p.calls != tc.wantCalls {
			t.Errorf("%s: registry lookups = %d, want %d", tc.name, p.calls, tc.wantCalls)
		}
	}
}

// Real runtime config of the images compared in the design note.
var (
	mq211 = ociRuntime{Entrypoint: []string{"/docker-entrypoint.sh"}, Cmd: []string{"/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"}, WorkingDir: "/",
		Env: []string{"DOWNLOAD_SHA256=d93026", "GPG_KEYS=A0D6EEA1", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "VERSION=2.1.1"}}
	mq212 = ociRuntime{Entrypoint: []string{"/docker-entrypoint.sh"}, Cmd: []string{"/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"}, WorkingDir: "/",
		Env: []string{"DOWNLOAD_SHA256=fd9053", "GPG_KEYS=A0D6EEA1", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "VERSION=2.1.2"}}
	ha91 = ociRuntime{Entrypoint: []string{"/init"}, WorkingDir: "/config",
		Env: []string{"LANG=C.UTF-8", "S6_CMD_WAIT_FOR_SERVICES=1", "UV_NO_CACHE=true"}}
)

func mqInstanceConfig() map[string]string {
	return map[string]string{
		"oci.entrypoint": "/docker-entrypoint.sh /usr/sbin/mosquitto -c /mosquitto/config/mosquitto.conf", "oci.cwd": "/", "oci.uid": "0", "oci.gid": "0",
		"environment.DOWNLOAD_SHA256": "d93026", "environment.GPG_KEYS": "A0D6EEA1", "environment.VERSION": "2.1.1",
		"environment.PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"environment.HOME": "/root", "environment.TERM": "xterm",
	}
}

func TestRuntimeConfigDiff(t *testing.T) {
	t.Run("mosquitto 2.1.1 to 2.1.2 changes VERSION and DOWNLOAD_SHA256", func(t *testing.T) {
		_, keys := runtimeConfigDiff(mqInstanceConfig(), nil, mq212)
		if strings.Join(keys, ",") != "environment.DOWNLOAD_SHA256,environment.VERSION" && strings.Join(keys, ",") != "environment.VERSION,environment.DOWNLOAD_SHA256" {
			t.Fatalf("keys = %v", keys)
		}
	})
	t.Run("declaring the keys in the YAML clears the block", func(t *testing.T) {
		declared := map[string]string{"environment.VERSION": "2.1.2", "environment.DOWNLOAD_SHA256": "fd9053"}
		if diffs, _ := runtimeConfigDiff(mqInstanceConfig(), declared, mq212); len(diffs) != 0 {
			t.Fatalf("diffs = %v", diffs)
		}
	})
	t.Run("same image: nothing", func(t *testing.T) {
		if diffs, _ := runtimeConfigDiff(mqInstanceConfig(), nil, mq211); len(diffs) != 0 {
			t.Fatalf("diffs = %v", diffs)
		}
	})
	t.Run("HA 2026.9.1 to 2026.9.4 (identical runtime config): nothing", func(t *testing.T) {
		cfg := map[string]string{"oci.entrypoint": "/init", "oci.cwd": "/config", "oci.uid": "0", "oci.gid": "0",
			"environment.LANG": "C.UTF-8", "environment.S6_CMD_WAIT_FOR_SERVICES": "1", "environment.UV_NO_CACHE": "true", "environment.TZ": "America/Denver", "environment.HOME": "/root"}
		if diffs, _ := runtimeConfigDiff(cfg, nil, ha91); len(diffs) != 0 {
			t.Fatalf("diffs = %v", diffs)
		}
	})
	t.Run("a new env var the instance never got", func(t *testing.T) {
		rt := ha91
		rt.Env = append(append([]string{}, ha91.Env...), "NEW_THING=1")
		cfg := map[string]string{"oci.entrypoint": "/init", "oci.cwd": "/config", "environment.LANG": "C.UTF-8", "environment.S6_CMD_WAIT_FOR_SERVICES": "1", "environment.UV_NO_CACHE": "true"}
		_, keys := runtimeConfigDiff(cfg, nil, rt)
		if len(keys) != 1 || keys[0] != "environment.NEW_THING" {
			t.Fatalf("keys = %v", keys)
		}
	})
	t.Run("changed entrypoint, cwd and numeric user", func(t *testing.T) {
		rt := ociRuntime{Entrypoint: []string{"/new"}, Cmd: []string{"--x"}, WorkingDir: "/app", User: "1000:1000"}
		cfg := map[string]string{"oci.entrypoint": "/old", "oci.cwd": "/", "oci.uid": "0", "oci.gid": "0"}
		_, keys := runtimeConfigDiff(cfg, nil, rt)
		want := "oci.entrypoint,oci.cwd,oci.uid,oci.gid"
		if strings.Join(keys, ",") != want {
			t.Fatalf("keys = %v, want %s", keys, want)
		}
	})
	t.Run("named user cannot be verified", func(t *testing.T) {
		diffs, keys := runtimeConfigDiff(map[string]string{}, nil, ociRuntime{User: "node"})
		if len(diffs) != 1 || strings.Join(keys, ",") != "oci.uid,oci.gid" {
			t.Fatalf("diffs=%v keys=%v", diffs, keys)
		}
		if diffs, _ := runtimeConfigDiff(map[string]string{}, map[string]string{"oci.uid": "1000", "oci.gid": "1000"}, ociRuntime{User: "node"}); len(diffs) != 0 {
			t.Fatalf("declared uid/gid should clear it: %v", diffs)
		}
	})
	t.Run("empty cwd and missing oci.cwd both mean /", func(t *testing.T) {
		if diffs, _ := runtimeConfigDiff(map[string]string{}, nil, ociRuntime{}); len(diffs) != 0 {
			t.Fatalf("diffs = %v", diffs)
		}
	})
}

func TestSplitArgs(t *testing.T) {
	tests := map[string][]string{
		"/docker-entrypoint.sh /usr/sbin/mosquitto -c /mosquitto/config/mosquitto.conf": {"/docker-entrypoint.sh", "/usr/sbin/mosquitto", "-c", "/mosquitto/config/mosquitto.conf"},
		"/init":                   {"/init"},
		`sh -c 'echo "hi there"'`: {"sh", "-c", `echo "hi there"`},
		`a "b c" d\ e`:            {"a", "b c", "d e"},
		`x "q\"uote"`:             {"x", `q"uote`},
		"":                        nil,
	}
	for in, want := range tests {
		got, err := splitArgs(in)
		if err != nil || !equalStrings(got, want) {
			t.Errorf("splitArgs(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{`'open`, `"open`, `trailing\`} {
		if _, err := splitArgs(bad); err == nil {
			t.Errorf("splitArgs(%q): expected an error", bad)
		}
	}
}

func TestSplitRefAndCanonicalRepo(t *testing.T) {
	repo, tag, dig := splitRef("library/eclipse-mosquitto:2.1.2-alpine@sha256:38c0")
	if repo != "library/eclipse-mosquitto" || tag != "2.1.2-alpine" || dig != "sha256:38c0" {
		t.Errorf("got %q %q %q", repo, tag, dig)
	}
	repo, tag, dig = splitRef("home-assistant/home-assistant@sha256:aa")
	if repo != "home-assistant/home-assistant" || tag != "" || dig != "sha256:aa" {
		t.Errorf("got %q %q %q", repo, tag, dig)
	}
	if canonicalRepo("docker.io", "library/x") != "x" || canonicalRepo("ghcr.io", "library/x") != "library/x" {
		t.Error("library/ is only implicit on docker.io")
	}
}
