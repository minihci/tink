package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/minihci/tink/internal/resolve"
)

// Stacks is the directory of stacks synced to the helper, one per name:
//
//	<dir>/<name>/v-<id>/   the stack's files, and stack.json listing which are stack files
//	<dir>/<name>/current   a symlink to the active version
//
// A new version is written beside the active one, finished with READY, and made active by replacing the `current`
// symlink, which is atomic: a scheduler tick sees the old stack or the new, never a mixture, and a stack that is
// still being written is invisible. Each stack is loaded on its own, so one that no longer parses never stops
// another.
type Stacks struct{ Dir string }

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

// ValidStackName reports whether name can be a stack's name.
func ValidStackName(name string) bool { return namePattern.MatchString(name) }

type stackMeta struct {
	Proto   int      `json:"proto"`
	Entries []string `json:"entries"`
	Synced  string   `json:"synced"`
}

// Stack is a loaded stack.
type Stack struct {
	Name      string
	Version   string
	Synced    time.Time
	Resources []resolve.Resource
}

// Sync stores files as the new active version of the stack `name`; entries are the files (by their path in files)
// that are stack YAML. Nothing changes if any step fails.
func (s Stacks) Sync(name string, files map[string][]byte, entries []string, now time.Time) error {
	if !ValidStackName(name) {
		return fmt.Errorf("%q is not a valid stack name (lower case letters, digits, '.', '_' and '-')", name)
	}
	if len(entries) == 0 {
		return errors.New("a stack needs at least one stack file")
	}
	cleaned := make([]string, 0, len(entries))
	for _, e := range entries {
		c, err := CleanBundlePath(e)
		if err != nil {
			return fmt.Errorf("stack file: %w", err)
		}
		if _, ok := files[e]; !ok {
			return fmt.Errorf("stack file %q is not among the files sent", e)
		}
		cleaned = append(cleaned, c)
	}
	stackDir := filepath.Join(s.Dir, name)
	if err := os.MkdirAll(stackDir, 0o700); err != nil {
		return err
	}
	version := "v-" + now.UTC().Format("20060102T150405Z") + "-" + NewID(now)[len("20060102T150405Z-"):]
	vdir := filepath.Join(stackDir, version)
	if err := writeFiles(vdir, files); err != nil {
		_ = os.RemoveAll(vdir)
		return err
	}
	meta, _ := json.Marshal(stackMeta{Proto: Proto, Entries: cleaned, Synced: now.UTC().Format(time.RFC3339)})
	if err := os.WriteFile(filepath.Join(vdir, "stack.json"), meta, 0o600); err != nil {
		_ = os.RemoveAll(vdir)
		return err
	}
	if err := os.WriteFile(filepath.Join(vdir, "READY"), nil, 0o600); err != nil {
		_ = os.RemoveAll(vdir)
		return err
	}
	// Prove it loads BEFORE it becomes the active stack: a stack that does not parse never replaces one that does.
	if _, err := loadVersion(name, vdir); err != nil {
		_ = os.RemoveAll(vdir)
		return fmt.Errorf("the stack does not load, so it was not activated: %w", err)
	}
	link := filepath.Join(stackDir, "current")
	tmp := link + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil { // relative, so the directory can be moved
		_ = os.RemoveAll(vdir)
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		_ = os.RemoveAll(vdir)
		return err
	}
	s.pruneVersions(stackDir, version)
	return nil
}

// pruneVersions keeps the active version and the one before it.
func (s Stacks) pruneVersions(stackDir, active string) {
	entries, err := os.ReadDir(stackDir)
	if err != nil {
		return
	}
	var old []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "v-") && e.Name() != active {
			old = append(old, e.Name())
		}
	}
	sort.Strings(old) // oldest first: names start with the time
	for len(old) > 1 {
		_ = os.RemoveAll(filepath.Join(stackDir, old[0]))
		old = old[1:]
	}
}

// Names lists the stacks that have an active version.
func (s Stacks) Names() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() || !ValidStackName(e.Name()) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(s.Dir, e.Name(), "current")); err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// Load loads one stack's active version. A reader can land in the instant a new version is activated (the symlink is
// being replaced) or just after the version it resolved was pruned; those fail with "not found" or "invalid argument"
// for a moment, and are retried a few times before being believed. A stack that does not parse is never retried.
func (s Stacks) Load(name string) (Stack, error) {
	var err error
	for attempt := 0; attempt < 5; attempt++ {
		var st Stack
		if st, err = s.loadOnce(name); err == nil || !transient(err) {
			return st, err
		}
		time.Sleep(time.Duration(attempt+1) * 10 * time.Millisecond)
	}
	return Stack{}, err
}

func transient(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.EINVAL)
}

func (s Stacks) loadOnce(name string) (Stack, error) {
	if !ValidStackName(name) {
		return Stack{}, fmt.Errorf("%q is not a valid stack name", name)
	}
	vdir, err := filepath.EvalSymlinks(filepath.Join(s.Dir, name, "current"))
	if err != nil {
		return Stack{}, fmt.Errorf("stack %q has no active version: %w", name, err)
	}
	return loadVersion(name, vdir)
}

func loadVersion(name, vdir string) (Stack, error) {
	if _, err := os.Stat(filepath.Join(vdir, "READY")); err != nil {
		return Stack{}, errors.New("not finished (no READY)")
	}
	b, err := os.ReadFile(filepath.Join(vdir, "stack.json"))
	if err != nil {
		return Stack{}, err
	}
	var meta stackMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return Stack{}, fmt.Errorf("stack.json: %w", err)
	}
	if meta.Proto != Proto {
		return Stack{}, fmt.Errorf("stack written for protocol %d: this helper speaks %d", meta.Proto, Proto)
	}
	st := Stack{Name: name, Version: filepath.Base(vdir)}
	st.Synced, _ = time.Parse(time.RFC3339, meta.Synced)
	for _, e := range meta.Entries {
		rs, err := resolve.LoadFileConfined(filepath.Join(vdir, e), vdir)
		if err != nil {
			return Stack{}, err
		}
		st.Resources = append(st.Resources, rs...)
	}
	return st, nil
}

// LoadAll loads every stack, one at a time. A stack that fails to load is reported by name and does not affect the
// others.
func (s Stacks) LoadAll() (ok []Stack, bad map[string]error) {
	names, err := s.Names()
	if err != nil {
		return nil, map[string]error{"": err}
	}
	bad = map[string]error{}
	for _, n := range names {
		st, err := s.Load(n)
		if err != nil {
			bad[n] = err
			continue
		}
		ok = append(ok, st)
	}
	return ok, bad
}
