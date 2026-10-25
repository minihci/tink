package incusapi

import (
	"errors"
	"net/http"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// volServer is a custom volume with a config and an etag that changes on every write, as Incus's does. conflicts is how many writes are
// refused with a 412 first, as if someone else had written between the read and the write.
type volServer struct {
	incus.InstanceServer
	project   string
	vol       api.StorageVolume
	etag      int
	conflicts int
	updates   []api.StorageVolumePut
	etags     []string
	getErr    error
	putErr    error
}

func (s *volServer) UseProject(p string) incus.InstanceServer { s.project = p; return s }

func (s *volServer) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	if s.getErr != nil {
		return nil, "", s.getErr
	}
	if pool != "fast" || volType != "custom" || name != "helper-status" {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	cp := s.vol
	cp.Config = map[string]string{}
	for k, v := range s.vol.Config {
		cp.Config[k] = v
	}
	return &cp, string(rune('a' + s.etag)), nil
}

func (s *volServer) UpdateStoragePoolVolume(_, _, _ string, put api.StorageVolumePut, etag string) error {
	s.updates = append(s.updates, put)
	s.etags = append(s.etags, etag)
	if s.putErr != nil {
		return s.putErr
	}
	if s.conflicts > 0 {
		s.conflicts--
		s.etag++ // someone else got in first
		return api.StatusErrorf(http.StatusPreconditionFailed, "ETag doesn't match")
	}
	s.vol.StorageVolumePut = put
	s.etag++
	return nil
}

func TestMergeVolumeConfigSetsTheKeysItNamesAndKeepsEveryOtherOne(t *testing.T) {
	s := &volServer{vol: api.StorageVolume{StorageVolumePut: api.StorageVolumePut{
		Description: "the helper's status",
		Config:      map[string]string{"user.mine": "keep", "user.tink.helper.status": "old", "volatile.uuid": "abc"},
	}}}
	if err := MergeVolumeConfig(s, "tink-helper", "fast", "helper-status", map[string]string{"user.tink.helper.status": "new"}); err != nil {
		t.Fatal(err)
	}
	if s.project != "tink-helper" {
		t.Errorf("project = %q: the volume is in the helper's project", s.project)
	}
	if len(s.updates) != 1 {
		t.Fatalf("one write: %d", len(s.updates))
	}
	got := s.updates[0]
	if got.Config["user.tink.helper.status"] != "new" {
		t.Errorf("the key is set: %v", got.Config)
	}
	if got.Config["user.mine"] != "keep" || got.Config["volatile.uuid"] != "abc" || len(got.Config) != 3 {
		t.Errorf("every other key is carried through: %v", got.Config)
	}
	if got.Description != "the helper's status" {
		t.Errorf("and the description: %q", got.Description)
	}
	if s.etags[0] != "a" {
		t.Errorf("the write carries the ETag that was read: %q", s.etags[0])
	}
}

func TestMergeVolumeConfigReadsAgainWhenSomeoneWroteInBetween(t *testing.T) {
	s := &volServer{conflicts: 1, vol: api.StorageVolume{StorageVolumePut: api.StorageVolumePut{Config: map[string]string{"user.mine": "keep"}}}}
	if err := MergeVolumeConfig(s, "", "fast", "helper-status", map[string]string{"user.tink.helper.status": "new"}); err != nil {
		t.Fatal(err)
	}
	if len(s.updates) != 2 || s.etags[0] == s.etags[1] {
		t.Errorf("the refused write is redone against what is there now: %d writes, etags %v", len(s.updates), s.etags)
	}
	if s.vol.Config["user.tink.helper.status"] != "new" || s.vol.Config["user.mine"] != "keep" {
		t.Errorf("%v", s.vol.Config)
	}
	if s.project != "" {
		t.Errorf("no project given means the connection's own: %q", s.project)
	}

	// a volume that keeps changing is given up on, not retried forever
	s = &volServer{conflicts: 10, vol: api.StorageVolume{}}
	err := MergeVolumeConfig(s, "", "fast", "helper-status", map[string]string{"user.a": "b"})
	if err == nil || !api.StatusErrorCheck(err, http.StatusPreconditionFailed) || len(s.updates) != conflictAttempts {
		t.Errorf("err = %v after %d writes", err, len(s.updates))
	}
}

func TestMergeVolumeConfigReturnsTheOriginalErrorWithTheVolumeNamed(t *testing.T) {
	denied := api.StatusErrorf(http.StatusForbidden, "not authorized")
	s := &volServer{putErr: denied}
	err := MergeVolumeConfig(s, "", "fast", "helper-status", map[string]string{"user.a": "b"})
	if err == nil || !errors.Is(err, denied) || len(s.updates) != 1 {
		t.Errorf("a refusal that is not a conflict is not retried, and comes back: %v (%d writes)", err, len(s.updates))
	}
	s = &volServer{getErr: denied}
	if err := MergeVolumeConfig(s, "", "fast", "helper-status", nil); err == nil || !errors.Is(err, denied) || len(s.updates) != 0 {
		t.Errorf("an unreadable volume is never written: %v", err)
	}
	if err := MergeVolumeConfig(&volServer{}, "", "fast", "nope", nil); err == nil || !IsNotFound(err) {
		t.Errorf("a volume that is not there is an error, not a silent create: %v", err)
	}
}
