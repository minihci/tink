package resolve

import (
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	regremote "github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/lxc/incus/v7/shared/api"
)

// rebuildServer is a server that has no snapshots and a pool with the space it is given.
type rebuildServer struct {
	archServer
	free uint64
}

func (rebuildServer) GetInstanceSnapshotNames(string) ([]string, error) { return nil, nil }

func (s rebuildServer) GetStoragePoolResources(string) (*api.ResourcesStoragePool, error) {
	return &api.ResourcesStoragePool{Space: api.ResourcesStoragePoolSpace{Total: 1 << 40, Used: 1<<40 - s.free}}, nil
}

// pushWithConfig pushes a single-platform image whose config is cfg.
func pushWithConfig(t *testing.T, host, repoTag string, cfg v1.Config) {
	t.Helper()
	img, err := random.Image(1<<20, 2)
	if err != nil {
		t.Fatal(err)
	}
	if img, err = mutate.Config(img, cfg); err != nil {
		t.Fatal(err)
	}
	ref, err := name.ParseReference(host + "/" + repoTag)
	if err != nil {
		t.Fatal(err)
	}
	if err := regremote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
}

// rebuildPreflight reads the new image from the registry for the server's architecture: its runtime config, which a rebuild does not
// refresh, and its size, for the free-space check.
func TestRebuildPreflightReadsTheNewImageFromTheRegistry(t *testing.T) {
	host := newRegistry(t, nil)
	pushWithConfig(t, host, "team/app:2", v1.Config{Entrypoint: []string{"/run"}, Env: []string{"A=1"}})
	pushWithConfig(t, host, "team/app:3", v1.Config{Entrypoint: []string{"/run"}, Env: []string{"A=2"}}) // the environment changed
	instance := func() *api.Instance {
		return &api.Instance{
			Type: "container",
			InstancePut: api.InstancePut{Config: map[string]string{
				"image.type": "oci", "oci.entrypoint": "/run", "environment.A": "1",
			}},
			ExpandedDevices: map[string]map[string]string{
				"root": {"type": "disk", "pool": "default", "path": "/"},
				"data": {"type": "disk", "pool": "default", "source": "vol", "path": "/data"},
			},
		}
	}
	resource := func(tag string) Resource {
		return Resource{Kind: KindInstance, Name: "app", Image: "reg:team/app:" + tag}
	}
	roomy := rebuildServer{archServer{archs: []string{"x86_64"}}, 100 << 30}

	if p := rebuildPreflight(roomy, instance(), resource("2"), envFor(host)); len(p.Blockers) != 0 || len(p.Warnings) != 0 {
		t.Errorf("an image with the instance's own runtime config, and room for it: %+v", p)
	}
	p := rebuildPreflight(roomy, instance(), resource("3"), envFor(host))
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "the new image changes runtime config") || !strings.Contains(p.Blockers[0], "environment.A") {
		t.Errorf("a rebuild does not refresh the environment, so an image that changes it is blocked, naming the key: %+v", p)
	}
	tight := rebuildServer{archServer{archs: []string{"x86_64"}}, 4 << 20}
	p = rebuildPreflight(tight, instance(), resource("2"), envFor(host))
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], `pool "default" has 4 MiB free`) {
		t.Errorf("the size of the new image is read from the registry and the pool's space is checked against it: %+v", p)
	}
	p = rebuildPreflight(roomy, instance(), resource("missing"), envFor(host))
	if len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "could not read the new image's runtime config") ||
		len(p.Warnings) != 1 || !strings.Contains(p.Warnings[0], "could not size the new image") {
		t.Errorf("an image the registry does not have: %+v", p)
	}
	off := envFor(host)
	off.offline = true
	if p := rebuildPreflight(roomy, instance(), resource("2"), off); len(p.Blockers) != 1 || !strings.Contains(p.Blockers[0], "--offline") {
		t.Errorf("offline cannot read the image, so a rebuild is blocked: %+v", p)
	}
}
