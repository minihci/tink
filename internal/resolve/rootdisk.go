package resolve

import (
	"fmt"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// Creating an instance sends Incus only its profiles and the ephemeral flag (run.Create); the instance's own config and devices are applied after it exists
// (createInstance). Incus needs a root disk when it writes the instance record, and at that moment the profiles are all it has, so an instance whose profiles
// carry no disk device at `path: /` cannot be created, and a root disk declared on the instance itself comes too late to help. A project made with
// features.profiles=true starts with an EMPTY `default` profile, so an instance that lists no profiles fails the same way. Incus says
//
//	Failed instance creation: Failed creating instance record: Failed initializing instance: Failed getting root disk: No root device could be found
//
// and nothing in it points at a profile. Found in the DNS lab (2026-10-13), whose first two applies failed so; see docs/resolver-architecture.md.
//
// rootDiskWarnings is the check that closes that gap on the tink side. It is a warning, not an error, and it speaks only when it can be sure: a profile that cannot
// be read or named is never guessed at. Like ociNICWarnings it is about the stack, so it appears on the next plan whether the instance exists or not.

// isRootDisk reports whether dev is a disk device mounted at /, which is what Incus calls the root disk.
func isRootDisk(dev map[string]string) bool { return dev["type"] == "disk" && dev["path"] == "/" }

// hasRootDisk reports whether devices holds a root disk.
func hasRootDisk(devices map[string]map[string]string) bool {
	for _, d := range devices {
		if isRootDisk(d) {
			return true
		}
	}
	return false
}

// rootDiskWarnings returns a warning when none of the profiles r will get has a root disk. server is scoped to r's project. The profiles are r's own list, or
// the project's `default` when it lists none (Incus's rule, and run.checkNetwork's). Each is read from the stack's declaration and from the server: applying a
// declared profile only adds or replaces the devices it names (updateOne), so a root disk the server's copy already has still counts. One profile with a root
// disk is enough, and the instance's own devices are never looked at, because creation does not see them.
//
// It is conservative on purpose. If any profile cannot be read (a failed lookup), or exists neither in the stack nor on the server (Incus reports that on its own,
// and there is nothing to say about its devices), there is no warning. A later profile that overrides an earlier root device is not modelled either: that can
// only hide a warning, never add a false one.
func rootDiskWarnings(server incus.InstanceServer, r Resource, declared map[string]Resource) []string {
	if r.Kind != KindInstance {
		return nil
	}
	profiles, which := r.Profiles, ""
	if len(profiles) == 0 {
		profiles, which = []string{"default"}, `the project's "default" profile, because none are listed`
	} else {
		which = strings.Join(profiles, ", ")
	}
	for _, name := range profiles {
		p, inStack := declared[name]
		if inStack && hasRootDisk(p.Devices) {
			return nil
		}
		live, _, found, err := incusapi.LookupProfile(server, name)
		if err != nil || (!found && !inStack) {
			return nil
		}
		if found && hasRootDisk(live.Devices) {
			return nil
		}
	}
	return []string{fmt.Sprintf("instance %q has no root disk: none of its profiles (%s) has a disk device with path: /. Incus is sent only the profiles when an instance is created, never the instance's own devices, so creation fails with \"No root device could be found\", and a root disk declared on the instance does not help. Declare a kind: profile with a root disk (a device with type: disk, path: /, pool: <pool>) and list it under profiles:; see docs/resolver-architecture.md",
		r.Name, which)}
}
