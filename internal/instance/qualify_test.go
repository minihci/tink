package instance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Moved here with Qualify from internal/dockerrun (#216).

func qualify(t *testing.T, f *readFake, image string) (ImageQualification, error) {
	t.Helper()
	return Qualify(f, image)
}

func TestABareReferenceIsDockerHubWithAWarningThatSaysHowToBeExplicit(t *testing.T) {
	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	q, err := qualify(t, none, "louislam/uptime-kuma:2")
	if err != nil || q.Image != "docker-oci:louislam/uptime-kuma:2" {
		t.Fatalf("%+v %v", q, err)
	}
	for _, want := range []string{"names no registry", "Docker Hub", "docker.io/louislam/uptime-kuma:2", "docker-oci:louislam/uptime-kuma:2"} {
		if !strings.Contains(q.Warning, want) {
			t.Errorf("warning %q should mention %q", q.Warning, want)
		}
	}
	// an official image lives under library/ on Docker Hub, and the suggestion has to say so
	q, _ = qualify(t, none, "redis:7")
	if !strings.Contains(q.Warning, "docker.io/library/redis:7") {
		t.Errorf("official image: %q", q.Warning)
	}
}

func TestAReferenceTheStackOrTheMachineAlreadyResolvesIsLeftAlone(t *testing.T) {
	for name, f := range map[string]*readFake{
		"a local alias":   {aliasTarget: "abc"},
		"a remote prefix": {aliasErr: errNotFound, imageErr: errNotFound},
		"a failed read":   {aliasErr: errForbidden},
	} {
		in := "haos-x86-64"
		if name == "a remote prefix" {
			in = "docker-oci:redis:7"
		}
		q, err := qualify(t, f, in)
		if err != nil || q.Image != in || q.Warning != "" || q.Note != "" {
			t.Errorf("%s: %+v %v", name, q, err)
		}
	}
}

func TestAReferenceThatNamesARegistryHostUsesTheRemoteForThatHostWithoutAWarning(t *testing.T) {
	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	for in, want := range map[string]string{
		"ghcr.io/advplyr/audiobookshelf:latest": "ghcr:advplyr/audiobookshelf:latest",
		"docker.io/library/redis:7":             "docker-oci:library/redis:7",
		"quay.io/prometheus/node-exporter:v1":   "quay:prometheus/node-exporter:v1",
		"lscr.io/linuxserver/sonarr:latest":     "lscr:linuxserver/sonarr:latest",
	} {
		q, err := qualify(t, none, in)
		if err != nil || q.Image != want || q.Warning != "" {
			t.Errorf("%s -> %+v %v, want %q and no warning", in, q, err, want)
		}
	}
}

func TestAnUnknownRegistryHostIsAnErrorThatSaysHowToAddIt(t *testing.T) {
	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	_, err := qualify(t, none, "registry.example.com/team/app:1")
	if err == nil {
		t.Fatal("sending this to Docker Hub fails with a message about docker.io/registry.example.com/..., which hides the cause")
	}
	for _, want := range []string{"registry.example.com", "incus remote add", "--protocol=oci", "NAME:team/app:1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

// A client configuration may define docker-oci itself (a mirror, say) and that always wins over the built-in. The reference written Docker's way,
// docker.io/..., must then mean the same thing as the bare one does: Docker Hub through the remote the person chose.
func TestADockerHubReferenceUsesTheDockerRemoteEvenWhenTheClientConfigPointsItAtAMirror(t *testing.T) {
	dir := t.TempDir()
	conf := "remotes:\n  docker-oci:\n    protocol: oci\n    public: true\n    addr: https://mirror.example.org\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INCUS_CONF", dir)

	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	q, err := qualify(t, none, "docker.io/library/redis:7")
	if err != nil || q.Image != "docker-oci:library/redis:7" || q.Warning != "" {
		t.Errorf("docker.io: %+v %v, want docker-oci:library/redis:7 and no warning", q, err)
	}
	// the bare reference already goes through the same remote
	q, err = qualify(t, none, "library/redis:7")
	if err != nil || q.Image != "docker-oci:library/redis:7" {
		t.Errorf("bare: %+v %v", q, err)
	}
}
