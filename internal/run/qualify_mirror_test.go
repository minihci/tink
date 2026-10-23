package run

import (
	"os"
	"path/filepath"
	"testing"
)

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
	for _, in := range []string{"docker.io/library/redis:7"} {
		q, err := qualify(t, none, in)
		if err != nil || q.Image != "docker-oci:library/redis:7" {
			t.Errorf("%s -> %+v %v, want docker-oci:library/redis:7", in, q, err)
		}
	}
	// the bare reference already goes through the same remote
	q, err := qualify(t, none, "library/redis:7")
	if err != nil || q.Image != "docker-oci:library/redis:7" {
		t.Errorf("bare: %+v %v", q, err)
	}
}
