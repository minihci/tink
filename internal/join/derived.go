package join

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/instance"
	"github.com/minihci/tink/internal/resolve"
)

// What a reconcile of the derived files found and did, for each file. They are fixed words, because the daemon's status document carries them and holds no
// error text or file content (errors from Incus can echo credentials; the detail stays in the log).
const (
	DerivedChanged   = "changed"    // the file differed; it was rewritten and read back
	DerivedUnchanged = "no changes" // the file already held the rendering; nothing was written
	DerivedBlocked   = "blocked"    // a refusal stopped it, on purpose; nothing was written
	DerivedSkipped   = "skipped"    // the site has no stamped derived file, so there is nothing to keep
	DerivedFailing   = "failing"    // the server or the instance could not be read, written or restarted
)

// A Restarter restarts an instance, the way a `kind: file` with restart: true does after a push that changed something (resolve.Resource.Restart).
type Restarter interface {
	Restart(project, instance string) error
}

// IncusRestarter is the real Restarter: it asks Incus to restart the instance, or to start it if it is stopped, which is what `plan apply` does.
type IncusRestarter struct{ Server incus.InstanceServer }

func (r IncusRestarter) Restart(project, name string) error {
	return instance.EnsureRunningWithin(r.Server.UseProject(project), name, 2*time.Minute)
}

// DerivedOutcome is the result of one reconcile of one derived file of one site. Name is the file document's name, empty for a result that is about the
// whole site (nothing stamped, or a stamp that cannot be read).
type DerivedOutcome struct {
	Site, Name string
	State      string // one of the Derived* words
	Reason     string // short and fixed: what to look for in the log; empty when State is changed, no changes or skipped
	Wrote      bool
	// Findings, Detail and Err are for a log. They are not for the status document.
	Findings []string
	Detail   []string
	Err      error
}

// A StampedFile is a derived file as its stamp says it, with the state of the instance it is on, as the list of instances showed it.
type StampedFile struct {
	resolve.DerivedFile
	// Status is the instance's state: "Running", "Stopped", ...
	Status string
}

// DerivedFromServer reads the stamps of the derived files (resolve.DeriveStampPrefix) from the instances of one site. A stamp that cannot be read is never
// skipped or repaired: it is returned as a problem beside the files that could be read, and the caller blocks that file and keeps the others (they are
// independent, unlike the publish stamps, which together make one rendering).
func DerivedFromServer(server incus.InstanceServer, site string) ([]StampedFile, []StampProblem, error) {
	insts, err := server.GetInstancesAllProjects(api.InstanceTypeAny)
	if err != nil {
		return nil, nil, fmt.Errorf("listing the instances of every project: %w", err)
	}
	sort.Slice(insts, func(i, j int) bool {
		if insts[i].Project != insts[j].Project {
			return insts[i].Project < insts[j].Project
		}
		return insts[i].Name < insts[j].Name
	})
	var files []StampedFile
	var bad []StampProblem
	for _, in := range insts {
		if !hasDeriveStamp(in.Config) || !memberOf(in.Config, true, site) {
			continue
		}
		keys := make([]string, 0)
		for k := range in.Config {
			if strings.HasPrefix(k, resolve.DeriveStampPrefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			f, err := resolve.DecodeDeriveStamp(in.Project, in.Name, k, in.Config[k])
			if err != nil {
				bad = append(bad, StampProblem{Project: in.Project, Instance: in.Name, Key: k, Reason: err.Error()})
				continue
			}
			files = append(files, StampedFile{DerivedFile: f, Status: in.Status})
		}
	}
	return files, bad, nil
}

// ReconcileDerived reads one site from the server and makes each of its stamped derived files equal to the join's rendering of what the site publishes:
// what `plan apply` does for a `kind: file` with derive:, run from the stamps and not from the stack files, for the daemon. It is level-triggered: it reads
// everything it needs, compares, and writes only a file that differs, so running it again, or on an event that changed nothing, writes nothing.
//
// It holds the guards of `plan` (resolve.CheckDerived) and passes neither decision that belongs to the operator: it never adopts a file tink did not write
// and never empties a region that has lines. It does not create a file or its markers either: the live file is the template, every line outside the markers
// is kept, and a file that is missing or has no markers is blocked ("run plan apply"). A join error, or a publish stamp that cannot be read, blocks every file
// and writes nothing: a partial rendering looks like a complete one. A file is read back after it is written, and the instance is restarted only when the
// stamp says restart and the content changed.
//
// It keeps files on RUNNING instances only. `plan` knows that a file pushed into a stopped instance lands beneath any disk mount over its path, where the app
// never sees it, and puts it into the volume instead; this does not, and a read-back of such a write would pass. So an instance that is not running is skipped,
// and the next pass after it starts (or `plan apply`) puts the file right.
//
// A missing file is blocked for a region file, which is the person's file with a region in it and has no skeleton to create, and created for the mesh view,
// which is a whole file that tink owns, as `plan` and `join apply dns` create theirs. The directory it goes in must be there: a write into a missing one is failing.
func ReconcileDerived(server incus.InstanceServer, f Files, r Restarter, site string) []DerivedOutcome {
	files, problems, err := DerivedFromServer(server, site)
	if err != nil {
		return []DerivedOutcome{{Site: site, State: DerivedFailing, Reason: "the server could not be read", Err: err}}
	}
	var out []DerivedOutcome
	for _, p := range problems {
		out = append(out, DerivedOutcome{Site: site, Name: strings.TrimPrefix(p.Key, resolve.DeriveStampPrefix), State: DerivedBlocked, Reason: "a derived-file stamp cannot be read",
			Findings: []string{fmt.Sprintf("%s/%s: %s", p.Project, p.Instance, p.Reason)}})
	}
	if len(files) == 0 {
		if len(out) == 0 {
			out = append(out, DerivedOutcome{Site: site, State: DerivedSkipped})
		}
		return out
	}

	blockAll := func(reason string, cause error, findings []string) []DerivedOutcome {
		for _, d := range files {
			out = append(out, DerivedOutcome{Site: site, Name: d.Name, State: DerivedBlocked, Reason: reason, Err: cause, Findings: findings})
		}
		return out
	}
	stacks, err := FromServer(server, site)
	var se *StampError
	if errors.As(err, &se) {
		var fs []string
		for _, fi := range se.Findings() {
			fs = append(fs, fi.String())
		}
		return blockAll("a publish stamp cannot be read", err, fs)
	}
	if err != nil {
		for _, d := range files {
			out = append(out, DerivedOutcome{Site: site, Name: d.Name, State: DerivedFailing, Reason: "the server could not be read", Err: err})
		}
		return out
	}
	res := Build(stacks)
	var findings []string
	for _, fi := range res.Findings {
		findings = append(findings, fi.String())
	}
	if res.Errors() > 0 {
		return blockAll("the join has errors", errors.New("the join has errors"), findings)
	}

	for _, d := range files {
		o := reconcileDerivedFile(f, r, res, d)
		o.Site, o.Name = site, d.Name
		o.Findings = append(findings[:len(findings):len(findings)], o.Findings...)
		out = append(out, o)
	}
	return out
}

// reconcileDerivedFile does one stamped file. The caller has checked that the join has no error.
func reconcileDerivedFile(f Files, r Restarter, res Result, sf StampedFile) DerivedOutcome {
	d := sf.DerivedFile
	blocked := func(reason, detail string) DerivedOutcome {
		return DerivedOutcome{State: DerivedBlocked, Reason: reason, Detail: []string{detail}}
	}
	failing := func(reason string, err error) DerivedOutcome {
		return DerivedOutcome{State: DerivedFailing, Reason: reason, Err: err}
	}

	if sf.Status != "Running" {
		return DerivedOutcome{State: DerivedSkipped, Reason: "the instance is not running", Detail: []string{fmt.Sprintf("%s is %s: a file written now could land beneath a mount, so it is left for the next pass after it starts", d.Instance, strings.ToLower(sf.Status))}}
	}

	var rendering string
	switch d.Render {
	case resolve.DeriveCaddy:
		rendering = res.Caddy
	case resolve.DeriveSSO:
		rendering = res.SSO
	case resolve.DeriveDNS:
		rendering = res.DNS
	case resolve.DeriveMesh:
		if !res.HasMesh {
			return blocked("the site has no mesh helper", fmt.Sprintf("%s: derive: mesh, but no helper has role mesh (user.tink.role: mesh), so there is no mesh view to write", d.Name))
		}
		rendering = res.Mesh
	}

	live, found, err := f.Read(d.Project, d.Instance, d.Path)
	if err != nil {
		return failing("the file could not be read", fmt.Errorf("reading %s on %s: %w", d.Path, d.Instance, err))
	}
	if !found && d.Render != resolve.DeriveMesh {
		return blocked("the file is not there", fmt.Sprintf("%s does not exist on %s: the daemon keeps a region of a file, it does not create the file; `tink plan apply` of the stack places it", d.Path, d.Instance))
	}

	var desired string
	if d.Render == resolve.DeriveMesh {
		desired = rendering
		if desired == "" {
			desired = "[]\n"
		}
	} else {
		desired, err = resolve.FillDerived(live, rendering)
		if err != nil {
			return blocked("the file has no region to fill", fmt.Sprintf("%s on %s: %v; the daemon does not place the markers, `tink plan apply` of the stack does", d.Path, d.Instance, err))
		}
	}

	if d.Render == resolve.DeriveDNS {
		if refusal, err := dnsClash(f, res, d); err != nil {
			return failing("the zone file could not be read", err)
		} else if refusal != "" {
			return blocked("a name clashes with the platform's own", refusal)
		}
	}

	if live == desired {
		return DerivedOutcome{State: DerivedUnchanged}
	}
	if refusal, _ := resolve.CheckDerived(resolve.DerivedCheck{Render: d.Render, Path: d.Path, Instance: d.Instance, Live: live, Desired: desired, Zone: res.Zone}); refusal != "" {
		return blocked("the file is not the daemon's to replace", refusal)
	}

	if err := f.Write(d.Project, d.Instance, d.Path, desired); err != nil {
		return failing("the file could not be written", fmt.Errorf("writing %s on %s: %w", d.Path, d.Instance, err))
	}
	back, ok, err := f.Read(d.Project, d.Instance, d.Path)
	if err != nil {
		return failing("the file could not be read back", fmt.Errorf("the write succeeded but reading %s back failed: %w", d.Path, err))
	}
	if !ok || back != desired {
		return failing("the file does not hold what was written", fmt.Errorf("the write succeeded but %s on %s does not hold what was written", d.Path, d.Instance))
	}
	verb := "rewritten"
	if !found {
		verb = "created"
	}
	o := DerivedOutcome{State: DerivedChanged, Wrote: true, Detail: []string{fmt.Sprintf("%s on %s %s and read back", d.Path, d.Instance, verb)}}
	if d.Restart {
		if err := r.Restart(d.Project, d.Instance); err != nil {
			o.State, o.Reason, o.Err = DerivedFailing, "the instance could not be restarted", fmt.Errorf("%s was rewritten, but restarting %s failed: %w", d.Path, d.Instance, err)
			return o
		}
		o.Detail = append(o.Detail, "restarted "+d.Instance)
	}
	return o
}

// dnsClash holds a derived dns file to the rule of the applier it replaces: a published name that is already a record in the zone file would be taken over by
// the generated record, which answers first. The zone file is the one on the same instance, at /db.<zone>. It returns the refusal, or an error when the zone
// file cannot be read; a zone file that is not there is a refusal too (the dns helper is not laid out the way the join expects), never a guess.
func dnsClash(f Files, res Result, d resolve.DerivedFile) (string, error) {
	if res.Zone == "" {
		return fmt.Sprintf("the dns helper states no zone (%s), so the zone file cannot be checked for names it already holds", zoneKey), nil
	}
	zonePath := DNSZonePath(res.Zone)
	zone, found, err := f.Read(d.Project, d.Instance, zonePath)
	if err != nil {
		return "", fmt.Errorf("reading %s on %s: %w", zonePath, d.Instance, err)
	}
	if !found {
		return fmt.Sprintf("%s does not exist on %s: the dns helper is not laid out the way the join expects (docs/join.md), so it cannot check the published names against the platform's own", zonePath, d.Instance), nil
	}
	owners := ZoneOwners(zone, res.Zone)
	var clash []string
	for _, p := range res.Published {
		if owners[p.Label] {
			clash = append(clash, p.Label)
		}
	}
	if len(clash) > 0 {
		return fmt.Sprintf("%s already holds a record for %s, and a generated record would answer first and take the name over: rename the publish", zonePath, strings.Join(clash, ", ")), nil
	}
	return "", nil
}
