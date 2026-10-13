package resolve

// Files under a disk-device mount. See docs/first-start-files-design.md, "Files under a mount".
//
// A disk device mounts something over part of an instance's filesystem: a custom storage volume at /config, a host directory at /media. What the
// instance's own root filesystem holds at that path is hidden once the device is mounted, and that happens at the instance's start. A file pushed to the
// stopped instance at /config/configuration.yaml therefore lands in the rootfs directory underneath the mount and is never seen; the file resource then
// pushed it again after the start, and the application read its config only if that second push beat its first boot (found live, 2026-10-13: Home
// Assistant won by two seconds).
//
// So before a file is placed, its path is checked against the disk devices the instance will have. A file at or below a mount path is not pushed into
// the rootfs. If the mount is a custom storage volume, the file is written into the volume itself instead (Incus has a file API for volumes, and the
// volume exists by then: the instance waits for it), which is exactly where the started instance finds it. Any other mount (a host directory, a disk that
// is not a plain volume) is left to the file's own turn after the start, and the apply and the plan say so.

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// mount is a disk device that puts something of its own over part of an instance's filesystem.
type mount struct {
	Device string // the disk device's name
	Path   string // where it is mounted inside the instance
	Pool   string // the storage pool, when the source is a custom volume
	Volume string // the custom volume's name; empty when the source is not one
	Host   string // the host directory, when the source is one
}

// describe is what the mount is, for a sentence.
func (m mount) describe() string {
	switch {
	case m.Volume != "":
		return "a mounted volume"
	case m.Host != "":
		return "a mounted host directory"
	}
	return "a mounted disk"
}

// holds reports whether filePath is the mount path itself or below it. Both must be clean: "/configuration.yaml" is not under "/config".
func (m mount) holds(filePath string) bool {
	return filePath == m.Path || strings.HasPrefix(filePath, m.Path+"/")
}

// inVolume is filePath as the volume sees it: relative to the volume's root, with the leading slash the volume file API takes.
func (m mount) inVolume(filePath string) string { return strings.TrimPrefix(filePath, m.Path) }

// profileDevices says what devices a profile gives an instance, and whether that could be found out at all. A profile that cannot be read
// contributes nothing: placing then behaves as it did before mounts were considered, and is never made to fail by it.
type profileDevices func(name string) (devices map[string]map[string]string, ok bool)

// profileLookup reads a profile the stack declares from the stack (that is what apply will make it), and any other from the server. server may be nil, for
// a caller that has none: only the stack's profiles are then known.
func profileLookup(server incus.InstanceServer, declared map[string]Resource) profileDevices {
	return func(name string) (map[string]map[string]string, bool) {
		if p, ok := declared[name]; ok {
			return p.Devices, true
		}
		if server == nil {
			return nil, false
		}
		p, _, found, err := incusapi.LookupProfile(server, name)
		if err != nil || !found {
			return nil, false
		}
		return p.Devices, true
	}
}

// mountsOf lists the mounts an instance will have, deepest path first: the disk devices of its profiles, then its own, a device of the same name
// replacing the whole of an earlier one as Incus does. An instance that names no profile gets Incus's "default". The root disk (path "/") is not a
// mount: it hides nothing.
func mountsOf(inst Resource, profiles profileDevices) []mount {
	devices := map[string]map[string]string{}
	names := inst.Profiles
	if len(names) == 0 {
		names = []string{"default"}
	}
	for _, p := range names {
		if devs, ok := profiles(p); ok {
			for name, d := range devs {
				devices[name] = d
			}
		}
	}
	for name, d := range inst.Devices {
		devices[name] = d
	}

	var out []mount
	for name, d := range devices {
		if d["type"] != "disk" {
			continue
		}
		p := path.Clean(d["path"])
		if p == "/" || !strings.HasPrefix(p, "/") { // the root disk, or a disk that is not mounted at a path
			continue
		}
		m := mount{Device: name, Path: p}
		src := d["source"]
		switch {
		case strings.HasPrefix(src, "/"):
			m.Host = src
		case src != "" && d["pool"] != "" && !strings.Contains(src, "/"):
			m.Pool, m.Volume = d["pool"], src // the same test as a data volume elsewhere (see hasDataVolume)
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].Path) != len(out[j].Path) {
			return len(out[i].Path) > len(out[j].Path)
		}
		return out[i].Device < out[j].Device
	})
	return out
}

// mountOver is the mount that holds filePath, the deepest if mounts are nested.
func mountOver(mounts []mount, filePath string) (mount, bool) {
	for _, m := range mounts { // deepest first
		if m.holds(filePath) {
			return m, true
		}
	}
	return mount{}, false
}

// placeAt is a file to place before the first start, and where.
type placeAt struct {
	File  Resource
	Mount *mount // nil: the instance's own filesystem; else the mount whose volume the file is written into
}

// skippedFile is a file that was a candidate for placing before the first start and is left to its own turn after the start, and why, as the
// continuation of "not placed before the first start, ...".
type skippedFile struct {
	File Resource
	Why  string
}

// placement is what creating an instance does with its candidate files.
type placement struct {
	Place   []placeAt     // in declaration order
	Skipped []skippedFile // the files a mount hides, in declaration order
}

// files are the files to place, in order.
func (p placement) files() []Resource {
	out := make([]Resource, len(p.Place))
	for i, pa := range p.Place {
		out[i] = pa.File
	}
	return out
}

// placeBy sorts the candidates (the files firstStartFiles chose) by where they would land. A file outside every mount is pushed into the instance as
// before. One under a custom volume's mount is written into that volume. One under any other mount, or at the mount path itself, is skipped. A file
// that waits for a skipped file waits for the start too (it is left out without a word: nothing hides it), the same rule as a file that waits for an exec.
func placeBy(inst Resource, candidates []Resource, profiles profileDevices) placement {
	mounts := mountsOf(inst, profiles)
	var pl placement
	left := map[string]bool{} // by name: the files that wait for their own turn
	for _, f := range candidates {
		fp := path.Clean(f.Path)
		m, under := mountOver(mounts, fp)
		switch {
		case !under:
			pl.Place = append(pl.Place, placeAt{File: f})
		case m.Volume != "" && fp != m.Path:
			pl.Place = append(pl.Place, placeAt{File: f, Mount: &m})
		default:
			pl.Skipped = append(pl.Skipped, skippedFile{File: f, Why: fmt.Sprintf("because %s is %s (device %s)", m.Path, m.describe(), m.Device)})
			left[f.Name] = true
		}
	}
	for changed := len(left) > 0; changed; {
		changed = false
		var kept []placeAt
		for _, pa := range pl.Place {
			waits := false
			for _, d := range pa.File.DependsOn {
				if left[d] {
					waits = true
					break
				}
			}
			if waits {
				left[pa.File.Name] = true
				changed = true
			} else {
				kept = append(kept, pa)
			}
		}
		pl.Place = kept
	}
	return pl
}

// skippedNote is what an apply, and a plan, says about a file that was not placed before its instance's first start.
func skippedNote(s skippedFile) string {
	return "file/" + s.File.Name + ": not placed before the first start, " + s.Why + "; it is written right after the start, so the app may read its config first"
}

// firstStartNotes are the notes for the plan of an instance that does not exist yet: the files that will not be placed before its first start. The
// plan can say what a mount hides; whether a write into a volume will work it finds out when it happens.
func firstStartNotes(server incus.InstanceServer, r Resource, opts PlanOptions) []string {
	candidates := opts.firstStart[instanceKey(r.Project, r.Name)]
	if len(candidates) == 0 {
		return nil
	}
	var notes []string
	for _, s := range placeBy(r, candidates, profileLookup(server, opts.profiles)).Skipped {
		notes = append(notes, skippedNote(s))
	}
	return notes
}

// firstStartOutcome is what creating an instance did with its files before the first start.
type firstStartOutcome struct {
	Placed  []string // the files in place, as placedNote lists them: the file's name, and the volume it went into when it did
	Skipped []skippedFile
}

// lines are what an apply says after "instance/NAME: created": the files placed, then each one that was not and why. Only a file that is in place is
// ever called placed.
func (o firstStartOutcome) lines(instance string) []string {
	var out []string
	if len(o.Placed) > 0 {
		out = append(out, placedNote(instance, o.Placed))
	}
	for _, s := range o.Skipped {
		out = append(out, skippedNote(s))
	}
	return out
}

// placer carries a placement out against a server, and remembers which writes into a volume failed, so they can be said. A failed write is not a new way
// to fail (see createInOrder): the file waits for its own turn, as it always did.
type placer struct {
	server incus.InstanceServer
	pl     placement
	mounts map[string]*mount // by file name; absent for a file that goes into the instance
	failed []skippedFile
}

func newPlacer(server incus.InstanceServer, pl placement) *placer {
	p := &placer{server: server, pl: pl, mounts: map[string]*mount{}}
	for _, pa := range pl.Place {
		if pa.Mount != nil {
			p.mounts[pa.File.Name] = pa.Mount
		}
	}
	return p
}

// place puts one file where the placement says.
func (p *placer) place(f Resource) error {
	m := p.mounts[f.Name]
	if m == nil {
		return placeFile(p.server, f)
	}
	err := placeVolumeFile(p.server, f, *m)
	if err != nil {
		p.failed = append(p.failed, skippedFile{File: f, Why: "because " + err.Error()})
	}
	return err
}

// outcome is what happened, given the names of the files that were placed.
func (p *placer) outcome(placed []string) firstStartOutcome {
	out := firstStartOutcome{Skipped: append(append([]skippedFile(nil), p.pl.Skipped...), p.failed...)}
	for _, name := range placed {
		if m := p.mounts[name]; m != nil {
			name += " (in volume " + m.Pool + "/" + m.Volume + ")"
		}
		out.Placed = append(out.Placed, name)
	}
	return out
}

// placeVolumeFile writes f's content to its path inside the custom volume mounted over it: the same file resource, converged, not a seed. It reads what
// the volume holds and writes only when that differs, so a file already right is left untouched. It writes as placeFile does (mode 0644, owner as the
// client's defaults give) and creates no directories: the parent must be in the volume already. The path is relative to the volume's root.
func placeVolumeFile(server incus.InstanceServer, f Resource, m mount) error {
	s := scopedServer(server, f)
	rel := m.inVolume(path.Clean(f.Path))
	vol := m.Pool + "/" + m.Volume

	rc, _, found, err := incusapi.LookupVolumeFile(s, m.Pool, m.Volume, rel)
	if err != nil {
		return fmt.Errorf("reading %s in volume %s failed: %w", rel, vol, err)
	}
	if found {
		if rc == nil {
			return fmt.Errorf("%s in volume %s is a directory", rel, vol)
		}
		current, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("reading %s in volume %s failed: %w", rel, vol, err)
		}
		if string(current) == f.Content {
			return nil
		}
	}

	err = s.CreateStorageVolumeFile(m.Pool, "custom", m.Volume, rel, incus.InstanceFileArgs{
		Content:   strings.NewReader(f.Content),
		Type:      "file",
		WriteMode: "overwrite",
		Mode:      0o644,
	})
	if err != nil {
		hint := ""
		if incusapi.IsNotFound(err) {
			hint = " (tink creates no directories: check that the volume exists and that the file's parent directory is in it)"
		}
		return fmt.Errorf("writing %s into volume %s failed: %w%s", rel, vol, err, hint)
	}
	return nil
}
