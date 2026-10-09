// Package opinion is tink's opinions as data and as checks.
//
// An opinion is a default, stated once with its reason, plus a check that notices when a resource departs from it, plus a way to depart
// on purpose that records why. The checks here are pure: they take what the caller has already read (a pool's driver, an instance's
// config) and return a Finding, so they can be tested without an Incus and reused wherever a stack is read. Nothing in this package
// reads a server or writes anything.
//
// Opinions warn; they do not block. A Finding never fails a plan.
//
// See docs/opinion-as-code.md.
package opinion

import (
	"fmt"
	"sort"
	"strings"
)

// Name identifies an opinion. It is also the key of a resource's `accept:` block.
type Name string

const (
	Backup   Name = "backup"
	Storage  Name = "storage"
	Identity Name = "identity"
)

// Opinion is what tink prefers, why, and what it costs to put right.
type Opinion struct {
	Name        Name
	Default     string // the preference, stated once
	Why         string
	Alternative string // the named alternative, and when it is the right choice
	FixCost     string // what correcting a departure involves, in words
}

// All is every opinion, in the order a report lists them.
var All = []Opinion{
	{
		Name:        Backup,
		Default:     "every volume declares how it is backed up, and the copies meet 3-2-1",
		Why:         "three copies on two failure domains with one off-site survive the failures a homelab or a VPS actually has, and a backup is not one until a restore has been shown to work",
		Alternative: "backup: none: REASON, for a volume that can be rebuilt",
		FixCost:     "edit the YAML and apply; the first copy still has to run",
	},
	{
		Name:        Storage,
		Default:     "volumes live on a pool whose driver checksums its data: zfs, btrfs (or a pool backed by one)",
		Why:         "volume management and checksumming: a silent bit flip is found on read and not carried into a backup, and snapshots and copy-on-write clones are cheap",
		Alternative: "lvm has snapshots but no checksums; dir has neither. Fine for scratch data, or when something above checksums (an application's own, or a file system underneath)",
		FixCost:     "migrate data: create the right pool, then move the volume",
	},
	{
		Name:        Identity,
		Default:     "an app that is reachable from the internet sits behind the identity provider (Authelia)",
		Why:         "one sign-in, one place to revoke it, and no app is public by forgetting to say so",
		Alternative: "a deliberately public app, with the reason written down",
		FixCost:     "set user.ingress.auth on the instance and apply",
	},
}

// Lookup returns the named opinion.
func Lookup(n Name) (Opinion, bool) {
	for _, o := range All {
		if o.Name == n {
			return o, true
		}
	}
	return Opinion{}, false
}

// Known reports whether n names an opinion, and Names lists them, for validating an `accept:` block.
func Known(n string) bool { _, ok := Lookup(Name(n)); return ok }

func Names() []string {
	out := make([]string, len(All))
	for i, o := range All {
		out[i] = string(o.Name)
	}
	sort.Strings(out)
	return out
}

// State is how a resource stands against an opinion.
type State string

const (
	// Met: the resource does what the opinion prefers.
	Met State = "met"
	// Accepted: it does not, and its author said so with a reason.
	Accepted State = "accepted"
	// Departs: it does not, and nobody said why.
	Departs State = "departs"
)

// Finding is one resource's standing against one opinion.
type Finding struct {
	Opinion  Name
	Resource string // kind/name, as plan prints it
	State    State
	Reason   string // Accepted: the author's reason
	Message  string // Departs: what is wrong; Met: a note worth keeping, if any
}

// CheckStorage judges a volume by the driver of the pool it lives in. accepted is the volume's own reason for being there, or "".
//
// The driver is only a proxy for what the opinion cares about (a dir pool on a ZFS dataset is fine), which is why a departure is a warning
// that can be accepted with a reason, never a refusal.
func CheckStorage(resource, pool, driver, accepted string) Finding {
	f := Finding{Opinion: Storage, Resource: resource}
	meets, note := driverChecksums(driver)
	switch {
	case meets:
		f.State, f.Message = Met, note
	case accepted != "":
		f.State, f.Reason = Accepted, accepted
	default:
		f.State = Departs
		f.Message = fmt.Sprintf("pool %q uses driver %s: %s. Preferred: zfs or btrfs. Putting it right means moving the volume to another pool; "+
			"or accept it with a reason: accept: {storage: \"...\"}", pool, driver, note)
	}
	return f
}

// driverChecksums says whether an Incus storage driver verifies data on read, with a short note.
func driverChecksums(driver string) (bool, string) {
	switch driver {
	case "zfs":
		return true, "zfs checksums every block"
	case "btrfs":
		return true, "btrfs checksums data and metadata"
	case "truenas":
		return true, "a ZFS dataset on the NAS, so blocks are checksummed there"
	case "ceph", "cephfs":
		return true, "ceph's object store checksums what it stores"
	case "lvm", "lvmcluster":
		return false, "snapshots, but no checksums"
	case "dir":
		return false, "no checksums and no cheap snapshots"
	case "":
		return false, "its driver could not be read"
	default:
		return false, "tink does not know whether this driver checksums"
	}
}

// IngressAuth is the value of user.ingress.auth that puts a route behind Authelia.
const IngressAuth = "authelia"

// CheckIdentity judges an instance that registers with the ingress. It returns false when the instance is not registered, because then the
// opinion does not apply. config is the instance's declared config; accepted is its own reason for being public, or "".
func CheckIdentity(resource string, config map[string]string, accepted string) (Finding, bool) {
	if config["user.ingress.enabled"] != "true" {
		return Finding{}, false
	}
	f := Finding{Opinion: Identity, Resource: resource}
	switch auth := config["user.ingress.auth"]; {
	case auth == IngressAuth:
		f.State = Met
	case accepted != "":
		f.State, f.Reason = Accepted, accepted
	case auth != "":
		f.State = Departs
		f.Message = fmt.Sprintf("user.ingress.auth is %q, which tink does not know; the value that puts the route behind sign-in is %q", auth, IngressAuth)
	default:
		f.State = Departs
		f.Message = fmt.Sprintf("%s is reachable from the internet at %s without sign-in. Put it behind Authelia with user.ingress.auth: %s, "+
			"or accept it with a reason: accept: {identity: \"...\"}", strings.TrimPrefix(resource, "instance/"), config["user.ingress.domain"], IngressAuth)
	}
	return f, true
}
