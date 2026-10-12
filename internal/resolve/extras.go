package resolve

import (
	"fmt"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/incusapi"
)

// Extras is what a live instance carries that its stack does not declare: the other direction of a plan, which is one-directional on
// purpose (see diffConfig). It is the instance as `tink export` would write it (so Incus's and the image's own keys are not counted)
// set against what the stack says. Nothing here is a change plan would make, and nothing is removed because of it; it is what a reader
// asking "is this instance exactly what the stack says?" is otherwise not told. See docs/authoritative-apply-design.md, layer 1.
type Extras struct {
	// Devices are the instance's own devices (not ones a profile supplies) that the stack does not declare, as "name (type)".
	Devices []string
	// Config are the names of config keys set on the instance that the stack does not declare. Names only: never a value.
	Config []string
	// LiveProfiles are profiles on the instance that the stack's profiles: list does not name, and MissingProfiles are the other way
	// round. Both are empty when the stack lists no profiles at all (then there is nothing to compare).
	LiveProfiles, MissingProfiles []string
}

// Empty reports that the instance is exactly what the stack declares, as far as this report can tell.
func (e Extras) Empty() bool {
	return len(e.Devices) == 0 && len(e.Config) == 0 && len(e.LiveProfiles) == 0 && len(e.MissingProfiles) == 0
}

// Count is the number of separate things reported.
func (e Extras) Count() int {
	return len(e.Devices) + len(e.Config) + len(e.LiveProfiles) + len(e.MissingProfiles)
}

// tinkOwnKeyPrefixes are config keys tink itself writes as state, not as something a person declared: the helper's status document, the
// record of the command `tink run` was given, and the hash a `kind: exec` with triggers stores to know it already ran. They are never "extras".
var tinkOwnKeyPrefixes = []string{"user.tink.helper.", "user.tink.run.", "user.tink.exec."}

// undeclaredExtras compares the live instance, already reduced to what a person set (see UndeclaredExtras), with the declared one.
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

// UndeclaredExtras reads the instance that declared names from server and reports what it carries that declared does not. found is false
// when the instance does not exist (a stack not yet applied has no extras). offline skips the registry lookups that tell the image's own
// oci.* and environment.* keys from a person's; without them every such key looks like an extra, so leave it false unless there is no network.
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
	d := exportInstance(server, scoped, newImageEnv(offline), inst, project, ExportOptions{Offline: offline}, &ExportResult{})
	return undeclaredExtras(d.res, declared), true, nil
}
