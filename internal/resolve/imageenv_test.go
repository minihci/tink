package resolve

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
)

// envFor is an image environment whose only OCI remote, "reg", is the registry at host.
func envFor(host string) *imageEnv { return envAs("reg", host) }

// envAs is the same with the remote under another name, which is what stops a lookup that hard-codes a name from passing by luck.
func envAs(remoteName, host string) *imageEnv {
	conf := &cliconfig.Config{Remotes: map[string]cliconfig.Remote{remoteName: remoteAt(host)}}
	return &imageEnv{conf: conf, images: map[string]registryResult{}, runtime: map[string]runtimeResult{}}
}

// The whole point of asking the registry here, and for the server's architecture: an instance built on an amd64 server is up to date
// when the server is amd64, whatever machine asks, and the same instance is drift on an arm64 server, because the arm64 image is another
// image. The answer follows the server, not the laptop.
func TestTheDriftCheckAsksForTheServersArchitectureNotTheClients(t *testing.T) {
	host := newRegistry(t, nil)
	imgs, _ := pushIndex(t, host, "team/app:1", "amd64", "arm64")
	built := want(t, imgs["amd64"]).Fingerprint
	instance := map[string]string{"image.id": "team/app:1", "image.description": host + "/team/app (OCI)", "volatile.base_image": built}
	res := Resource{Kind: KindInstance, Name: "app", Image: "mirror:team/app:1"}

	onAmd64 := envAs("mirror", host).checkInstance(archServer{archs: []string{"x86_64"}}, &api.Instance{InstancePut: api.InstancePut{Config: instance}}, res)
	if len(onAmd64.Drift) != 0 || len(onAmd64.Unverified) != 0 {
		t.Errorf("an instance built from the amd64 image, on an amd64 server, is current: %+v", onAmd64)
	}
	onArm64 := envAs("mirror", host).checkInstance(archServer{archs: []string{"aarch64"}}, &api.Instance{InstancePut: api.InstancePut{Config: instance}}, res)
	if len(onArm64.Drift) != 1 {
		t.Errorf("the same instance recorded on an arm64 server was not built from the arm64 image, and that is drift: %+v", onArm64)
	}
}

func TestAMovedTagIsDriftAndAnUnmovedOneIsNot(t *testing.T) {
	host := newRegistry(t, nil)
	srv := archServer{archs: []string{"x86_64"}}
	res := Resource{Kind: KindInstance, Name: "app", Image: "reg:team/app:1"}
	first, _ := pushIndex(t, host, "team/app:1", "amd64")
	instance := func() *api.Instance {
		return &api.Instance{InstancePut: api.InstancePut{Config: map[string]string{
			"image.id": "team/app:1", "image.description": host + "/team/app (OCI)", "volatile.base_image": want(t, first["amd64"]).Fingerprint,
		}}}
	}
	if c := envFor(host).checkInstance(srv, instance(), res); len(c.Drift) != 0 || len(c.Unverified) != 0 {
		t.Errorf("unmoved: %+v", c)
	}
	pushIndex(t, host, "team/app:1", "amd64") // the tag now points at other content
	if c := envFor(host).checkInstance(srv, instance(), res); len(c.Drift) != 1 || !strings.Contains(c.Drift[0], "resolves to") {
		t.Errorf("moved: %+v", c)
	}
}

// counting wraps a registry to count what it is asked.
func counting(n *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

// countingServer is a server that says what it runs, and how often it was asked.
type countingServer struct {
	archServer
	asked *atomic.Int64
}

func (s countingServer) GetServer() (*api.Server, string, error) {
	s.asked.Add(1)
	return s.archServer.GetServer()
}

func TestTheServerIsAskedOnceAndEachImageIsLookedUpOnce(t *testing.T) {
	var registryAsked, serverAsked atomic.Int64
	host := newRegistry(t, counting(&registryAsked))
	pushIndex(t, host, "team/app:1", "amd64")
	pushIndex(t, host, "team/other:1", "amd64")
	env := envFor(host)
	srv := countingServer{archServer{archs: []string{"x86_64"}}, &serverAsked}

	if _, err := env.registryImage(srv, "reg", "team/app:1"); err != nil {
		t.Fatal(err)
	}
	afterFirst := registryAsked.Load()
	for i := 0; i < 3; i++ { // instances sharing an image, and the same image asked again
		if _, err := env.registryImage(srv, "reg", "team/app:1"); err != nil {
			t.Fatal(err)
		}
	}
	if registryAsked.Load() != afterFirst {
		t.Errorf("an image already looked up is not looked up again: %d requests, %d after the first", registryAsked.Load(), afterFirst)
	}
	if _, err := env.registryImage(srv, "reg", "team/other:1"); err != nil {
		t.Fatal(err)
	}
	if registryAsked.Load() == afterFirst {
		t.Error("another image is another lookup")
	}
	if serverAsked.Load() != 1 {
		t.Errorf("the server is asked for its architecture once per run, not per image: %d", serverAsked.Load())
	}

	// the same goes for the runtime config
	before := registryAsked.Load()
	remote := env.remotes()["reg"]
	for i := 0; i < 2; i++ {
		if _, err := env.runtimeConfig(srv, remote, "team/app:1"); err != nil {
			t.Fatal(err)
		}
	}
	once := registryAsked.Load() - before
	if _, err := env.runtimeConfig(srv, remote, "team/app:1"); err != nil || registryAsked.Load()-before != once {
		t.Errorf("a runtime config already read is not read again (%d requests for the first, %d after)", once, registryAsked.Load()-before)
	}
}

func TestWhyAnImageCouldNotBeAskedAboutIsSaid(t *testing.T) {
	var registryAsked atomic.Int64
	host := newRegistry(t, counting(&registryAsked))
	pushIndex(t, host, "team/app:1", "amd64")
	registryAsked.Store(0) // what the test itself pushed does not count
	srv := archServer{archs: []string{"x86_64"}}

	if _, err := envFor(host).registryImage(srv, "nope", "team/app:1"); err == nil || !strings.Contains(err.Error(), `"nope" is not a configured OCI remote`) {
		t.Errorf("an unknown remote: %v", err)
	}
	if _, err := envFor(host).registryImage(archServer{archs: []string{"vax"}}, "reg", "team/app:1"); err == nil || !strings.Contains(err.Error(), `"vax"`) {
		t.Errorf("a server whose architecture has no OCI platform is an error, not a guess: %v", err)
	}
	if _, err := envFor(host).registryImage(archServer{err: errors.New("boom")}, "reg", "team/app:1"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("a server that cannot be read: %v", err)
	}
	if registryAsked.Load() != 0 {
		t.Errorf("none of those reached the registry: %d requests", registryAsked.Load())
	}

	off := envFor(host)
	off.offline = true
	if _, err := off.registryImage(srv, "reg", "team/app:1"); !errors.Is(err, errOffline) {
		t.Errorf("offline: %v", err)
	}
	if _, err := off.runtimeConfig(srv, off.remotes()["reg"], "team/app:1"); !errors.Is(err, errOffline) {
		t.Errorf("offline: %v", err)
	}
	if registryAsked.Load() != 0 {
		t.Errorf("offline never touches the registry: %d requests", registryAsked.Load())
	}
}
