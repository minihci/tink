package resolve

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/minihci/tink/internal/backupext"
	"github.com/minihci/tink/internal/secrets"
)

// A stack's `defaults:` (docs/stack-defaults.md) is the boilerplate every resource of a kind repeats, written once on the `kind: stack` document:
//
//	kind: stack
//	name: platform
//	defaults:
//	  instance:
//	    config: {boot.autostart: "true", limits.memory: 256MiB}
//	    devices: {nic: {name: eth0}}
//	  storage-volume:
//	    backup: {snapshots: {schedule: "0 3 * * *", retain: 7d}}
//
// ApplyDefaults copies each default into the resources of the stack that do not set it, before the dependency graph is built. A default is only ever
// a shorthand for writing the value on each resource: it adds no resource, and what it fills in is checked exactly as if it had been written there.
//
// The rules, each one tested:
//   - a value the resource sets always wins. Config and device keys are filled one key at a time; a backup block is all or nothing (a volume that
//     declares any backup, even `none:`, is left as it is, because half of one policy merged into another would be a policy nobody wrote);
//   - the section names are kinds, and only the ones with something to say are accepted; a key tink does not know is a load error;
//   - nothing that names another resource is a default on a device (its network, source or ACL list): that would give every NIC or disk an edge of
//     the graph that is not in the file it reads;
//   - the secret rule holds: a reference is accepted in an instance's environment.* default and nowhere else, and the tink-owned config prefix is
//     still not writable.

// StackDefaults is the `defaults:` of a `kind: stack` document. A nil section is "no defaults for that kind".
type StackDefaults struct {
	Instance      *InstanceDefaults
	StorageVolume *VolumeDefaults
}

// InstanceDefaults are filled into every `kind: instance` of the stack.
type InstanceDefaults struct {
	// Config is filled one key at a time into an instance's config.
	Config map[string]string
	// Devices maps a device TYPE (nic, disk, proxy...) to the keys filled into every device of that type that does not set the key.
	// It never adds a device.
	Devices map[string]map[string]string
}

// VolumeDefaults are filled into every `kind: storage-volume` of the stack.
type VolumeDefaults struct {
	// Backup is the backup block of a volume that declares none.
	Backup *backupext.VolumeBackup
}

// deviceTypes are the Incus device types a default may name. A misspelling would otherwise default nothing, silently.
var deviceTypes = map[string]bool{
	"none": true, "nic": true, "disk": true, "unix-char": true, "unix-block": true, "unix-hotplug": true, "usb": true,
	"gpu": true, "infiniband": true, "proxy": true, "tpm": true, "pci": true,
}

// validateStackDefaults holds the `defaults:` of a stack document to the rules of the resources it fills. It builds the resource each section stands for
// and runs the same checks on it, so a default is never accepted that the same value written on a resource would be refused for.
func validateStackDefaults(r Resource) error {
	d := r.Defaults
	if r.Kind != KindStack || d == nil {
		return nil
	}
	wrap := func(section string, err error) error {
		return fmt.Errorf("stack %q: defaults.%s: %w", r.Name, section, err)
	}
	if in := d.Instance; in != nil {
		probe := Resource{Kind: KindInstance, Name: "defaults.instance", Config: in.Config, Devices: in.Devices}
		for _, check := range []func(Resource) error{validateSecretRefs, validateOwnedConfig} {
			if err := check(probe); err != nil {
				return wrap("instance", err)
			}
		}
		for _, typ := range sortedKeys(in.Devices) {
			if !deviceTypes[typ] {
				return wrap("instance", fmt.Errorf("devices: %q is not a device type (known: %s)", typ, strings.Join(sortedKeys(deviceTypes), ", ")))
			}
			keys := in.Devices[typ]
			if len(keys) == 0 {
				return wrap("instance", fmt.Errorf("devices.%s: names no key", typ))
			}
			for _, k := range sortedKeys(keys) {
				if k == "type" || k == "network" || k == "source" || strings.HasPrefix(k, "security.acls") {
					return wrap("instance", fmt.Errorf("devices.%s.%s: a default may not name another resource or change what a device is; write %s on the device itself", typ, k, k))
				}
			}
		}
	}
	if v := d.StorageVolume; v != nil && v.Backup != nil {
		// validateSecretRefs does not walk a backup block (a pointer), so a default's is checked here.
		if hasSecretRef(reflect.ValueOf(v.Backup)) {
			return wrap("storage-volume", fmt.Errorf("backup: a secret reference is not allowed: references are only accepted in an instance's environment.* values"))
		}
		if err := backupext.Get().Validate("defaults.storage-volume", v.Backup); err != nil {
			return wrap("storage-volume", err)
		}
	}
	return nil
}

// hasSecretRef reports whether any string anywhere inside v holds a `${secret:` reference.
func hasSecretRef(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		return !v.IsNil() && hasSecretRef(v.Elem())
	case reflect.String:
		return secrets.HasRef(v.String())
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if hasSecretRef(v.Index(i)) {
				return true
			}
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if hasSecretRef(v.Field(i)) {
				return true
			}
		}
	}
	return false
}

// stackDefaults is the defaults of the stack's `kind: stack` document, or nil when it has none. A stack that names itself twice has no single answer.
func stackDefaults(resources []Resource) (*StackDefaults, error) {
	var found *StackDefaults
	for _, r := range resources {
		if r.Kind == KindStack && r.Defaults != nil {
			found = r.Defaults
		}
	}
	if found == nil {
		return nil, nil
	}
	if _, err := StackName(resources); err != nil {
		return nil, err
	}
	return found, nil
}

// ApplyDefaults returns the stack's resources with its `defaults:` filled in. A stack that declares none gets its own slice back, untouched, so it plans
// exactly as it did before defaults existed.
//
// It never modifies its input: a resource that receives a default gets fresh maps (and its own copy of a backup block), so nothing is shared with the
// caller or with another resource. It is safe to run twice, because a second run finds every key already set. The result is what `plan explain` is to show.
//
// Run it once, straight after the files are loaded and before secrets are expanded and the graph is built (internal/prepare is where that order lives):
// an environment.* default may hold a secret reference, which expansion has to see, and the graph has to be built from the filled resources.
func ApplyDefaults(resources []Resource) ([]Resource, error) {
	d, err := stackDefaults(resources)
	if err != nil || d == nil {
		return resources, err
	}
	out := make([]Resource, len(resources))
	copy(out, resources)
	for i := range out {
		r := &out[i]
		changed := false
		switch r.Kind {
		case KindInstance:
			if d.Instance != nil {
				changed = fillInstance(r, d.Instance)
			}
		case KindStorageVolume:
			if d.StorageVolume != nil && d.StorageVolume.Backup != nil && r.Backup == nil {
				r.Backup = cloneBackup(d.StorageVolume.Backup)
				changed = true
			}
		}
		if changed {
			// Belt and braces: the filled resource is held to the rules of any other.
			if err := Validate(*r); err != nil {
				return nil, fmt.Errorf("after the stack's defaults: %w", err)
			}
		}
	}
	return out, nil
}

// fillInstance fills the instance defaults into r, copying a map before the first write, and reports whether anything was added.
func fillInstance(r *Resource, d *InstanceDefaults) bool {
	changed := false
	var cfg map[string]string
	for _, k := range sortedKeys(d.Config) {
		if _, set := r.Config[k]; set {
			continue
		}
		if cfg == nil {
			cfg = make(map[string]string, len(r.Config)+len(d.Config))
			for ck, cv := range r.Config {
				cfg[ck] = cv
			}
		}
		cfg[k] = d.Config[k]
	}
	if cfg != nil {
		r.Config, changed = cfg, true
	}

	var devs map[string]map[string]string
	for _, name := range sortedKeys(r.Devices) {
		dev := r.Devices[name]
		var filled map[string]string
		for _, k := range sortedKeys(d.Devices[dev["type"]]) {
			if _, set := dev[k]; set {
				continue
			}
			if filled == nil {
				filled = make(map[string]string, len(dev)+1)
				for dk, dv := range dev {
					filled[dk] = dv
				}
			}
			filled[k] = d.Devices[dev["type"]][k]
		}
		if filled == nil {
			continue
		}
		if devs == nil {
			devs = make(map[string]map[string]string, len(r.Devices))
			for dn, dd := range r.Devices {
				devs[dn] = dd
			}
		}
		devs[name] = filled
	}
	if devs != nil {
		r.Devices, changed = devs, true
	}
	return changed
}

// cloneBackup is a deep copy of a backup block, so that no two volumes share one.
func cloneBackup(b *backupext.VolumeBackup) *backupext.VolumeBackup {
	out := *b
	if b.Snapshots != nil {
		s := *b.Snapshots
		out.Snapshots = &s
	}
	out.Copies = append([]backupext.BackupCopy(nil), b.Copies...)
	if b.VerifyCheck != nil {
		c := *b.VerifyCheck
		c.Command = append([]string(nil), b.VerifyCheck.Command...)
		out.VerifyCheck = &c
	}
	return &out
}
