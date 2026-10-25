// Package instance is the instance engine: writing an instance to Incus. It creates the record without starting it (Create), converges its config and
// devices (ApplyConfig, which also makes the managed volumes it names), starts or restarts it (EnsureRunning), and turns an image reference into one
// Incus can resolve (Qualify). Spec is the request all of that takes.
//
// It is what applying an instance IS, so a stack apply (resolve), the helper's install and upgrade, and the backup check instance all use it. It knows
// nothing of docker-run flags: translating those into a Spec is internal/dockerrun, which imports this package and not the other way round (#216).
package instance

import "time"

// Spec is the fully-resolved instance-creation request produced by
// translating docker-run-style flags onto Incus primitives -- see
// DESIGN.md's "Flag mapping" table for the reasoning behind each
// translation below. Building a Spec never touches Incus; only Run does.
type Spec struct {
	Name      string
	Image     string
	Cmd       []string
	Config    map[string]string
	Devices   map[string]map[string]string
	Profiles  []string
	Ephemeral bool
	VM        bool

	// Notes are things the person should be told about what was asked for (a bind mount that will not be writable, an option that was ignored).
	Notes []string

	// VolumeConfig is the config a managed storage volume gets when this run has to CREATE it (one that exists is not changed). From --user, or, when
	// --user is not given, from the user the image runs as (inheritImageUser).
	VolumeConfig map[string]string
	// VolumeOwnerFromImage says VolumeConfig's owner is the image's user and not one --user named, so the lines about the volumes say so.
	VolumeOwnerFromImage bool

	// RootDiskAtCreate sends the root disk in Devices (the disk at path /) in the creation request itself, as `incus init --storage` does, instead of
	// leaving it for ApplyConfig. Incus needs a root disk when it writes the instance record, and by default it is sent only the profiles, so profiles that
	// carry none (a project's empty `default`) fail with "No root device could be found". It is for instances tink makes for its own purposes (the
	// check instance of `backup verify`, the helper). A stack's instances leave it off: `plan` warns about their profiles instead (resolve/rootdisk.go).
	RootDiskAtCreate bool

	// CreateConfig is config sent in the creation request itself, so it is on the instance from the moment it exists and not after a second call that a
	// crash could skip. It is for what must be there from birth (the ownership stamp, resolve/ownership.go); everything else in Config still goes in
	// ApplyConfig, because a key may need a device or a volume that does not exist yet.
	CreateConfig map[string]string

	// OwnedConfigPrefixes are config key prefixes that tink OWNS on the instance (a prefix that ends in a dot owns every key under it, one that does not owns
	// that key alone; see StaleOwnedKeys): ApplyConfig removes every live key owned that Config does not carry. Everywhere else ApplyConfig only sets, and a key that is no longer declared is left alone; an owned prefix is the one place a key tink wrote is
	// taken back when nothing derives it any more (the stamps of `kind: publish`, resolve/publishstamp.go). Empty for anything else.
	OwnedConfigPrefixes []string

	// OpTimeout bounds each call Create and ApplyConfig make to change the server: past it the call is abandoned, not cancelled, and the error wraps
	// incusapi.ErrTimeout (see incusapi.Bounded for what that does and does not mean). Zero is no bound, as `tink run` and the helper have always had it;
	// the resolver sets it.
	OpTimeout time.Duration
}

// HasRootDisk reports whether devices holds a root disk: Incus's own test (a disk with path / and no source, whatever the device is called).
func HasRootDisk(devices map[string]map[string]string) bool {
	for _, d := range devices {
		if isRootDisk(d) {
			return true
		}
	}
	return false
}

func isRootDisk(dev map[string]string) bool {
	return dev["type"] == "disk" && dev["path"] == "/" && dev["source"] == ""
}

// KeyCommand is the instance config key tink run records the command it was given in: the only trace, on the instance, of how it
// came to be. `tink export` reads it back as a comment. Environment values that look like secrets are masked in it.
const KeyCommand = "user.tink.run.command"
