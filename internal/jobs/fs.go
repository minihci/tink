package jobs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// FS is the part of a file system that a client of a jobs directory needs. Names are slash-separated and relative to the
// directory's root ("" is the root itself). The daemon that runs the jobs works on its own directory with the os package; a
// client on another machine reaches the same directory through the file API of the instance that holds it (see
// helper.InstanceFS), and the two must agree on the protocol in the package comment, which is why the client methods of
// Store are written against this and not against os.
type FS interface {
	// ReadFile returns the file's content. An absent file is an error that satisfies errors.Is(err, os.ErrNotExist).
	ReadFile(name string) ([]byte, error)
	// WriteFile creates or replaces a file. It does not create the directory it goes in.
	WriteFile(name string, data []byte) error
	// Mkdir creates a directory.
	Mkdir(name string) error
	// Remove removes a file, or a directory that is empty. Removing what is not there is not an error.
	Remove(name string) error
	// Exists reports whether the name is there.
	Exists(name string) (bool, error)
	// ReadDir returns the names in a directory (not their types). An absent directory is an os.ErrNotExist error.
	ReadDir(name string) ([]string, error)
}

// localFS is a directory on this machine.
type localFS struct{ root string }

func (l localFS) path(name string) string { return filepath.Join(l.root, filepath.FromSlash(name)) }

func (l localFS) ReadFile(name string) ([]byte, error) { return os.ReadFile(l.path(name)) }

func (l localFS) WriteFile(name string, data []byte) error {
	return os.WriteFile(l.path(name), data, 0o600)
}

// Mkdir also makes the root when it is missing: the first job of a new directory.
func (l localFS) Mkdir(name string) error { return os.MkdirAll(l.path(name), 0o700) }

func (l localFS) Remove(name string) error {
	if err := os.Remove(l.path(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (l localFS) Exists(name string) (bool, error) {
	_, err := os.Stat(l.path(name))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

func (l localFS) ReadDir(name string) ([]string, error) {
	entries, err := os.ReadDir(l.path(name))
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}
