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
// Build and the renderers are unchanged: the result is a []Stack like the file-based one, with every resource in its place.
func FromServer(server incus.InstanceServer) ([]Stack, error) {
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
	for _, in := range insts {
		rs = append(rs, resolve.Resource{
			Kind: resolve.KindInstance, Name: in.Name, Project: in.Project,
			Config: copyStrings(in.Config), Devices: copyDevices(in.Devices), Profiles: append([]string(nil), in.Profiles...),
		})
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
	return []Stack{{Name: ServerStackName, Resources: rs}}, nil
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
