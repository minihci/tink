package incusapi

import (
	"io"
	"net/http"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// IsNotFound reports whether err is Incus saying the object does not exist (an HTTP 404).
//
// It is the only error that may be read as "absent". A 403 (a revoked or restricted client), a 5xx or a dropped connection says
// nothing about whether the object exists, and acting as if it did not (planning a create, skipping a delete, overwriting a file)
// is the mistake this package exists to make hard to write.
func IsNotFound(err error) bool { return api.StatusErrorCheck(err, http.StatusNotFound) }

// The Lookup functions are for asking "does this exist?". Each returns (object, etag, found, err):
//
//   - found, nil error: the object, and its etag where Incus gives one;
//   - not found, nil error: Incus answered 404, so it is not there;
//   - an error: anything else. found is false then too, and it must NOT be read as absent.
//
// The result has three states and so does the signature, so a caller cannot write `exists := err == nil`: it has to say what it does
// with err. A read where a miss is simply an error (fetching an object that has to be there) keeps using the client's Get directly.

func lookup[T any](obj *T, etag string, err error) (*T, string, bool, error) {
	switch {
	case err == nil:
		return obj, etag, true, nil
	case IsNotFound(err):
		return nil, "", false, nil
	default:
		return nil, "", false, err
	}
}

// LookupInstance looks up an instance by name.
func LookupInstance(s incus.InstanceServer, name string) (*api.Instance, string, bool, error) {
	o, e, err := s.GetInstance(name)
	return lookup(o, e, err)
}

// LookupProfile looks up a profile by name.
func LookupProfile(s incus.InstanceServer, name string) (*api.Profile, string, bool, error) {
	o, e, err := s.GetProfile(name)
	return lookup(o, e, err)
}

// LookupProject looks up a project by name.
func LookupProject(s incus.InstanceServer, name string) (*api.Project, string, bool, error) {
	o, e, err := s.GetProject(name)
	return lookup(o, e, err)
}

// LookupVolume looks up a storage volume ("custom" for the volumes tink manages).
func LookupVolume(s incus.InstanceServer, pool, volType, name string) (*api.StorageVolume, string, bool, error) {
	o, e, err := s.GetStoragePoolVolume(pool, volType, name)
	return lookup(o, e, err)
}

// LookupImageAlias looks up an image alias.
func LookupImageAlias(s incus.ImageServer, name string) (*api.ImageAliasesEntry, string, bool, error) {
	o, e, err := s.GetImageAlias(name)
	return lookup(o, e, err)
}

// LookupImage looks up an image by fingerprint.
func LookupImage(s incus.ImageServer, fingerprint string) (*api.Image, string, bool, error) {
	o, e, err := s.GetImage(fingerprint)
	return lookup(o, e, err)
}

// LookupInstanceFile opens a file in an instance. When found the caller must close the reader.
func LookupInstanceFile(s incus.InstanceServer, instance, path string) (io.ReadCloser, *incus.InstanceFileResponse, bool, error) {
	rc, resp, err := s.GetInstanceFile(instance, path)
	switch {
	case err == nil:
		return rc, resp, true, nil
	case IsNotFound(err):
		return nil, nil, false, nil
	default:
		return nil, nil, false, err
	}
}
