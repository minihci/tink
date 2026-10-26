package instance

import (
	"fmt"
	"net/url"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/cliconfig"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/incusconf"
)

// ImageQualification is what Qualify decided about an image reference.
type ImageQualification struct {
	// Image is the reference to use: the input when nothing needed doing, else REMOTE:REF.
	Image string
	// Note says what was done when the person had been explicit (a registry host written Docker's way, mapped to its remote).
	Note string
	// Warning is set when the registry was a guess: the reference named none, so Docker Hub was assumed.
	Warning string
}

// Qualify turns a docker-run-style reference into one Incus can resolve, when nothing else would: it is not REMOTE:REF with a configured
// remote, and not a local alias or fingerprint (a name that resolves locally is left alone, so this only ever turns an error into an
// attempt). Used by `tink run` and, for a stack's instances, by `plan` and `plan apply`, so one reference means one thing everywhere.
//
//	ghcr.io/advplyr/audiobookshelf:latest   -> ghcr:advplyr/audiobookshelf:latest        a registry host: that host's OCI remote
//	louislam/uptime-kuma:2                  -> docker-oci:louislam/uptime-kuma:2         no registry: Docker Hub, with a Warning
//	registry.example.com/team/app:1         -> an error saying how to add a remote for it
//
// An unknown host is an error, not a guess: sending the whole name to Docker Hub fails with a message about docker.io/registry.example.com/...
// that hides the cause.
func Qualify(scoped incus.InstanceServer, image string) (ImageQualification, error) {
	same := ImageQualification{Image: image}
	conf, err := incusconf.Load()
	if err != nil {
		return same, nil
	}
	if remoteName, _, has := strings.Cut(image, ":"); has {
		if _, known := conf.Remotes[remoteName]; known {
			return same, nil
		}
	}
	if _, _, found, err := incusapi.LookupImageAlias(scoped, image); err != nil || found {
		return same, nil
	}
	if _, _, found, err := incusapi.LookupImage(scoped, image); err != nil || found {
		return same, nil
	}
	if host, rest, ok := registryHost(image); ok {
		for _, name := range sortedRemoteNames(conf.Remotes) {
			if r := conf.Remotes[name]; r.Protocol == "oci" && RemoteHost(r) == host {
				return ImageQualification{Image: name + ":" + rest, Note: fmt.Sprintf("%q is the registry %s: using the remote %s", image, host, name)}, nil
			}
		}
		if _, known := conf.Remotes[dockerRemote]; known && host == "docker.io" {
			// docker.io is what docker-oci means, wherever the client configuration points it (a mirror, say): the same answer the bare name gets.
			return ImageQualification{Image: dockerRemote + ":" + rest, Note: fmt.Sprintf("%q is the registry %s: using the remote %s", image, host, dockerRemote)}, nil
		}
		return same, fmt.Errorf("image %q is on the registry %s, and no OCI remote is configured for it: add one (for example `incus remote add NAME https://%s --protocol=oci`) and write the image as NAME:%s", image, host, host, rest)
	}
	if _, known := conf.Remotes[dockerRemote]; !known {
		return same, nil
	}
	hub := image
	if !strings.Contains(image, "/") {
		hub = "library/" + image // Docker Hub's official images live under library/
	}
	return ImageQualification{
		Image:   dockerRemote + ":" + image,
		Warning: fmt.Sprintf("%q names no registry: assuming Docker Hub. Say so with docker.io/%s (or %s:%s) to silence this", image, hub, dockerRemote, image),
	}, nil
}

// registryHost splits "host/path" when the first path segment looks like a registry host, as Docker decides it: it has a dot or a colon,
// or is localhost.
func registryHost(image string) (host, rest string, ok bool) {
	host, rest, found := strings.Cut(image, "/")
	if !found || rest == "" {
		return "", "", false
	}
	if strings.ContainsAny(host, ".:") || host == "localhost" {
		return host, rest, true
	}
	return "", "", false
}

// RemoteHost is the registry host an OCI remote points at, without scheme, path or login.
func RemoteHost(r cliconfig.Remote) string {
	if len(r.Addrs) == 0 {
		return ""
	}
	h := r.Addrs[0]
	if u, err := url.Parse(h); err == nil && u.Host != "" {
		return u.Host
	}
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://"), "/")
}

func sortedRemoteNames(m map[string]cliconfig.Remote) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// dockerRemote is the built-in OCI remote for Docker Hub (see internal/incusconf).
const dockerRemote = "docker-oci"
