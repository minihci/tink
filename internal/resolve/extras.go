package resolve

import (
	"fmt"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// Extras is what a live object carries that its stack does not declare: the other direction of a plan, which is one-directional on
// purpose (see diffConfig). It is the instance as `tink export` would write it (so Incus's and the image's own keys are not counted)
// set against what the stack says. Nothing here is a change plan would make, and nothing is removed because of it; it is what a reader
// asking "is this instance exactly what the stack says?" is otherwise not told. See docs/plan-extras.md.
//
// A profile, a storage volume, a network and a project get the same report (otherExtras): their devices (a profile's) and config keys.
// Profiles are only ever an instance's concern, so LiveProfiles and MissingProfiles stay empty for the rest.
type Extras struct {
	// Devices are the instance's own devices (not ones a profile supplies) that the stack does not declare, as "name (type)".
	Devices []string
	// Config are the names of config keys set on the instance that the stack does not declare. Names only: never a value.
	Config []string
	// LiveProfiles are profiles on the instance that the stack's profiles: list does not name, and MissingProfiles are the other way
	// round. Both are empty when the stack lists no profiles at all (then there is nothing to compare).
	LiveProfiles, MissingProfiles []string
	// Partial: oci.* and environment.* could not be compared with the image (offline, or its registry could not be read), so they are left
	// out rather than listing every variable the image sets.
	Partial bool
}

// Empty reports that the instance is exactly what the stack declares, as far as this report can tell.
func (e Extras) Empty() bool {
	return len(e.Devices) == 0 && len(e.Config) == 0 && len(e.LiveProfiles) == 0 && len(e.MissingProfiles) == 0
}

// Count is the number of separate things reported.
func (e Extras) Count() int {
	return len(e.Devices) + len(e.Config) + len(e.LiveProfiles) + len(e.MissingProfiles)
}

// maxExtrasShown is how many items Describe writes before summarising the rest.
const maxExtrasShown = 10

// Describe is the one line plan prints under an instance: what is on it and not in the stack. Empty when there is nothing to say.
func (e Extras) Describe() string {
	var items []string
	for _, d := range e.Devices {
		items = append(items, "device."+d)
	}
	for _, k := range e.Config {
		items = append(items, "config."+k)
	}
	for _, p := range e.LiveProfiles {
		items = append(items, fmt.Sprintf("profile %s (on the instance, not in profiles:)", p))
	}
	for _, p := range e.MissingProfiles {
		items = append(items, fmt.Sprintf("profile %s (in profiles:, not on the instance)", p))
	}
	if len(items) == 0 {
		return ""
	}
	if len(items) > maxExtrasShown {
		items = append(items[:maxExtrasShown], fmt.Sprintf("and %d more", len(items)-maxExtrasShown))
	}
	line := "not in the stack (left alone): " + strings.Join(items, ", ")
	if e.Partial {
		line += " [environment.* and oci.* not compared with the image]"
	}
	return line
}

// tinkOwnKeyPrefixes are config keys tink itself writes as state, not as something a person declared: the helper's status document, the
// record of the command `tink run` was given, the hash a `kind: exec` with triggers stores to know it already ran, and the stamps of `kind: publish`
// (a stamp the stack derives is declared, and one it no longer derives is a planned removal, so neither is an extra). They are never "extras".
var tinkOwnKeyPrefixes = []string{"user.tink.helper.", "user.tink.run.", "user.tink.exec.", PublishStampPrefix}

// undeclaredExtras compares the live instance, already reduced to what a person set (see instanceExtras), with the declared one.
func undeclaredExtras(live, declared Resource) Extras {
	var e Extras
	for name, dev := range live.Devices {
		if _, ok := declared.Devices[name]; !ok {
			e.Devices = append(e.Devices, fmt.Sprintf("%s (%s)", name, dev["type"]))
		}
	}
KeyLoop:
	for k := range live.Config {
		if _, ok := declared.Config[k]; ok {
			continue
		}
		for _, p := range tinkOwnKeyPrefixes {
			if strings.HasPrefix(k, p) {
				continue KeyLoop
			}
		}
		e.Config = append(e.Config, k)
	}
	if len(declared.Profiles) > 0 {
		e.LiveProfiles = difference(live.Profiles, declared.Profiles)
		e.MissingProfiles = difference(declared.Profiles, live.Profiles)
	}
	sort.Strings(e.Devices)
	sort.Strings(e.Config)
	return e
}

// difference returns the members of a that are not in b, in a's order.
func difference(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return out
}

// dropPrefixed returns keys without those that start with any of prefixes.
func dropPrefixed(keys []string, prefixes ...string) []string {
	var out []string
KeyLoop:
	for _, k := range keys {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				continue KeyLoop
			}
		}
		out = append(out, k)
	}
	return out
}

// instanceExtras reduces the live instance to what a person set, the way `tink export` does, and compares that with declared. server is
// the one the instance was read from (already scoped to its project). When oci.* and environment.* could not be compared with the image,
// those keys are left out and Partial says so, since otherwise every variable the image sets would look like an extra.
func instanceExtras(server incus.InstanceServer, env *imageEnv, inst *api.Instance, declared Resource, offline bool) Extras {
	d := exportInstance(server, server, env, inst, declared.Project, ExportOptions{Offline: offline}, &ExportResult{})
	e := undeclaredExtras(d.res, declared)
	if d.uncompared {
		e.Config = dropPrefixed(e.Config, "environment.", "oci.")
		e.Partial = true
	}
	return e
}

// UndeclaredExtras reads the instance that declared names from server and reports what it carries that declared does not. found is false
// when the instance does not exist (a stack not yet applied has no extras). offline skips the registry lookups that tell the image's own
// oci.* and environment.* keys from a person's; the result is then Partial.
func UndeclaredExtras(server incus.InstanceServer, declared Resource, offline bool) (extras Extras, found bool, err error) {
	project := declared.Project
	if project == "default" {
		project = ""
	}
	scoped := server
	if project != "" {
		scoped = server.UseProject(project)
	}
	inst, _, found, err := incusapi.LookupInstance(scoped, declared.Name)
	if err != nil || !found {
		return Extras{}, false, err
	}
	return instanceExtras(scoped, newImageEnv(offline), inst, declared, offline), true, nil
}

// WithExtras asks planning to list, for each instance that exists, what it carries that the stack does not declare (see Extras). It adds
// a line to what plan prints and changes nothing else: no action, no exit status, and nothing is removed.
func (o PlanOptions) WithExtras() PlanOptions {
	o.extras = true
	return o
}

// otherExtraPrefixes are config keys that are never a person's on a profile, a volume, a network or a project: Incus's own state, and tink's
// own (a volume's backup policy and copy markers, its stack pointer). An instance has its own, narrower list (tinkOwnKeyPrefixes), because
// it is reduced the way `tink export` does it.
var otherExtraPrefixes = []string{"volatile.", "user.tink."}

// projectFeatureDefaults are the values Incus gives a project's features.* keys when it creates one: it writes every one of them, so a
// project that declares only some of them still carries the rest, and listing those would be listing what nobody set. Only a value that is
// not the default counts as something a person did.
var projectFeatureDefaults = map[string]string{
	"features.images":          "true",
	"features.profiles":        "true",
	"features.storage.buckets": "true",
	"features.storage.volumes": "true",
	"features.networks":        "false",
	"features.networks.zones":  "false",
}

// undeclaredConfig returns the names of the keys in live that declared does not have, sorted, leaving out those skip says are not a person's.
func undeclaredConfig(live, declared map[string]string, skip func(k, v string) bool) []string {
	var keys []string
KeyLoop:
	for k, v := range live {
		if _, ok := declared[k]; ok {
			continue
		}
		for _, p := range otherExtraPrefixes {
			if strings.HasPrefix(k, p) {
				continue KeyLoop
			}
		}
		if skip != nil && skip(k, v) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// undeclaredDevices names, as "name (type)", the devices in live that declared does not have, sorted.
func undeclaredDevices(live, declared map[string]map[string]string) []string {
	var out []string
	for name, dev := range live {
		if _, ok := declared[name]; !ok {
			out = append(out, fmt.Sprintf("%s (%s)", name, dev["type"]))
		}
	}
	sort.Strings(out)
	return out
}

// otherExtras reads the live profile, storage volume, network or project that r declares and reports what it carries that r does not. It
// returns nil when there is nothing to compare: the object does not exist (a stack not yet applied has no extras), or it is a network tink
// would not touch anyway (planNetwork blocks it). server is already scoped to r's project. env is only used for a volume, whose declared config
// is more than r.Config: the snapshot keys its backup block derives (volumeBackupConfig).
func otherExtras(server incus.InstanceServer, r Resource, env volumeEnv) (*Extras, error) {
	var e Extras
	switch r.Kind {
	case KindProfile:
		cur, _, found, err := incusapi.LookupProfile(server, r.Name)
		if err != nil || !found {
			return nil, err
		}
		e.Config = undeclaredConfig(cur.Config, r.Config, nil)
		e.Devices = undeclaredDevices(cur.Devices, r.Devices)
	case KindStorageVolume:
		pool := r.Pool
		if pool == "" {
			pool = "default"
		}
		cur, _, found, err := incusapi.LookupVolume(server, pool, "custom", r.Name)
		if err != nil || !found {
			return nil, err
		}
		declared, _, err := volumeBackupConfig(r, env)
		if err != nil {
			return nil, nil // the plan is blocked for it already
		}
		e.Config = undeclaredConfig(cur.Config, declared, nil)
	case KindNetwork:
		cur, _, found, err := incusapi.LookupNetwork(server, r.Name)
		if err != nil || !found || !cur.Managed || cur.Type != "bridge" {
			return nil, err
		}
		e.Config = undeclaredConfig(cur.Config, r.Config, nil)
	case KindProject:
		cur, _, found, err := incusapi.LookupProject(server, r.Name)
		if err != nil || !found {
			return nil, err
		}
		e.Config = undeclaredConfig(cur.Config, r.Config, func(k, v string) bool { return projectFeatureDefaults[k] == v })
	default:
		return nil, nil
	}
	return &e, nil
}
