package resolve

import (
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	regremote "github.com/google/go-containerregistry/pkg/v1/remote"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
)

// The real layers of docker.io/library/eclipse-mosquitto@sha256:38c0da4f..., read with skopeo on the lab host, and the fingerprints Incus
// records for the instance built from it (amd64) and that the same reference gives for arm64. They pin Incus's derivation against real data:
// if Incus changes how it fingerprints an OCI image, this fails, and tink's drift check would otherwise report drift that is not there.
var (
	mosquittoAmd64 = []string{
		"sha256:d0c1d894c237d8192cbcd37e435031ad4eddec173299568a5a05869c2e40dfa3",
		"sha256:1cc094d98b60f0b536781b73eb8f049aac64d1c977ba75d0696541d6da8345c6",
		"sha256:c6efe2275c693632d08a78902f355669b66424eed0f11f40b91083b1edda37af",
	}
	mosquittoArm64 = []string{
		"sha256:ace1621be7ff15b54252f68393ac33181df7f3e095e36a5d9a9892031b357d31",
		"sha256:6393ff29d1920f03bb64b5561c070a4a03b03ac8aea66ec862905e58625ee44d",
		"sha256:065dc08a9c2869e14b7e19b92f0494be18c00805c1422355c280fdf1e0a24633",
	}
)

func layersOf(t *testing.T, digests []string) []v1.Descriptor {
	t.Helper()
	var out []v1.Descriptor
	for _, d := range digests {
		h, err := v1.NewHash(d)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, v1.Descriptor{Digest: h})
	}
	return out
}

func TestTheFingerprintIsIncusDerivationOverRealLayers(t *testing.T) {
	if got, want := ociFingerprint(layersOf(t, mosquittoAmd64)), "e59c17488050d66bb2536ba2661cc838622ffea8823ea02afac801b3ec998232"; got != want {
		t.Errorf("amd64: %s, want %s (the live instance's volatile.base_image)", got, want)
	}
	if got, want := ociFingerprint(layersOf(t, mosquittoArm64)), "bece04cd8917fbeed56250099cb86b7f7175e82aec55b326ddf0e4727998fe67"; got != want {
		t.Errorf("arm64: %s, want %s", got, want)
	}
	// the order of the layers is part of the identity
	rev := layersOf(t, mosquittoAmd64)
	rev[0], rev[2] = rev[2], rev[0]
	if ociFingerprint(rev) == ociFingerprint(layersOf(t, mosquittoAmd64)) {
		t.Error("the same layers in another order are another image")
	}
}

func TestAServerArchitectureBecomesAnOCIPlatform(t *testing.T) {
	for in, want := range map[string]v1.Platform{
		"x86_64":  {OS: "linux", Architecture: "amd64"},
		"aarch64": {OS: "linux", Architecture: "arm64"},
		"i686":    {OS: "linux", Architecture: "386"},
		"armv7l":  {OS: "linux", Architecture: "arm", Variant: "v7"},
		"armv6l":  {OS: "linux", Architecture: "arm", Variant: "v6"},
		"ppc64le": {OS: "linux", Architecture: "ppc64le"},
		"s390x":   {OS: "linux", Architecture: "s390x"},
		"riscv64": {OS: "linux", Architecture: "riscv64"},
	} {
		got, err := ociPlatform(in)
		if err != nil || got.String() != want.String() {
			t.Errorf("%s: %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := ociPlatform("vax"); err == nil || !strings.Contains(err.Error(), `"vax"`) {
		t.Errorf("an architecture there is no OCI platform for is an error that names it: %v", err)
	}
}

// archServer is a server that only says what it runs.
type archServer struct {
	incus.InstanceServer
	archs  []string
	kernel string
	err    error
}

func (s archServer) GetServer() (*api.Server, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return &api.Server{Environment: api.ServerEnvironment{Architectures: s.archs, KernelArchitecture: s.kernel}}, "", nil
}

func TestTheServersPlatformIsItsFirstArchitecture(t *testing.T) {
	// the first architecture is the native one; the others are what it can also run
	if p, err := serverPlatform(archServer{archs: []string{"x86_64", "i686"}, kernel: "aarch64"}); err != nil || p.Architecture != "amd64" {
		t.Errorf("%v %v: the listed architectures win, and the first is the native one", p, err)
	}
	if p, err := serverPlatform(archServer{kernel: "aarch64"}); err != nil || p.Architecture != "arm64" {
		t.Errorf("%v %v: with no list, the kernel's", p, err)
	}
	if _, err := serverPlatform(archServer{}); err == nil {
		t.Error("a server that says nothing about its architecture is an error, not a guess")
	}
	if _, err := serverPlatform(archServer{err: errors.New("boom")}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a server that cannot be read: %v", err)
	}
}

// ---- against a real registry (the library's own, in memory) ----

// newRegistry serves a registry on loopback and returns its host:port. wrap, if given, sits in front of it.
func newRegistry(t *testing.T, wrap func(http.Handler) http.Handler) string {
	t.Helper()
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // no container-registry login of whoever runs the tests
	var h http.Handler = registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func remoteAt(host string) cliconfig.Remote {
	return cliconfig.Remote{Addrs: []string{"http://" + host}, Protocol: "oci"}
}

// pushIndex pushes one multi-architecture image, with a random image per architecture, and returns them with the index's digest.
func pushIndex(t *testing.T, host, repoTag string, archs ...string) (map[string]v1.Image, v1.Hash) {
	t.Helper()
	idx := v1.ImageIndex(empty.Index)
	imgs := map[string]v1.Image{}
	for _, a := range archs {
		img, err := random.Image(512, 3)
		if err != nil {
			t.Fatal(err)
		}
		imgs[a] = img
		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{Add: img, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: a}}})
	}
	ref, err := name.ParseReference(host + "/" + repoTag)
	if err != nil {
		t.Fatal(err)
	}
	if err := regremote.WriteIndex(ref, idx); err != nil {
		t.Fatal(err)
	}
	d, err := idx.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return imgs, d
}

// want is what tink should say about a pushed image.
func want(t *testing.T, img v1.Image) registryImage {
	t.Helper()
	m, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	var size int64
	for _, l := range m.Layers {
		size += l.Size
	}
	return registryImage{Fingerprint: ociFingerprint(m.Layers), Size: size}
}

func TestAMultiArchitectureReferenceIsResolvedForTheServersArchitecture(t *testing.T) {
	host := newRegistry(t, nil)
	imgs, _ := pushIndex(t, host, "team/app:1", "amd64", "arm64")
	for _, arch := range []string{"amd64", "arm64"} {
		got, err := lookupRegistryImage(remoteAt(host), "team/app:1", v1.Platform{OS: "linux", Architecture: arch})
		if err != nil {
			t.Fatal(err)
		}
		if w := want(t, imgs[arch]); got != w {
			t.Errorf("%s: %+v, want %+v (its own layers' fingerprint and size)", arch, got, w)
		}
	}
	a, _ := lookupRegistryImage(remoteAt(host), "team/app:1", v1.Platform{OS: "linux", Architecture: "amd64"})
	b, _ := lookupRegistryImage(remoteAt(host), "team/app:1", v1.Platform{OS: "linux", Architecture: "arm64"})
	if a.Fingerprint == b.Fingerprint {
		t.Error("the same reference is a different image on another architecture, and must have another fingerprint")
	}
}

func TestADigestPinnedReferenceIsResolvedByItsDigestWhateverTheTagNowPointsAt(t *testing.T) {
	host := newRegistry(t, nil)
	amd := v1.Platform{OS: "linux", Architecture: "amd64"}
	first, digest := pushIndex(t, host, "team/app:1", "amd64")
	second, _ := pushIndex(t, host, "team/app:1", "amd64") // the tag moves
	pinned, err := lookupRegistryImage(remoteAt(host), "team/app:1@"+digest.String(), amd)
	if err != nil {
		t.Fatal(err)
	}
	if pinned != want(t, first["amd64"]) {
		t.Errorf("tag and digest together: the digest is what is resolved: %+v", pinned)
	}
	floating, err := lookupRegistryImage(remoteAt(host), "team/app:1", amd)
	if err != nil {
		t.Fatal(err)
	}
	if floating != want(t, second["amd64"]) || floating == pinned {
		t.Errorf("the bare tag follows the tag: %+v", floating)
	}
}

func TestAReferenceTheRegistryDoesNotHaveIsAnErrorNotAFingerprint(t *testing.T) {
	host := newRegistry(t, nil)
	got, err := lookupRegistryImage(remoteAt(host), "team/nothing:1", v1.Platform{OS: "linux", Architecture: "amd64"})
	if err == nil || got.Fingerprint != "" {
		t.Errorf("%+v %v", got, err)
	}
}

func TestAnImageWithNothingForTheServersPlatformIsAnError(t *testing.T) {
	host := newRegistry(t, nil)
	pushIndex(t, host, "team/arm-only:1", "arm64")
	_, err := lookupRegistryImage(remoteAt(host), "team/arm-only:1", v1.Platform{OS: "linux", Architecture: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "amd64") {
		t.Errorf("an image that has nothing for the server's architecture must say so, not hand back another one: %v", err)
	}
}

func TestTheRuntimeConfigIsReadFromTheImagesConfig(t *testing.T) {
	host := newRegistry(t, nil)
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.Config(img, v1.Config{Entrypoint: []string{"/docker-entrypoint.sh"}, Cmd: []string{"mosquitto", "-c", "/m.conf"}, Env: []string{"VERSION=2.1.2", "PATH=/bin"}, WorkingDir: "/srv", User: "1883"})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := name.ParseReference(host + "/team/app:cfg")
	if err := regremote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	got, err := lookupRuntimeConfig(remoteAt(host), "team/app:cfg", v1.Platform{OS: "linux", Architecture: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(got.Entrypoint, []string{"/docker-entrypoint.sh"}) || !equalStrings(got.Cmd, []string{"mosquitto", "-c", "/m.conf"}) ||
		!equalStrings(got.Env, []string{"VERSION=2.1.2", "PATH=/bin"}) || got.WorkingDir != "/srv" || got.User != "1883" {
		t.Errorf("runtime = %+v", got)
	}
}

// requireBasic makes the registry demand a login once enforce is set (so that images can be pushed first).
func requireBasic(enforce *atomic.Bool, user, password string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if enforce.Load() {
				if u, p, ok := r.BasicAuth(); !ok || u != user || p != password {
					w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func TestCredentialsWrittenIntoARemotesURLAreUsedAndNeverShown(t *testing.T) {
	var enforce atomic.Bool
	host := newRegistry(t, requireBasic(&enforce, "puller", "s3cret"))
	imgs, _ := pushIndex(t, host, "team/private:1", "amd64")
	enforce.Store(true)
	amd := v1.Platform{OS: "linux", Architecture: "amd64"}

	if _, err := lookupRegistryImage(remoteAt(host), "team/private:1", amd); err == nil {
		t.Fatal("a registry that wants a login must refuse an anonymous lookup")
	}
	withLogin := cliconfig.Remote{Addrs: []string{"http://puller:s3cret@" + host}, Protocol: "oci"}
	got, err := lookupRegistryImage(withLogin, "team/private:1", amd)
	if err != nil || got != want(t, imgs["amd64"]) {
		t.Fatalf("with the login in the URL: %+v %v", got, err)
	}
	wrong := cliconfig.Remote{Addrs: []string{"http://puller:nope@" + host}, Protocol: "oci"}
	if _, err := lookupRegistryImage(wrong, "team/private:1", amd); err == nil {
		t.Error("a wrong password is refused")
	}
	if h := remoteHost(withLogin); h != host || strings.Contains(h, "s3cret") || strings.Contains(registryRef(withLogin, "team/private:1"), "s3cret") {
		t.Errorf("the password must never become part of a reference or a message: host %q, ref %q", h, registryRef(withLogin, "team/private:1"))
	}
}

func TestTheHostOfAnOCIRemoteIsJustTheHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://docker.io":                   "docker.io",
		"https://ghcr.io/":                    "ghcr.io",
		"http://127.0.0.1:5000":               "127.0.0.1:5000",
		"https://user:pass@registry.example":  "registry.example",
		"https://registry.example:8443/extra": "registry.example:8443",
		"docker.io":                           "docker.io",
	} {
		if got := remoteHost(cliconfig.Remote{Addrs: []string{in}}); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
	if remoteHost(cliconfig.Remote{}) != "" {
		t.Error("a remote with no address has no host")
	}
}

// With no login in the URL, the registry is reached as whoever runs tink is logged in to it (the login skopeo and docker share), so an
// image on a private registry that a person can pull is one tink can check.
func TestTheContainerRegistryLoginOfWhoeverRunsTinkIsUsed(t *testing.T) {
	var enforce atomic.Bool
	host := newRegistry(t, requireBasic(&enforce, "puller", "s3cret"))
	imgs, _ := pushIndex(t, host, "team/private:1", "amd64")
	enforce.Store(true)
	amd := v1.Platform{OS: "linux", Architecture: "amd64"}

	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	login := base64.StdEncoding.EncodeToString([]byte("puller:s3cret"))
	cfg := `{"auths": {"` + host + `": {"auth": "` + login + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := lookupRegistryImage(remoteAt(host), "team/private:1", amd)
	if err != nil || got != want(t, imgs["amd64"]) {
		t.Fatalf("with a login on this machine: %+v %v", got, err)
	}
	// and the login in a remote's URL wins over it
	wrong := cliconfig.Remote{Addrs: []string{"http://puller:nope@" + host}, Protocol: "oci"}
	if _, err := lookupRegistryImage(wrong, "team/private:1", amd); err == nil {
		t.Error("what the remote's URL says is what is used")
	}
}
