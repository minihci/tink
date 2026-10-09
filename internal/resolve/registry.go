package resolve

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	regremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/cliconfig"
)

// Asking a registry about an image, in this process.
//
// Incus's own OCI client does this by running skopeo, on the machine running the client, for the architecture of that machine. For tink
// that meant a tool to install on every laptop that runs `plan` against a server, and the wrong answer whenever the laptop and the server
// differ (an arm64 laptop and an amd64 server compare the arm64 image with an instance built from the amd64 one, and every image looks
// like drift). So tink asks the registry itself, and for the architecture the SERVER runs images for.

// registryTimeout bounds one lookup of one image.
const registryTimeout = 90 * time.Second

// ociFingerprint is the fingerprint Incus gives an OCI image: a sha256 over the digests of its layers, in order, each written the way a
// registry writes it ("sha256:..."). It is Incus's own derivation (computeFingerprint in its client/oci_images.go), and it is what an
// instance records as volatile.base_image, so it is the identity to compare. The layers are those of ONE platform's image: a
// multi-architecture reference has one fingerprint per architecture.
func ociFingerprint(layers []v1.Descriptor) string {
	h := sha256.New()
	for _, l := range layers {
		h.Write([]byte(l.Digest.String()))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ociPlatform maps an architecture as an Incus server reports it to the platform an OCI image is chosen by.
func ociPlatform(arch string) (v1.Platform, error) {
	p := v1.Platform{OS: "linux"}
	switch arch {
	case "x86_64":
		p.Architecture = "amd64"
	case "aarch64":
		p.Architecture = "arm64"
	case "i686":
		p.Architecture = "386"
	case "armv7l", "armv8l":
		p.Architecture, p.Variant = "arm", "v7"
	case "armv6l":
		p.Architecture, p.Variant = "arm", "v6"
	case "ppc64le", "s390x", "riscv64":
		p.Architecture = arch
	default:
		return v1.Platform{}, fmt.Errorf("the server's architecture %q is not one tink can choose an OCI image for", arch)
	}
	return p, nil
}

// serverPlatform asks the server which platform it runs images for: its first (native) architecture.
func serverPlatform(server incus.InstanceServer) (v1.Platform, error) {
	srv, _, err := server.GetServer()
	if err != nil {
		return v1.Platform{}, fmt.Errorf("reading the server's architecture: %w", err)
	}
	arch := srv.Environment.KernelArchitecture
	if len(srv.Environment.Architectures) > 0 {
		arch = srv.Environment.Architectures[0]
	}
	if arch == "" {
		return v1.Platform{}, errors.New("the server does not say which architecture it runs")
	}
	return ociPlatform(arch)
}

// registryRef is the reference to ask the registry for: the OCI remote's host, then the image as the stack wrote it (a tag, a digest, or
// both: the digest is what is resolved, as Incus does).
func registryRef(remote cliconfig.Remote, ref string) string {
	return remoteHost(remote) + "/" + ref
}

// registryCredentials are the user and password written into an OCI remote's URL (https://user:password@host), which Incus's client
// presents to the registry. ok is false when the URL carries none.
func registryCredentials(remote cliconfig.Remote) (user, password string, ok bool) {
	if len(remote.Addrs) == 0 {
		return "", "", false
	}
	u, err := url.Parse(remote.Addrs[0])
	if err != nil || u.User == nil {
		return "", "", false
	}
	password, _ = u.User.Password()
	return u.User.Username(), password, true
}

// loginKeychain is the container-registry login of whoever runs tink (the one docker and skopeo use), tolerating a login that cannot
// be read. A credential helper named in the person's docker config can be missing or broken (Docker Desktop's, on a machine that no
// longer has it), and that must not stop a lookup of a public image that works anonymously. The problem is remembered instead, so that
// if the anonymous request is then refused, the error can say why.
type loginKeychain struct {
	mu  sync.Mutex
	err error
}

func (k *loginKeychain) Resolve(r authn.Resource) (authn.Authenticator, error) {
	a, err := authn.DefaultKeychain.Resolve(r)
	if err != nil {
		k.mu.Lock()
		k.err = err
		k.mu.Unlock()
		return authn.Anonymous, nil
	}
	return a, nil
}

// explain adds, to a REFUSAL from the registry (401 or 403), that the login could not be read, when that is so: it is the likely reason,
// and what the person has to fix. Any other error is left as it is.
func (k *loginKeychain) explain(err error) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err == nil || k.err == nil {
		return err
	}
	var terr *transport.Error
	if !errors.As(err, &terr) || (terr.StatusCode != http.StatusUnauthorized && terr.StatusCode != http.StatusForbidden) {
		return err
	}
	return fmt.Errorf("%w (the container-registry login could not be read, so the registry was asked anonymously: %v)", err, k.err)
}

// registryOptions say how to reach the registry: for the given platform, and as whoever the remote's URL names, or else as the
// container-registry login of the person running tink, or anonymously when there is none (or it cannot be read).
func registryOptions(ctx context.Context, remote cliconfig.Remote, plat v1.Platform, login *loginKeychain) []regremote.Option {
	opts := []regremote.Option{regremote.WithContext(ctx), regremote.WithPlatform(plat)}
	if user, password, ok := registryCredentials(remote); ok {
		return append(opts, regremote.WithAuth(&authn.Basic{Username: user, Password: password}))
	}
	return append(opts, regremote.WithAuthFromKeychain(login))
}

// openImage resolves ref on the remote's registry to the one platform's image the server would run.
func openImage(ctx context.Context, remote cliconfig.Remote, ref string, plat v1.Platform, login *loginKeychain) (v1.Image, error) {
	img, _, err := openImageDigest(ctx, remote, ref, plat, login)
	return img, err
}

// openImageDigest is openImage that also returns the digest the reference itself names: what a registry shows as the digest of a tag
// (for a multi-architecture image, the index's), which is what `image: name:tag@sha256:...` pins.
func openImageDigest(ctx context.Context, remote cliconfig.Remote, ref string, plat v1.Platform, login *loginKeychain) (v1.Image, string, error) {
	r, err := name.ParseReference(registryRef(remote, ref))
	if err != nil {
		return nil, "", err
	}
	desc, err := regremote.Get(r, registryOptions(ctx, remote, plat, login)...)
	if err != nil {
		return nil, "", err
	}
	img, err := desc.Image()
	if err != nil {
		return nil, "", err
	}
	return img, desc.Digest.String(), nil
}

// lookupRegistryImage is what the registry says about ref for plat: the fingerprint Incus would give it, and the size of its layers.
func lookupRegistryImage(remote cliconfig.Remote, ref string, plat v1.Platform) (registryImage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
	defer cancel()
	login := &loginKeychain{}
	img, digest, err := openImageDigest(ctx, remote, ref, plat, login)
	if err != nil {
		return registryImage{}, login.explain(err)
	}
	m, err := img.Manifest()
	if err != nil {
		return registryImage{}, login.explain(err)
	}
	var size int64
	for _, l := range m.Layers {
		size += l.Size
	}
	return registryImage{Fingerprint: ociFingerprint(m.Layers), Size: size, Digest: digest}, nil
}

// lookupRuntimeConfig reads the runtime config an image bakes in (entrypoint, command, environment, working directory, user).
func lookupRuntimeConfig(remote cliconfig.Remote, ref string, plat v1.Platform) (ociRuntime, error) {
	ctx, cancel := context.WithTimeout(context.Background(), registryTimeout)
	defer cancel()
	login := &loginKeychain{}
	img, err := openImage(ctx, remote, ref, plat, login)
	if err != nil {
		return ociRuntime{}, login.explain(err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return ociRuntime{}, login.explain(fmt.Errorf("reading the image config: %w", err))
	}
	c := cf.Config
	return ociRuntime{Entrypoint: c.Entrypoint, Cmd: c.Cmd, Env: c.Env, WorkingDir: c.WorkingDir, User: c.User}, nil
}
