package resolve

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"

	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/incusconf"

	"github.com/minihci/tink/internal/secrets"
)

// Image drift: does the image an instance was built from match the image
// the YAML asks for?
//
// The identity that matters is the image FINGERPRINT (volatile.base_image).
// For an OCI image Incus derives it from the layer digests, so it is
// content-addressed. The tempting alternative, comparing image.id, is wrong:
// image.id belongs to whichever cached image record first held that content,
// as typed at that pull, so two refs for the same bytes can differ in text
// (":2" vs ":2.1.2-alpine", with or without "library/", tag vs digest) and
// an unrelated ref can inherit another one's id.
//
// So a drift verdict needs a registry lookup: tink asks the registry itself
// (registry.go), for the architecture the SERVER runs images for, so it gives
// the same answer from a laptop as on the server. The only offline shortcut is
// a conclusive match: a digest-pinned ref whose digest the instance already
// records.

// errOffline marks a lookup that was skipped because --offline was given.
var errOffline = errors.New("registry lookups disabled (--offline)")

// imageCheck is what comparing an instance with its desired image found.
type imageCheck struct {
	// Drift: confirmed, the instance was built from different content.
	Drift []string
	// Unverified: the comparison could not be made (lookup failed, offline, ...).
	Unverified []string
}

// imageProbe holds the outside lookups so the logic can be tested without a
// daemon or a registry.
type imageProbe struct {
	remotes map[string]cliconfig.Remote
	// aliasTarget resolves a local image alias to its fingerprint. ok is false when the alias does not
	// exist (nothing to compare against); any other failure is err, so it is reported as unverified
	// instead of being read as "no drift".
	aliasTarget func(alias string) (fingerprint string, ok bool, err error)
	registryFP  func(remote, ref string) (string, error)
}

func checkImage(cfg map[string]string, image string, p imageProbe) imageCheck {
	// Same split resolveImage uses: a colon-prefix only names a remote if it
	// is actually configured; otherwise the whole string is a local alias.
	remoteName, ref, hasPrefix := strings.Cut(image, ":")
	if remote, known := p.remotes[remoteName]; hasPrefix && known {
		if remote.Protocol != "oci" {
			return imageCheck{} // simplestreams/incus remotes: no opinion
		}
		return checkOCI(cfg, remoteName, remote, ref, p)
	}

	base := cfg["volatile.base_image"]
	if base == "" {
		return imageCheck{} // no image metadata (imported disk, etc.)
	}
	fp, ok, err := p.aliasTarget(image)
	if err != nil {
		return imageCheck{Unverified: []string{fmt.Sprintf("image: could not look up the alias %q to compare it with the instance: %v", image, err)}}
	}
	if !ok || fp == base {
		return imageCheck{}
	}
	return imageCheck{Drift: []string{fmt.Sprintf("image: alias %q now resolves to %.12s, but the instance was built from %.12s", image, fp, base)}}
}

func checkOCI(cfg map[string]string, remoteName string, remote cliconfig.Remote, ref string, p imageProbe) imageCheck {
	host := remoteHost(remote)
	display := host + "/" + ref
	gotID, desc, base := cfg["image.id"], cfg["image.description"], cfg["volatile.base_image"]

	if gotID == "" {
		if base == "" {
			return imageCheck{}
		}
		return imageCheck{Drift: []string{fmt.Sprintf("image: built from %q, not an OCI image, but the YAML wants %q", desc, display)}}
	}

	// Conclusive offline match: same pinned digest on the same repository.
	wantRepo, _, wantDigest := splitRef(ref)
	gotRepo, _, gotDigest := splitRef(gotID)
	if wantDigest != "" && wantDigest == gotDigest &&
		canonicalRepo(host, wantRepo) == canonicalRepo(host, gotRepo) && strings.HasPrefix(desc, host+"/") {
		return imageCheck{}
	}

	if base == "" {
		return imageCheck{Unverified: []string{fmt.Sprintf("image: the instance records no fingerprint, so %q cannot be verified", display)}}
	}
	fp, err := p.registryFP(remoteName, ref)
	if err != nil {
		return imageCheck{Unverified: []string{fmt.Sprintf("image: could not verify %q against the registry: %v", display, err)}}
	}
	if fp == base {
		return imageCheck{}
	}
	cur := strings.TrimSuffix(desc, " (OCI)") + refSuffix(gotID)
	return imageCheck{Drift: []string{fmt.Sprintf("image: %q resolves to %.12s, but the instance was built from %.12s (%s)", display, fp, base, cur)}}
}

// splitRef splits repo[:tag][@digest].
func splitRef(s string) (repo, tag, digest string) {
	if i := strings.Index(s, "@"); i >= 0 {
		digest = s[i+1:]
		s = s[:i]
	}
	if j := strings.LastIndex(s, ":"); j > strings.LastIndex(s, "/") {
		tag = s[j+1:]
		s = s[:j]
	}
	return s, tag, digest
}

// canonicalRepo treats Docker Hub library/x and x as the same repository.
func canonicalRepo(host, repo string) string {
	if host == "docker.io" {
		repo = strings.TrimPrefix(repo, "library/")
	}
	return repo
}

// refSuffix returns the :tag or @digest part of an image.id, for display.
func refSuffix(id string) string {
	if i := strings.Index(id, "@"); i >= 0 {
		return id[i:]
	}
	if j := strings.LastIndex(id, ":"); j > strings.LastIndex(id, "/") {
		return id[j:]
	}
	return ""
}

// remoteHost is the registry host of an OCI remote, without its scheme, its path and any user:password written into its URL.
func remoteHost(remote cliconfig.Remote) string {
	if len(remote.Addrs) == 0 {
		return ""
	}
	h := remote.Addrs[0]
	if u, err := url.Parse(h); err == nil && u.Host != "" {
		return u.Host
	}
	h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
	return strings.TrimSuffix(h, "/")
}

// ---------------------------------------------------------------------------
// Runtime config an OCI image bakes into an instance at creation.
//
// Incus copies oci.entrypoint/cwd/uid/gid and environment.* from the image
// when it creates the instance (and only if absent). `incus rebuild` does not
// refresh them, so after a rebuild they describe the OLD image. Verified live:
// rebuilding mosquitto 2.1.1 -> 2.1.2 left environment.VERSION at 2.1.1.

type ociRuntime struct {
	Entrypoint []string
	Cmd        []string
	Env        []string
	WorkingDir string
	User       string
}

// runtimeConfigDiff lists the instance config keys that would be stale after
// rebuilding onto an image with runtime config rt, ignoring keys the YAML
// declares (tink writes those after the rebuild).
func runtimeConfigDiff(cfg, declared map[string]string, rt ociRuntime) (diffs, keys []string) {
	isDeclared := func(k string) bool { _, ok := declared[k]; return ok }
	add := func(key, have, want string) {
		if isDeclared(key) {
			return
		}
		if secrets.SensitiveKey(key) {
			diffs = append(diffs, fmt.Sprintf("%s: instance and new image differ: %s", key, secrets.HiddenValue))
		} else {
			diffs = append(diffs, fmt.Sprintf("%s: instance has %q, new image wants %q", key, have, want))
		}
		keys = append(keys, key)
	}

	args := append(append([]string{}, rt.Entrypoint...), rt.Cmd...)
	if len(args) > 0 {
		have, err := splitArgs(cfg["oci.entrypoint"])
		if err != nil || !equalStrings(have, args) {
			add("oci.entrypoint", cfg["oci.entrypoint"], strings.Join(args, " "))
		}
	}

	wantCwd := rt.WorkingDir
	if wantCwd == "" {
		wantCwd = "/"
	}
	haveCwd := cfg["oci.cwd"]
	if haveCwd == "" {
		haveCwd = "/"
	}
	if haveCwd != wantCwd {
		add("oci.cwd", haveCwd, wantCwd)
	}

	uid, gid, hasGID, ok := parseImageUser(rt.User)
	if !ok {
		if !(isDeclared("oci.uid") && isDeclared("oci.gid")) {
			diffs = append(diffs, fmt.Sprintf("the image runs as user %q, which cannot be resolved without its filesystem; declare oci.uid and oci.gid", rt.User))
			keys = append(keys, "oci.uid", "oci.gid")
		}
	} else {
		if cfgOr(cfg, "oci.uid", "0") != strconv.Itoa(uid) {
			add("oci.uid", cfgOr(cfg, "oci.uid", "0"), strconv.Itoa(uid))
		}
		if hasGID && cfgOr(cfg, "oci.gid", "0") != strconv.Itoa(gid) {
			add("oci.gid", cfgOr(cfg, "oci.gid", "0"), strconv.Itoa(gid))
		}
	}

	for _, kv := range rt.Env {
		k, v, _ := strings.Cut(kv, "=")
		key := "environment." + k
		have, present := cfg[key]
		if !present || have != v {
			if !present {
				have = "<unset>"
			}
			add(key, have, v)
		}
	}
	return diffs, keys
}

func cfgOr(cfg map[string]string, k, def string) string {
	if v := cfg[k]; v != "" {
		return v
	}
	return def
}

func parseImageUser(u string) (uid, gid int, hasGID, ok bool) {
	if u == "" {
		return 0, 0, false, true
	}
	us, gs, hasColon := strings.Cut(u, ":")
	uid, err := strconv.Atoi(us)
	if err != nil {
		return 0, 0, false, false
	}
	if !hasColon {
		return uid, 0, false, true
	}
	gid, err = strconv.Atoi(gs)
	if err != nil {
		return 0, 0, false, false
	}
	return uid, gid, true, true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// splitArgs splits a shell-quoted command line the way Incus joined it:
// single quotes are literal, double quotes honour backslash escapes, and an
// unquoted backslash escapes the next character.
func splitArgs(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		case c == '\\':
			i++
			if i >= len(s) {
				return nil, errors.New("trailing backslash")
			}
			cur.WriteByte(s[i])
			inWord = true
		case c == '\'':
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				return nil, errors.New("unterminated single quote")
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
			inWord = true
		case c == '"':
			i++
			closed := false
			for ; i < len(s); i++ {
				if s[i] == '"' {
					closed = true
					break
				}
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
			if !closed {
				return nil, errors.New("unterminated double quote")
			}
			inWord = true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// imageEnv: the per-run registry access, with a cache so each ref is resolved
// once however many instances share it.

type registryImage struct {
	Fingerprint string
	Size        int64
}

type imageEnv struct {
	conf    *cliconfig.Config
	offline bool

	// the platform the server runs images for, asked of it once
	platOnce sync.Once
	plat     v1.Platform
	platErr  error

	mu      sync.Mutex
	images  map[string]registryResult
	runtime map[string]runtimeResult
}

// platformOf is the platform images are chosen for: the server's, not that of the machine asking. An environment is for one run against one
// server, so it is asked once.
func (e *imageEnv) platformOf(server incus.InstanceServer) (v1.Platform, error) {
	e.platOnce.Do(func() { e.plat, e.platErr = serverPlatform(server) })
	return e.plat, e.platErr
}

type registryResult struct {
	img registryImage
	err error
}

type runtimeResult struct {
	rt  ociRuntime
	err error
}

func newImageEnv(offline bool) *imageEnv {
	e := &imageEnv{offline: offline, images: map[string]registryResult{}, runtime: map[string]runtimeResult{}}
	if conf, err := incusconf.Load(); err == nil {
		e.conf = conf
	}
	return e
}

func (e *imageEnv) remotes() map[string]cliconfig.Remote {
	if e == nil || e.conf == nil {
		return nil
	}
	return e.conf.Remotes
}

// registryImage is what the registry says about remote:ref for the architecture the server runs images for.
func (e *imageEnv) registryImage(server incus.InstanceServer, remoteName, ref string) (registryImage, error) {
	if e == nil || e.offline {
		return registryImage{}, errOffline
	}
	remote, ok := e.remotes()[remoteName]
	if !ok {
		return registryImage{}, fmt.Errorf("%q is not a configured OCI remote", remoteName)
	}
	plat, err := e.platformOf(server)
	if err != nil {
		return registryImage{}, err
	}
	key := remoteName + ":" + ref
	e.mu.Lock()
	if hit, ok := e.images[key]; ok {
		e.mu.Unlock()
		return hit.img, hit.err
	}
	e.mu.Unlock()

	img, err := lookupRegistryImage(remote, ref, plat)
	e.mu.Lock()
	e.images[key] = registryResult{img: img, err: err}
	e.mu.Unlock()
	return img, err
}

// runtimeConfig reads the runtime config an image bakes in, for the architecture the server runs images for. The registry is reached as
// registry.go says: the login in the remote's URL, else the person's own container-registry login, else anonymously; an image it cannot
// read reports an error, which blocks a rebuild rather than guessing.
func (e *imageEnv) runtimeConfig(server incus.InstanceServer, remote cliconfig.Remote, ref string) (ociRuntime, error) {
	if e == nil || e.offline {
		return ociRuntime{}, errOffline
	}
	plat, err := e.platformOf(server)
	if err != nil {
		return ociRuntime{}, err
	}
	key := remoteHost(remote) + "/" + ref
	e.mu.Lock()
	if hit, ok := e.runtime[key]; ok {
		e.mu.Unlock()
		return hit.rt, hit.err
	}
	e.mu.Unlock()

	rt, err := lookupRuntimeConfig(remote, ref, plat)
	e.mu.Lock()
	e.runtime[key] = runtimeResult{rt: rt, err: err}
	e.mu.Unlock()
	return rt, err
}

// checkInstance runs checkImage against the live daemon and registry.
func (e *imageEnv) checkInstance(server incus.InstanceServer, current *api.Instance, r Resource) imageCheck {
	if r.Image == "" {
		return imageCheck{}
	}
	return checkImage(current.Config, r.Image, imageProbe{
		remotes: e.remotes(),
		aliasTarget: func(alias string) (string, bool, error) {
			a, _, found, err := incusapi.LookupImageAlias(server, alias)
			if err != nil || !found {
				return "", false, err
			}
			return a.Target, true, nil
		},
		registryFP: func(remote, ref string) (string, error) {
			img, err := e.registryImage(server, remote, ref)
			return img.Fingerprint, err
		},
	})
}
