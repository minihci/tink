package resolve

// The directories above a kind: file that are not there yet. See #140 and docs/first-start-files-design.md, "Parent directories".
//
// Incus's file API writes into a directory that exists and answers 404 for one that does not (an image that has no /config, a volume that has no
// .storage). A file resource used to fail on that, and the instance it was meant for went on without the file, often crash-looping on the absence
// (found in #181). So when a push is answered with not-found, the directories above the file are made, top down, and the push is tried once more. The
// common case, every parent in place, costs no extra call.
//
// Only a failed push makes directories, never a plan or a push that worked, and only the ones that are missing: an existing directory is not touched, so its
// mode and owner stay whatever the app or the image gave it. A directory tink makes is 0755 and owned as the client's defaults give (root), which a
// non-root app cannot write beside; that is the neighbouring problem of #176 and is not fixed here.

import (
	"fmt"
	"path"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// dirOps is what makeParents needs of a place that holds files: the instance's own file API, or a custom volume's.
type dirOps struct {
	// stat says whether p exists and, if it does, whether it is a directory. An error is anything other than absence.
	stat func(p string) (exists, isDir bool, err error)
	// mkdir makes the one directory p, whose parent exists.
	mkdir func(p string) error
}

// makeParents makes the directories above file that do not exist, top down, and returns the ones it made. file is absolute and cleaned. The root is
// taken to exist. It stops at the first ancestor that is there; one that is a file is an error, since nothing can go below it.
func makeParents(file string, ops dirOps) ([]string, error) {
	var missing []string
	for d := path.Dir(file); d != "/" && d != "."; d = path.Dir(d) {
		exists, isDir, err := ops.stat(d)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", d, err)
		}
		if exists {
			if !isDir {
				return nil, fmt.Errorf("%s is not a directory", d)
			}
			break
		}
		missing = append(missing, d)
	}
	var made []string
	for i := len(missing) - 1; i >= 0; i-- {
		if err := ops.mkdir(missing[i]); err != nil {
			return made, fmt.Errorf("creating directory %s: %w", missing[i], err)
		}
		made = append(made, missing[i])
	}
	return made, nil
}

// pushCreatingParents calls push; if the server answers not-found it makes the missing parents of file with ops and calls push once more. The error of a
// push that failed for another reason, or whose parents were all in place (so the not-found was about something else, an instance that is gone), is
// returned as it came. A failure to make a directory is returned as that failure, naming the directory.
func pushCreatingParents(file string, ops dirOps, push func() error) error {
	err := push()
	if err == nil || !incusapi.IsNotFound(err) {
		return err
	}
	made, mkErr := makeParents(file, ops)
	if mkErr != nil {
		return fmt.Errorf("its parent directory is missing and could not be made: %w", mkErr)
	}
	if len(made) == 0 {
		return err
	}
	return push()
}

// instanceDirOps is dirOps over an instance's file API. s must be scoped to the instance's project.
func instanceDirOps(s incus.InstanceServer, instance string) dirOps {
	return dirOps{
		stat: func(p string) (bool, bool, error) {
			rc, resp, found, err := incusapi.LookupInstanceFile(s, instance, p)
			if rc != nil {
				_ = rc.Close()
			}
			if err != nil || !found {
				return false, false, err
			}
			return true, resp != nil && resp.Type == "directory", nil
		},
		mkdir: func(p string) error {
			return s.CreateInstanceFile(instance, p, incus.InstanceFileArgs{Type: "directory", Mode: 0o755})
		},
	}
}

// volumeDirOps is dirOps over a custom volume's file API; paths are relative to the volume's root. s must be scoped to the volume's project.
func volumeDirOps(s incus.InstanceServer, pool, volume string) dirOps {
	return dirOps{
		stat: func(p string) (bool, bool, error) {
			rc, resp, found, err := incusapi.LookupVolumeFile(s, pool, volume, p)
			if rc != nil {
				_ = rc.Close()
			}
			if err != nil || !found {
				return false, false, err
			}
			return true, resp != nil && resp.Type == "directory", nil
		},
		mkdir: func(p string) error {
			return s.CreateStorageVolumeFile(pool, "custom", volume, p, incus.InstanceFileArgs{Type: "directory", Mode: 0o755})
		},
	}
}
