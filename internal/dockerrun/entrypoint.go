package dockerrun

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/imageconfig"
	"github.com/minihci/tink/internal/incusconf"
	"github.com/minihci/tink/internal/instance"
)

// A command given after the image goes after the image's ENTRYPOINT, as `docker run IMAGE ARGS` does (#230). Incus keeps one merged string for the process
// (oci.entrypoint) and a value set there replaces all of it, so without the image's own entrypoint `tink run IMAGE ARGS` started nothing for an image that has
// one (Piper and Whisper: "bad file descriptor"). The entrypoint is read from the image's registry (internal/imageconfig), because that is the only place where it is
// still apart from the image's CMD.

// errNotOCI says the image is not one an OCI registry holds, so it has no entrypoint to put the command after.
var errNotOCI = errors.New("not an OCI image")

// readImageConfig is imageconfig.Fetch, replaceable by a test.
var readImageConfig = func(ctx context.Context, base, repo, ref, arch string) (*imageconfig.Config, error) {
	return imageconfig.Fetch(ctx, nil, base, repo, ref, arch)
}

// imageEntrypoint is the ENTRYPOINT of image, a REMOTE:REF reference whose remote is an OCI one, read from that remote's registry for the server's architecture.
func imageEntrypoint(server incus.InstanceServer, image string) ([]string, error) {
	remoteName, ref, has := strings.Cut(image, ":")
	if !has {
		return nil, errNotOCI
	}
	conf, err := incusconf.Load()
	if err != nil {
		return nil, err
	}
	remote, known := conf.Remotes[remoteName]
	if !known || remote.Protocol != "oci" || len(remote.Addrs) == 0 {
		return nil, errNotOCI
	}
	host := instance.RemoteHost(remote)
	base := registryBase(remote.Addrs[0])
	repo, tag := splitRef(ref, isDockerHub(host))
	srv, _, err := server.GetServer()
	if err != nil {
		return nil, fmt.Errorf("reading the server's architecture: %w", err)
	}
	arch, err := ociArch(srv.Environment.KernelArchitecture)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := readImageConfig(ctx, base, repo, tag, arch)
	if err != nil {
		return nil, err
	}
	return c.Entrypoint, nil
}

// withEntrypoint is the command line oci.entrypoint takes for command given after an image whose entrypoint is entry: the two joined, as Build joins a command
// that has nothing before it.
func withEntrypoint(entry, command []string) string {
	return strings.Join(append(append([]string{}, entry...), command...), " ")
}

func isDockerHub(host string) bool {
	return host == "docker.io" || host == "index.docker.io" || host == "registry-1.docker.io"
}

// registryBase is the URL the registry API of an OCI remote answers on. Docker Hub's remote is written https://docker.io, which serves the web site; its
// registry is registry-1.docker.io.
func registryBase(addr string) string {
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return strings.TrimSuffix(addr, "/")
	}
	if isDockerHub(u.Host) {
		return "https://registry-1.docker.io"
	}
	return u.Scheme + "://" + u.Host
}

// splitRef splits REPO[:TAG] or REPO@DIGEST into the repository and the reference a manifest is read by (default latest). On Docker Hub a name with no
// slash is an official image, which lives under library/.
func splitRef(ref string, hub bool) (repo, tag string) {
	repo, tag = ref, "latest"
	if i := strings.Index(ref, "@"); i >= 0 {
		repo, tag = ref[:i], ref[i+1:]
	} else if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo, tag = ref[:i], ref[i+1:]
	}
	if hub && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return repo, tag
}

// ociArch maps an Incus server's kernel architecture to the name OCI images use.
func ociArch(kernel string) (string, error) {
	switch kernel {
	case "x86_64":
		return "amd64", nil
	case "aarch64":
		return "arm64", nil
	case "ppc64le", "s390x", "riscv64":
		return kernel, nil
	}
	return "", fmt.Errorf("the server's architecture %q has no OCI name here", kernel)
}
