package run

import (
	"net/http"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

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

func diskOnPool() *Spec {
	return &Spec{Devices: map[string]map[string]string{"data": {"type": "disk", "pool": "default", "source": "vol1", "path": "/data"}}}
}

func TestManagedVolumesAreCreatedOnlyWhenTheyAreReallyAbsent(t *testing.T) {
	f := &readFake{volErr: errNotFound}
	if err := ensureManagedVolumes(f, diskOnPool()); err != nil || len(f.created) != 1 {
		t.Errorf("a 404 is absent, so it is created: %v %v", err, f.created)
	}
	f = &readFake{}
	if err := ensureManagedVolumes(f, diskOnPool()); err != nil || len(f.created) != 0 {
		t.Errorf("an existing volume is left alone: %v %v", err, f.created)
	}
	f = &readFake{volErr: errForbidden}
	err := ensureManagedVolumes(f, diskOnPool())
	if err == nil || !strings.Contains(err.Error(), "checking whether managed volume default/vol1 exists") {
		t.Errorf("err = %v", err)
	}
	if len(f.created) != 0 {
		t.Errorf("a failed read is not 'absent': %v", f.created)
	}
}

func TestALocalImageReferenceFallsBackToAFingerprintOnlyForARealMiss(t *testing.T) {
	f := &readFake{aliasTarget: "abc123"}
	if _, img, err := resolveLocalImage(f, "web"); err != nil || img.Fingerprint != "abc123" {
		t.Errorf("a found alias resolves to its target: %v %+v", err, img)
	}
	f = &readFake{aliasErr: errNotFound}
	if _, img, err := resolveLocalImage(f, "abc123"); err != nil || img.Fingerprint != "abc123" {
		t.Errorf("a 404 means the reference is a bare fingerprint: %v %+v", err, img)
	}
	f = &readFake{aliasErr: errForbidden}
	if _, _, err := resolveLocalImage(f, "web"); err == nil {
		t.Error("a failed alias lookup is an error")
	}
	if len(f.fetchedImages) != 0 {
		t.Errorf("and it must not go on to treat the name as a fingerprint: %v", f.fetchedImages)
	}
}

func TestARootDiskIsNotAManagedVolume(t *testing.T) {
	// a root disk names a pool and no source: there is no volume to create for it, and "" is not a volume name
	spec := &Spec{Devices: map[string]map[string]string{
		"root": {"type": "disk", "path": "/", "pool": "default"},
		"data": {"type": "disk", "pool": "default", "source": "vol1", "path": "/data"},
	}}
	f := &readFake{volErr: errNotFound}
	if err := ensureManagedVolumes(f, spec); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0] != "default/vol1" {
		t.Errorf("only the data volume is created: %v", f.created)
	}
}
