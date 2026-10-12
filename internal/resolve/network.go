package resolve

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/incusapi"
)

// A kind: network resource declares an Incus MANAGED BRIDGE: the thing an instance's NIC names with `network:`. Until now a stack could name a bridge
// but not state it, so the bridge a project depends on was hand-made host state (docs/network.md has the reasoning, and the design note
// docs/network-resources-design.md, on branch eval/design-notes, the larger picture this is the first slice of).
//
// What this slice does, and deliberately no more:
//
//   - Creates a missing bridge and updates the keys the stack declares. It never deletes, like every other kind.
//   - Is server-global. Incus scopes networks to a project only with OVN, which this does not manage, so a `project:` is a load error rather than
//     something silently ignored.
//   - Manages type bridge only. A network that exists but is not a bridge, or is not managed by Incus at all (the host's own LAN bridge, a
//     physical NIC), is BLOCKED, never created over and never changed. That is the safety the whole kind rests on.
//   - Treats a change to a bridge that instances are using as dangerous. Changing its address, NAT or DNS can interrupt every instance on it, so
//     plan names the instances and apply refuses unless the resource says `allow_disruption: true`. It is the network version of
//     on_image_change: report: the default is to say, not to do. A description-only change is harmless and is never blocked.
//   - Compares only the keys the stack declares (diffConfig), so the keys Incus generated (volatile.*, a concrete address where `auto` was asked
//     for) are never drift. The one exception is spelled out in networkWanted.
//
// Not here: ACLs, forwards, zones, OVN, `tink export` of networks, and anything that deletes.

// networkNameRE is deliberately a little looser than Incus's own rule (a valid interface name) so tink never rejects a name Incus would accept.
// What it does enforce is what makes a bridge name unusable anywhere: too long for an interface, or containing a separator.
var networkNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// maxNetworkNameLen is the longest Linux interface name (IFNAMSIZ minus the terminating NUL); a bridge is an interface of the same name.
const maxNetworkNameLen = 15

func validateNetwork(r Resource) error {
	if r.Kind != KindNetwork {
		return nil
	}
	if r.Project != "" {
		return fmt.Errorf("resource %q: kind network is server-global and takes no project: (Incus scopes networks to a project only with OVN, which tink does not manage)", r.Name)
	}
	if len(r.Name) > maxNetworkNameLen {
		return fmt.Errorf("resource %q: a network name is also its bridge interface name, so it can be at most %d characters (%d given)", r.Name, maxNetworkNameLen, len(r.Name))
	}
	if !networkNameRE.MatchString(r.Name) || r.Name == "." || r.Name == ".." {
		return fmt.Errorf("resource %q: a network name may use only letters, digits, '_', '.' and '-'", r.Name)
	}
	for k := range r.Config {
		if k == "" {
			return fmt.Errorf("resource %q: config has an empty key", r.Name)
		}
	}
	return nil
}

// networkWanted is the part of the declared config that still needs writing: desired without the addresses that ask for `auto` when the network
// already has one. `ipv4.address: auto` (and ipv6) is how Incus is told to pick a subnet, and what it stores afterwards is the concrete address it
// picked, so comparing the literal would show a difference forever and, worse, an apply would ask Incus to pick a NEW subnet and move every
// instance on the bridge. `auto` therefore means "any address, but have one". If the network has none (or `none`), `auto` stays wanted.
func networkWanted(current, desired map[string]string) map[string]string {
	out := make(map[string]string, len(desired))
	for k, v := range desired {
		if (k == "ipv4.address" || k == "ipv6.address") && v == "auto" {
			if have := current[k]; have != "" && have != "none" {
				continue
			}
		}
		out[k] = v
	}
	return out
}

// networkInstances names the instances a network's used_by lists, as "name" in the default project and "project/name" elsewhere. used_by holds
// URLs such as /1.0/instances/ha?project=ha-eval; profiles and other objects on the network are not instances and are not counted.
func networkInstances(usedBy []string) []string {
	var out []string
	for _, ref := range usedBy {
		u, err := url.Parse(ref)
		if err != nil || !strings.HasPrefix(u.Path, "/1.0/instances/") {
			continue
		}
		name, err := url.PathUnescape(strings.TrimPrefix(u.Path, "/1.0/instances/"))
		if err != nil || name == "" {
			continue
		}
		if p := u.Query().Get("project"); p != "" && p != "default" {
			name = p + "/" + name
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func planNetwork(server incus.InstanceServer, r Resource) (PlannedResource, error) {
	current, _, found, err := incusapi.LookupNetwork(server, r.Name)
	if err != nil {
		return PlannedResource{}, fmt.Errorf("reading the live network: %w", err)
	}
	if !found {
		return PlannedResource{Resource: r, Action: ActionCreate}, nil
	}

	blocked := func(why string) (PlannedResource, error) {
		return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{why}}, nil
	}
	if !current.Managed {
		return blocked(fmt.Sprintf("%q exists but Incus does not manage it (a host interface, such as a LAN bridge). tink never creates over or changes one of those: name a different network, or leave this one out of the stack", r.Name))
	}
	if current.Type != "bridge" {
		return blocked(fmt.Sprintf("%q is a managed network of type %q; kind: network manages type bridge only", r.Name, current.Type))
	}
	if current.Status != "" && current.Status != "Created" {
		return blocked(fmt.Sprintf("%q has status %q, not Created, so it is not safe to change", r.Name, current.Status))
	}

	changes := diffConfig(current.Config, networkWanted(current.Config, r.Config), nil)
	configChanged := len(changes) > 0
	if r.Description != "" && r.Description != current.Description {
		changes = append(changes, fmt.Sprintf("description: %q -> %q", current.Description, r.Description))
	}
	if len(changes) == 0 {
		return PlannedResource{Resource: r, Action: ActionNone}, nil
	}

	plan := PlannedResource{Resource: r, Action: ActionUpdate, Changes: changes}
	if users := networkInstances(current.UsedBy); configChanged && len(users) > 0 {
		list := strings.Join(users, ", ")
		if !r.AllowDisruption {
			plan.Action = ActionBlocked
			plan.Blocked = []string{fmt.Sprintf("changing a bridge that %d instance(s) use can interrupt their traffic (%s); nothing was changed. Set allow_disruption: true on this resource to apply it", len(users), list)}
		} else {
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("affects %d instance(s): %s", len(users), list))
		}
	}
	return plan, nil
}

func createNetwork(server incus.InstanceServer, r Resource) error {
	return server.CreateNetwork(api.NetworksPost{
		Name:       r.Name,
		Type:       "bridge",
		NetworkPut: api.NetworkPut{Config: r.Config, Description: r.Description},
	})
}

func updateNetwork(server incus.InstanceServer, r Resource) error {
	current, etag, err := server.GetNetwork(r.Name)
	if err != nil {
		return err
	}
	put := current.Writable()
	if put.Config == nil {
		put.Config = map[string]string{}
	}
	for k, v := range networkWanted(current.Config, r.Config) {
		put.Config[k] = v
	}
	if r.Description != "" {
		put.Description = r.Description
	}
	return server.UpdateNetwork(r.Name, put, etag)
}
