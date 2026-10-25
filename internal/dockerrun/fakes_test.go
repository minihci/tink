package dockerrun

import (
	"net/http"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/instance"
)

// The fakes below are also in internal/instance's tests, which test the engine's own reads and writes. They are copied and not shared, so neither package
// has a test-only package to import (#216).

var (
	errNotFound  = api.StatusErrorf(http.StatusNotFound, "not found")
	errForbidden = api.StatusErrorf(http.StatusForbidden, "not authorized")
)

// readFake answers volume and image reads from its fields and records the writes and image fetches a test cares about. Any other
// method panics through the nil embedded interface.
type readFake struct {
	incus.InstanceServer
	volErr, aliasErr error  // what the read answers; nil means found
	imageErr         error  // what GetImage answers
	aliasTarget      string // what a found alias points at
	created          []string
	createdConfig    []map[string]string
	fetchedImages    []string
}

func (f *readFake) GetStoragePoolVolume(_, _, n string) (*api.StorageVolume, string, error) {
	return &api.StorageVolume{Name: n}, "", f.volErr
}

func (f *readFake) CreateStoragePoolVolume(pool string, v api.StorageVolumesPost) error {
	f.created = append(f.created, pool+"/"+v.Name)
	f.createdConfig = append(f.createdConfig, v.Config)
	return nil
}

func (f *readFake) GetImageAlias(n string) (*api.ImageAliasesEntry, string, error) {
	return &api.ImageAliasesEntry{Name: n, ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: f.aliasTarget}}, "", f.aliasErr
}

func (f *readFake) GetImage(fp string) (*api.Image, string, error) {
	f.fetchedImages = append(f.fetchedImages, fp)
	return &api.Image{Fingerprint: fp}, "", f.imageErr
}

func diskOnPool() *instance.Spec {
	return &instance.Spec{Devices: map[string]map[string]string{"data": {"type": "disk", "pool": "default", "source": "vol1", "path": "/data"}}}
}
