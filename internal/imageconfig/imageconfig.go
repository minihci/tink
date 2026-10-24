// Package imageconfig reads an OCI image's own ENTRYPOINT and CMD from its registry.
//
// Incus keeps one merged string for the process an OCI container runs (oci.entrypoint, from the image's Entrypoint and Cmd joined), and nothing records where
// one ends and the other begins; an attempt to keep the image's Cmd on the instance was closed unmerged upstream (lxc/incus #3765). The boundary matters
// because Docker puts the arguments given after the image name after the ENTRYPOINT, while a string set as oci.entrypoint replaces all of it: `tink run IMAGE
// ARGS` for an image with an entrypoint started nothing (#230). The only place the two are still apart is the image's configuration blob in its registry, so
// this reads it there.
//
// It is anonymous and read-only: a manifest (choosing the platform from an index) and the configuration blob it names, with the bearer-token handshake
// registries ask for. A registry that wants credentials is an error, which the caller treats as "could not read", not as "has no entrypoint".
package imageconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Config is what an image's configuration says about the process it starts.
type Config struct {
	Entrypoint []string
	Cmd        []string
}

// maxBody bounds every response read: a manifest or a configuration blob is a few kilobytes, so a registry that sends megabytes is not one to trust.
const maxBody = 4 << 20

const acceptManifests = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Platform  *struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
	} `json:"platform"`
}

type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    descriptor   `json:"config"`
	Manifests []descriptor `json:"manifests"`
}

// Fetch reads the ENTRYPOINT and CMD of repo at ref (a tag or a digest) for linux and arch ("amd64", "arm64"), from the registry at base ("https://ghcr.io").
// client may be nil.
func Fetch(ctx context.Context, client *http.Client, base, repo, ref, arch string) (*Config, error) {
	if client == nil {
		client = http.DefaultClient
	}
	r := &reader{client: client, base: strings.TrimSuffix(base, "/"), repo: repo}

	body, err := r.get(ctx, "/manifests/"+ref, acceptManifests)
	if err != nil {
		return nil, fmt.Errorf("reading the manifest of %s: %w", repo, err)
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("the manifest of %s is not JSON: %w", repo, err)
	}
	if len(m.Manifests) > 0 { // an index: choose the platform, then read that manifest
		digest := ""
		for _, d := range m.Manifests {
			if d.Platform != nil && d.Platform.OS == "linux" && d.Platform.Architecture == arch {
				digest = d.Digest
				break
			}
		}
		if digest == "" {
			return nil, fmt.Errorf("%s has no linux/%s image", repo, arch)
		}
		if body, err = r.get(ctx, "/manifests/"+digest, acceptManifests); err != nil {
			return nil, fmt.Errorf("reading the manifest of %s: %w", repo, err)
		}
		m = manifest{}
		if err := json.Unmarshal(body, &m); err != nil {
			return nil, fmt.Errorf("the manifest of %s is not JSON: %w", repo, err)
		}
	}
	if m.Config.Digest == "" {
		return nil, fmt.Errorf("the manifest of %s names no configuration", repo)
	}
	blob, err := r.get(ctx, "/blobs/"+m.Config.Digest, "")
	if err != nil {
		return nil, fmt.Errorf("reading the configuration of %s: %w", repo, err)
	}
	var c struct {
		Config struct {
			Entrypoint []string `json:"Entrypoint"`
			Cmd        []string `json:"Cmd"`
		} `json:"config"`
	}
	if err := json.Unmarshal(blob, &c); err != nil {
		return nil, fmt.Errorf("the configuration of %s is not JSON: %w", repo, err)
	}
	return &Config{Entrypoint: c.Config.Entrypoint, Cmd: c.Config.Cmd}, nil
}

type reader struct {
	client *http.Client
	base   string
	repo   string
	token  string
}

// get reads /v2/REPO + path, answering a 401 once with an anonymous bearer token.
func (r *reader) get(ctx context.Context, path, accept string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/v2/"+r.repo+path, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if r.token != "" {
			req.Header.Set("Authorization", "Bearer "+r.token)
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusUnauthorized && attempt == 0:
			if err := r.authenticate(ctx, resp.Header.Get("Www-Authenticate")); err != nil {
				return nil, err
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return nil, fmt.Errorf("the registry answered %s", resp.Status)
		case readErr != nil:
			return nil, readErr
		case len(body) > maxBody:
			return nil, fmt.Errorf("the registry sent more than %d bytes", maxBody)
		}
		return body, nil
	}
	return nil, fmt.Errorf("the registry refused an anonymous read of %s", r.repo)
}

// authenticate answers a `Bearer realm="...",service="...",scope="..."` challenge with an anonymous token.
func (r *reader) authenticate(ctx context.Context, challenge string) error {
	scheme, params, ok := strings.Cut(strings.TrimSpace(challenge), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return fmt.Errorf("the registry wants credentials (%q)", challenge)
	}
	vals := map[string]string{}
	for _, p := range strings.Split(params, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok {
			vals[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
	}
	realm, err := url.Parse(vals["realm"])
	if err != nil || (realm.Scheme != "https" && realm.Scheme != "http") || realm.Host == "" {
		return fmt.Errorf("the registry's token endpoint %q is not a URL", vals["realm"])
	}
	q := realm.Query()
	if vals["service"] != "" {
		q.Set("service", vals["service"])
	}
	scope := vals["scope"]
	if scope == "" {
		scope = "repository:" + r.repo + ":pull"
	}
	q.Set("scope", scope)
	realm.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the registry's token endpoint answered %s (the repository may need credentials)", resp.Status)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&t); err != nil {
		return fmt.Errorf("the registry's token is not JSON: %w", err)
	}
	r.token = t.Token
	if r.token == "" {
		r.token = t.AccessToken
	}
	if r.token == "" {
		return fmt.Errorf("the registry gave no token")
	}
	return nil
}
