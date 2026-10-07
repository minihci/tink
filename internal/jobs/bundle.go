package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/resolve"
)

// MaxBundleBytes bounds what a bundle may carry. A stack is YAML and a few small files; anything near this is a mistake
// (a disk image named as a source_path), and shipping it through the file API would be slow and pointless.
var MaxBundleBytes int64 = 16 << 20

// Bundle is a stack and every file it reads, with paths relative to one root so that the stack's own relative references
// (`source_path: ../shared/x`) resolve the same way where it is delivered.
type Bundle struct {
	// Files maps a slash-separated relative path to its content.
	Files map[string][]byte
	// Entries are the stack files among Files, in the order given.
	Entries []string
	// Root is the directory the paths are relative to, on the machine the bundle was built on.
	Root string
}

// BuildBundle loads the stacks at paths (as an operator would, recording what the load reads) and packs them. It does
// not ship kind: image sources: they are not read at load time and a helper has no use for them; one that points
// outside the bundle's tree is an error, since it could not be confined once delivered.
func BuildBundle(paths []string) (Bundle, error) {
	if len(paths) == 0 {
		paths = []string{resolve.DefaultFile}
	}
	_, deps, err := resolve.LoadFilesRecording(paths)
	if err != nil {
		return Bundle{}, err
	}
	var files []string
	seen := map[string]bool{}
	for _, f := range deps.Files {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	root := commonDir(files)
	if root == "" {
		return Bundle{}, errors.New("the stack files have no common directory")
	}
	for _, src := range deps.ImageSources {
		if !isInside(root, src) {
			return Bundle{}, fmt.Errorf("an image source (%s) is outside the stack's directory tree (%s): it cannot be shipped, and a helper has no use for it", src, root)
		}
	}
	b := Bundle{Files: map[string][]byte{}, Root: root}
	var total int64
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			return Bundle{}, err
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return Bundle{}, err
		}
		if total += int64(len(data)); total > MaxBundleBytes {
			return Bundle{}, fmt.Errorf("the stack and the files it reads add up to more than %d bytes: is a large file named as a source_path?", MaxBundleBytes)
		}
		b.Files[filepath.ToSlash(rel)] = data
	}
	given := map[string]bool{}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return Bundle{}, err
		}
		rel, err := filepath.Rel(root, filepath.Clean(abs))
		if err != nil {
			return Bundle{}, err
		}
		key := filepath.ToSlash(rel)
		if !given[key] {
			given[key] = true
			b.Entries = append(b.Entries, key)
		}
	}
	return b, nil
}

// commonDir is the deepest directory that contains every file, or "" if there is none.
func commonDir(files []string) string {
	if len(files) == 0 {
		return ""
	}
	parts := func(p string) []string { return strings.Split(filepath.Dir(p), string(filepath.Separator)) }
	common := parts(files[0])
	for _, f := range files[1:] {
		p := parts(f)
		n := 0
		for n < len(common) && n < len(p) && common[n] == p[n] {
			n++
		}
		common = common[:n]
	}
	if len(common) == 0 {
		return ""
	}
	dir := strings.Join(common, string(filepath.Separator))
	if dir == "" {
		dir = string(filepath.Separator)
	}
	return dir
}

func isInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Names returns the bundle's file names, sorted (stable for tests and for display).
func (b Bundle) Names() []string {
	out := make([]string, 0, len(b.Files))
	for n := range b.Files {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
