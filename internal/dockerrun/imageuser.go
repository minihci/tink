package dockerrun

import (
	"fmt"
	"strconv"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// A volume `tink run` creates is owned by the user the image runs as (#176).
//
// An image whose process is not root (Grafana runs as 472, Node-RED and the Matter.js server as 1000) cannot write to a volume that Incus creates, which is
// root's, and exits at once. Docker copies the image's ownership of the mount point into a new named volume; Incus does not, but it applies
// initial.uid and initial.gid to a volume when it creates one (never to one that exists), which is what --user already uses (applyUser).
//
// The image's user is on the instance as oci.uid and oci.gid once Create has made it, and before it starts, so the volumes ApplyConfig creates take it. --user
// still decides when it is given. A user of 0 (most images), no user, or one that is not a number changes nothing; a virtual machine has no oci.* keys.
// Every volume this run creates gets the same owner, as with --user. Docker copies each mount point's own owner, which is usually the process user; a
// mount point owned by someone else is not covered.

// inheritImageUser sets spec.VolumeConfig from the new instance's oci.uid and oci.gid. server must be scoped to the instance's project. It returns a
// warning when it could not read the instance (the volumes are then created root-owned, as before), and "" otherwise.
func inheritImageUser(server incus.InstanceServer, spec *Spec) string {
	if len(spec.VolumeConfig) > 0 || !createsManagedVolume(spec) {
		return ""
	}
	inst, _, found, err := incusapi.LookupInstance(server, spec.Name)
	switch {
	case err != nil:
		return fmt.Sprintf("could not read %s to see which user its image runs as (%v), so the volumes this run creates are root-owned; if the app cannot write to them, run it again with --user UID[:GID]", spec.Name, err)
	case !found:
		return ""
	}
	uid, gid := inst.Config["oci.uid"], inst.Config["oci.gid"]
	if n, err := strconv.ParseUint(uid, 10, 32); err != nil || n == 0 {
		return ""
	}
	spec.VolumeConfig = map[string]string{"initial.uid": uid}
	if _, err := strconv.ParseUint(gid, 10, 32); err == nil {
		spec.VolumeConfig["initial.gid"] = gid
	}
	spec.VolumeOwnerFromImage = true
	return ""
}

// createsManagedVolume reports whether the spec attaches a managed volume, which this run may have to create.
func createsManagedVolume(spec *Spec) bool {
	for _, dev := range spec.Devices {
		if dev["type"] == "disk" && dev["pool"] != "" && dev["source"] != "" {
			return true
		}
	}
	return false
}

// ownerSource is what the lines about a volume's owner add when the owner is the image's user.
func ownerSource(spec *Spec) string {
	if spec.VolumeOwnerFromImage {
		return " (the image's user)"
	}
	return ""
}
