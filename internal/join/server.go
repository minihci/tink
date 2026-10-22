package join

import (
	"fmt"
	"sort"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/resolve"
)

// ServerStackName names the one stack FromServer returns: the server is a single place, not a set of stack files.
const ServerStackName = "server"

// FromServer reads, from the live server, the same view of the site that Build is given from stack files, so the join can run without the files. It is how
// the daemon helper will run it: the declarations are the `user.tink.publish.<name>` stamps that `plan apply` wrote onto the instances (resolve/publishstamp.go),
// and everything else Build reads is already on the server.
//
// What it reads, all of it read-only (no write method is called):
//
//   - the instances of every project: name, project, config, devices and profiles as they are STORED (not the expanded view, which Build would then merge
//     the profiles into a second time);
//   - the profiles of every project, because an offer's NIC may come from a profile;
//   - the networks, which are server-global and are what the ACL is found from; only the managed ones (an unmanaged bridge is not a network Incus holds, and a
//     NIC on one has no `network:` for the join to follow);
//   - one `kind: publish` resource per stamp, in the project of the instance that carries it.
//
// It is FAIL-CLOSED about the stamps. A stamp that is not valid JSON, has a field it should not or lacks one, or breaks any rule a `kind: publish` document is
// held to (the same rules, resolve.ValidatePublish) is never skipped and never repaired: FromServer returns a *StampError naming every such stamp and its
// instance, and no stacks. A name that is half-read would be half-rendered, and the join's whole stance is that a partial rendering looks like a complete one.
//
// It reads ONE SITE (site.go): the helpers and the publishes whose instances carry user.tink.site: SITE, or, for the unnamed site "", carry none. Instances
// of another site are left out before their stamps are read, so a stamp of another site that cannot be read does not stop this one, and the number of
// helpers that were left out is kept (Stack.Elsewhere) to be said when the join fails.
//
// Build and the renderers are unchanged: the result is a []Stack like the file-based one, with every resource in its place.
func FromServer(server incus.InstanceServer, site string) ([]Stack, error) {
	return readServer(server, false, site)
}

// PlatformFromServer reads only the platform from the server: the helpers (the instances that state a role), the profiles and the managed networks,
// and no publish at all. It is what the derive stage joins a stack's own declarations to: the stack brings its apps and its publishes, and the
// server brings the ingress helper's legs and addresses. Another stack's apps and stamps are not read, so a fault in one of them cannot stop this
// stack from being planned.
func PlatformFromServer(server incus.InstanceServer, site string) ([]Stack, error) {
	return readServer(server, true, site)
}

// Overlay puts over on top of base: the result holds every resource of over, and every resource of base that over does not declare (the same kind,
// project and name). It is a stack-wins merge, so a stack being planned replaces the server's copy of what it declares, and Build, which refuses an
// instance declared twice, sees each once. A publish the server holds for an instance the overlay declares is dropped with it: the overlay is the
// word on which names that instance shows.
func Overlay(base, over []Stack) []Stack {
	type id struct {
		kind          resolve.Kind
		project, name string
	}
	idOf := func(r resolve.Resource) id {
		if r.Kind == resolve.KindNetwork || r.Kind == resolve.KindNetworkACL {
			return id{r.Kind, "", r.Name} // server-global
		}
		return id{r.Kind, projectOf(r), r.Name}
	}
	taken := map[id]bool{}
	declared := map[id]bool{} // the instances the overlay declares: it is also the word on which names they show
	for _, s := range over {
		for _, r := range s.Resources {
			taken[idOf(r)] = true
			if r.Kind == resolve.KindInstance {
				declared[id{resolve.KindInstance, projectOf(r), r.Name}] = true
			}
		}
	}
	out := make([]Stack, 0, len(base)+len(over))
	for _, s := range base {
		kept := Stack{Name: s.Name}
		for _, r := range s.Resources {
			if taken[idOf(r)] {
				continue
			}
			// a publish the server holds for an instance the overlay declares is a stamp of an older version of it: if the overlay still
			// publishes the name it brings it, and if it does not, the name is gone
			if r.Kind == resolve.KindPublish && declared[id{resolve.KindInstance, projectOf(r), r.Instance}] {
				continue
			}
			kept.Resources = append(kept.Resources, r)
		}
		out = append(out, kept)
	}
	return append(out, over...)
}

func readServer(server incus.InstanceServer, platformOnly bool, site string) ([]Stack, error) {
	insts, err := server.GetInstancesAllProjects(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("listing the instances of every project: %w", err)
	}
	profs, err := server.GetProfilesAllProjects()
	if err != nil {
		return nil, fmt.Errorf("listing the profiles of every project: %w", err)
	}
	nets, err := server.GetNetworks()
	if err != nil {
		return nil, fmt.Errorf("listing the networks: %w", err)
	}

	sort.Slice(insts, func(i, j int) bool {
		if insts[i].Project != insts[j].Project {
			return insts[i].Project < insts[j].Project
		}
		return insts[i].Name < insts[j].Name
	})
	sort.Slice(profs, func(i, j int) bool {
		if profs[i].Project != profs[j].Project {
			return profs[i].Project < profs[j].Project
		}
		return profs[i].Name < profs[j].Name
	})
	sort.Slice(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })

	var rs []resolve.Resource
	var bad []StampProblem
	elsewhere := map[string]int{}
	for _, in := range insts {
		if platformOnly && strings.TrimSpace(in.Config[roleKey]) == "" {
			continue
		}
		if !memberOf(in.Config, hasPublishStamp(in.Config), site) {
			if strings.TrimSpace(in.Config[roleKey]) != "" {
				elsewhere[strings.TrimSpace(in.Config[resolve.SiteKey])]++
			}
			continue
		}
		rs = append(rs, resolve.Resource{
			Kind: resolve.KindInstance, Name: in.Name, Project: in.Project,
			Config: copyStrings(in.Config), Devices: copyDevices(in.Devices), Profiles: append([]string(nil), in.Profiles...),
		})
		if platformOnly {
			continue
		}
		keys := make([]string, 0)
		for k := range in.Config {
			if strings.HasPrefix(k, resolve.PublishStampPrefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			p, err := resolve.DecodePublishStamp(in.Project, in.Name, k, in.Config[k])
			if err != nil {
				bad = append(bad, StampProblem{Project: in.Project, Instance: in.Name, Key: k, Reason: err.Error()})
				continue
			}
			rs = append(rs, p)
		}
	}
	if len(bad) > 0 {
		return nil, &StampError{Problems: bad}
	}
	for _, p := range profs {
		rs = append(rs, resolve.Resource{Kind: resolve.KindProfile, Name: p.Name, Project: p.Project, Config: copyStrings(p.Config), Devices: copyDevices(p.Devices)})
	}
	for _, n := range nets {
		if !n.Managed {
			continue
		}
		rs = append(rs, resolve.Resource{Kind: resolve.KindNetwork, Name: n.Name, Config: copyStrings(n.Config)})
	}
	st := Stack{Name: ServerStackName, Resources: rs}
	if len(elsewhere) > 0 {
		st.Elsewhere = elsewhere
	}
	return []Stack{st}, nil
}

// A StampProblem is one publish stamp on the server that cannot be read. Reason already names the key.
type StampProblem struct {
	Project, Instance, Key string
	Reason                 string
}

// StampError is what FromServer returns when any stamp cannot be read. It is an error, not a set of warnings, because the join must not render from a
// declaration it could only half read.
type StampError struct{ Problems []StampProblem }

func (e *StampError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d publish stamp(s) on the server cannot be read, so nothing can be joined from it", len(e.Problems))
	for _, f := range e.Findings() {
		b.WriteString("\n  " + f.String())
	}
	return b.String()
}

// Findings are the problems as join findings (errors, one per stamp), so a caller can print them as it prints the join's own.
func (e *StampError) Findings() []Finding {
	out := make([]Finding, 0, len(e.Problems))
	for _, p := range e.Problems {
		out = append(out, Finding{Error, "instance/" + p.Instance, fmt.Sprintf("(project %s) %s", p.Project, p.Reason)})
	}
	return out
}

func copyStrings(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func copyDevices(m map[string]map[string]string) map[string]map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]map[string]string, len(m))
	for n, d := range m {
		out[n] = copyStrings(d)
	}
	return out
}
