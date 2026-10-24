package resolve

// A kind: file under a disk-device mount, on an instance that is not running. See docs/first-start-files-design.md, "Files under a mount".
//
// firststartmount.go keeps a file from being pushed beneath a mount when an instance is created. The same hazard returns on every later apply: while
// an instance is not running its disk devices are not mounted, and its own file API writes (and reads) the root filesystem underneath the mount path.
// Found live (2026-10-13, a crash-looping Authelia): Incus showed the container as Running with no init, the corrected config was pushed to
// containers/tink_sso/rootfs/config/, under the /config volume, and the volume kept the old file; tink said "created", the app kept reading the old one.
//
// So a file at or below a disk device's mount path goes where the instance will find it:
//
//   - the instance is running (it has an init): its own file API, which sees the mount, as always;
//   - it is not running and the mount is a custom volume: the volume's file API, to read the current content for the comparison and to write the new,
//     as the first start does (placeVolumeFile);
//   - it is not running and the mount is anything else (a host directory): nowhere. The file is deferred, with a warning that says why, and the next apply
//     that finds the instance running writes it. Pushing it now would hide it from the app and make the plan lie.
//
// A file outside every mount is not affected, and nor is a virtual machine (it takes no file push until its agent runs, mount or not). The mounts are
// those the live instance has (its expanded devices, so the ones from its profiles too), the instance's state is asked only for a file under one, and
// a state that cannot be read is an error, never a guess.

import (
	"fmt"
	"path"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// fileWhere is where a file resource's content is read from and written to.
type fileWhere int

const (
	inInstance fileWhere = iota // the instance's own file API: it sees whatever the instance has mounted
	inVolume                    // the custom volume behind the mount, because the instance is not running
	withheld                    // nowhere this time: under a mount that only a running instance can take the file into
)

// fileTarget is the decision for one file.
type fileTarget struct {
	Where fileWhere
	Mount mount  // inVolume and withheld: the mount that holds the file
	Why   string // inVolume and withheld: why the instance counts as not running, for a sentence
}

// decideFileTarget says where f goes. s must be scoped to f's project. It asks for the live instance, and for its state only when f is under one of its
// mounts. An instance that does not exist yet is not an error: the file then plans as a create, as it always did.
func decideFileTarget(s incus.InstanceServer, f Resource) (fileTarget, error) {
	inst, _, found, err := incusapi.LookupInstance(s, f.Instance)
	if err != nil {
		return fileTarget{}, fmt.Errorf("reading instance %s to see whether %s is under a mount: %w", f.Instance, f.Path, err)
	}
	if !found || inst.Type == string(api.InstanceTypeVM) {
		return fileTarget{}, nil
	}
	fp := path.Clean(f.Path)
	m, under := mountOver(mountsFromDevices(inst.ExpandedDevices), fp)
	if !under {
		return fileTarget{}, nil
	}
	running, why, err := incusapi.InstanceRunning(s, f.Instance)
	if err != nil {
		return fileTarget{}, fmt.Errorf("reading the state of instance %s to see where %s goes (it is under the %s mount, device %s): %w", f.Instance, f.Path, m.Path, m.Device, err)
	}
	switch {
	case running:
		return fileTarget{}, nil
	case m.Volume != "" && fp != m.Path:
		return fileTarget{Where: inVolume, Mount: m, Why: why}, nil
	}
	return fileTarget{Where: withheld, Mount: m, Why: why}, nil
}

// notRunning is the instance and the reason, as the start of a sentence.
func (t fileTarget) notRunning(f Resource) string {
	return fmt.Sprintf("instance %s is not running (%s)", f.Instance, t.Why)
}

// volumeNote is what a plan, and an apply, says about a file read from and written into a volume.
func (t fileTarget) volumeNote(f Resource) string {
	return fmt.Sprintf("%s, so %s is read from and written into volume %s/%s, where the app finds it once the instance runs; pushed into the instance it would land beneath the %s mount (device %s)",
		t.notRunning(f), f.Path, t.Mount.Pool, t.Mount.Volume, t.Mount.Path, t.Mount.Device)
}

// withheldBecause is why a file is not written: the cause alone, for the warning and for the error of a push that finds the same.
func (t fileTarget) withheldBecause(f Resource) string {
	return fmt.Sprintf("%s and %s is at or under %s, %s (device %s)", t.notRunning(f), f.Path, t.Mount.Path, t.Mount.describe(), t.Mount.Device)
}

// withheldWarning is the finding for a file that is deferred. It does not fail the apply.
func (t fileTarget) withheldWarning(f Resource) string {
	return "not written, because " + t.withheldBecause(f) + ": pushed now it would land beneath the mount, where the app never sees it, so it is left for the next apply that finds the instance running"
}

// planPlacedFile plans a kind: file against the place its instance will find it (see the package comment). A file outside every mount, or on a running
// instance, is planFile's: the instance's own file API.
func planPlacedFile(s incus.InstanceServer, f Resource) (PlannedResource, error) {
	t, err := decideFileTarget(s, f)
	if err != nil {
		return PlannedResource{}, err
	}
	switch t.Where {
	case inVolume:
		p, err := planVolumeFile(s, f, t)
		if err != nil {
			return PlannedResource{}, err
		}
		return guardDerived(f, p, p.liveContent), nil
	case withheld:
		return PlannedResource{Resource: f, Action: ActionDeferred, Warnings: []string{t.withheldWarning(f)}}, nil
	}
	p, err := planFile(s, f)
	if err != nil {
		return PlannedResource{}, err
	}
	return guardDerived(f, p, p.liveContent), nil
}

// planVolumeFile is planFile against the volume's copy of the file.
func planVolumeFile(s incus.InstanceServer, f Resource, t fileTarget) (PlannedResource, error) {
	current, found, err := readVolumeFile(s, t.Mount, t.Mount.inVolume(path.Clean(f.Path)))
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading current content of %s on %s: %w", f.Path, f.Instance, err)
	}
	p := PlannedResource{Resource: f, Notes: []string{t.volumeNote(f)}}
	switch {
	case !found:
		p.Action = ActionCreate
	case f.Seed:
		if current != f.Content {
			p.Notes = append(p.Notes, seedNote(f))
		}
	case current != f.Content:
		p.Action = ActionUpdate
		p.Changes = []string{fmt.Sprintf("content of %s on %s differs", f.Path, f.Instance)}
		p.liveContent = current
	}
	return p, nil
}

// placeFileAt writes a file where decideFileTarget says, without any restart (pushFile adds that). server is not yet scoped to the file's project.
func placeFileAt(server incus.InstanceServer, f Resource) error {
	t, err := decideFileTarget(scopedServer(server, f), f)
	if err != nil {
		return err
	}
	switch t.Where {
	case inVolume:
		return placeVolumeFile(server, f, t.Mount)
	case withheld:
		// The plan that led here said the file was deferred, or that it would go into a volume or the instance: the instance's state changed between
		// that plan and this push. Pushing would hide the file beneath the mount; failing says so.
		return fmt.Errorf("%s was not pushed to %s, because %s: run the apply again", f.Path, f.Instance, t.withheldBecause(f))
	}
	return placeFile(server, f)
}
