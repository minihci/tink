package resolve

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"

	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/incusapi"
)

// A kind: helper resource declares the DAEMON HELPER: the Incus instance that runs `tink daemon run` (the backup scheduler and executor, and the reconcile
// work that follows), which keeps the site matching its declarations between the times someone runs `plan apply` (docs/helper.md, "What the helper is for").
//
//	kind: helper
//	name: helper                 # the helper's instance name (helper.DefaultName)
//	project: tink-helper         # where it lives (helper.DefaultProject when omitted; the stack's default project is NOT used: the helper is not an app)
//	timezone: America/Denver     # the zone its schedules are evaluated in (UTC when omitted, and plan says so)
//	network: incusbr0            # the network its NIC joins (optional)
//	pool: default                # the pool for its root disk and volumes (optional)
//	image: ghcr:minihci/tink-helper:v0.1.4   # the OCI image it is installed from (optional: the image published for this tink)
//	remotes: [host, vps]         # the Incus servers it must be able to reach, for `plan` to check (optional)
//
// What this does, and deliberately no more:
//
//   - It ADOPTS a helper that is there. plan reports how it is (healthy, degraded, down), and warns when a remote the stack says it needs is not among the remotes
//     it reports. It changes nothing about a helper that exists: no upgrade, no restart, no re-enrolment. Those are `tink helper upgrade` and friends, because an
//     upgrade drains the helper and a declaration is not the place to start one by surprise.
//   - It INSTALLS one when there is none, exactly as `tink helper install` does (it mints a single-use trust token with the certificate of whoever runs the apply
//     and enrols the helper with a certificate of its own). That is the point of declaring it: a platform stack brings up its own controller.
//   - It does not install over an instance of the same name that is not a helper: that is BLOCKED.
//   - It never deletes a helper. Like every kind here, removal is `tink helper remove`.
//
// Not here: a version policy (an `on_version_change: report | upgrade`, like `on_image_change`), adding remotes (they need a token from the other server,
// which is the user's to give), and the ingress half.

var helperInstanceName = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)

// installHelper is the install seam: the real one is `tink helper install`'s. Tests replace it.
var installHelper = func(server incus.InstanceServer, o helper.InstallOptions) error {
	return (&helper.Installer{Server: server, Out: io.Discard}).Install(o)
}

// helperTarget is the project and instance name a helper resource means.
func helperTarget(r Resource) (project, name string) {
	project, name = r.Project, r.Name
	if project == "" {
		project = helper.DefaultProject
	}
	return project, name
}

// validateHelper rejects a malformed `kind: helper` at load time.
func validateHelper(r Resource) error {
	if r.Kind != KindHelper {
		return nil
	}
	if !helperInstanceName.MatchString(r.Name) {
		return fmt.Errorf("resource %q: a helper's name is its instance name: letters, digits and '-', at most 63, not starting or ending with '-'", r.Name)
	}
	if r.Timezone != "" {
		if _, err := time.LoadLocation(r.Timezone); err != nil {
			return fmt.Errorf("resource %q: timezone %q is not a time zone this system knows (for example America/Denver): %v", r.Name, r.Timezone, err)
		}
	}
	seen := map[string]bool{}
	for _, name := range r.Remotes {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("resource %q: remotes lists an empty name, or one with leading or trailing space", r.Name)
		}
		if seen[name] {
			return fmt.Errorf("resource %q: remotes lists %q twice", r.Name, name)
		}
		seen[name] = true
	}
	return nil
}

// planHelper reads the server: it finds helpers in every project (the marker is on the instance), so it takes the server unscoped.
func planHelper(server incus.InstanceServer, r Resource, now time.Time) (PlannedResource, error) {
	project, name := helperTarget(r)
	label := project + "/" + name
	all, err := helper.Find(server)
	if err != nil {
		return PlannedResource{}, err
	}
	var found *helper.Found
	for i := range all {
		if all[i].Project == project && all[i].Name == name {
			found = &all[i]
		}
	}

	p := PlannedResource{Resource: r, Action: ActionNone}
	if found == nil {
		// An instance of that name that is not a helper would make the install fail half way; say so now.
		if _, _, exists, err := incusapi.LookupInstance(server.UseProject(project), name); err != nil {
			return PlannedResource{}, fmt.Errorf("checking whether %s exists: %w", label, err)
		} else if exists {
			return PlannedResource{Resource: r, Action: ActionBlocked, Blocked: []string{
				fmt.Sprintf("an instance named %s exists and is not a helper (it lacks the marker %s): rename the helper, or move that instance", label, helper.MarkerKey)}}, nil
		}
		p.Action = ActionCreate
		p.Changes = []string{fmt.Sprintf("install the daemon helper %s (what `tink helper install` does: a trust token from this certificate, then its own certificate)", label)}
		if r.Timezone == "" {
			p.Warnings = append(p.Warnings, "no timezone: the helper would evaluate its backup schedules in UTC")
		}
		for _, remote := range r.Remotes {
			if remote != "host" {
				p.Warnings = append(p.Warnings, fmt.Sprintf("the helper will not be able to reach remote %q until `tink helper remote add %s ...` (it needs a token or fingerprint from that server)", remote, remote))
			}
		}
		return p, nil
	}

	rep := helper.Evaluate(*found, now)
	p.Notes = append(p.Notes, fmt.Sprintf("the daemon helper %s is %s", label, rep.Health))
	if rep.Status != nil && rep.Status.Version != "" {
		p.Notes = append(p.Notes, "it runs "+rep.Status.Version+"; plan never upgrades it (that is `tink helper upgrade`, which drains it first)")
	}
	if rep.Health != helper.Healthy {
		for _, reason := range rep.Reasons {
			p.Warnings = append(p.Warnings, "the daemon helper is "+rep.Health.String()+": "+reason)
		}
	}
	if rep.Status != nil && rep.Status.Remotes != nil {
		have := map[string]bool{}
		for _, remote := range rep.Status.Remotes {
			have[remote.Name] = true
		}
		for _, remote := range r.Remotes {
			if !have[remote] {
				p.Warnings = append(p.Warnings, fmt.Sprintf("the stack says the helper needs remote %q, and it reports none by that name: `tink helper remote add %s ...` (copies to it fail until then)", remote, remote))
			}
		}
	}
	return p, nil
}

// createHelper installs the helper, the way `tink helper install` does. It takes the unscoped server: it creates the project too.
func createHelper(server incus.InstanceServer, r Resource) error {
	project, name := helperTarget(r)
	return installHelper(server, helper.InstallOptions{
		Project: project, Name: name, Pool: r.Pool, Network: r.Network, Image: r.Image, TZ: r.Timezone,
	})
}
