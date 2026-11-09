package join

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/instance"
	"github.com/minihci/tink/internal/observe"
	"github.com/minihci/tink/internal/resolve"
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

// A StampedFile is a derived file as its stamp says it, with the state of the instance it is on, as the list of instances showed it.
type StampedFile struct {
	resolve.DerivedFile
	// Status is the instance's state: "Running", "Stopped", ...
	Status string
}

// DerivedFromServer reads the stamps of the derived files (resolve.DeriveStampPrefix) from the instances of one site. A stamp that cannot be read is never
// skipped or repaired: it is returned as a problem beside the files that could be read, and the caller blocks that file and keeps the others (they are
// independent, unlike the publish stamps, which together make one rendering).
//
// The stamps of an instance that is a copy of another, or that has no resource stamp yet, are not read (readableStamps): a copy of an instance that carries a
// derived file would have the file written, and be restarted, beside the original. The third result is one finding for each such instance.
func DerivedFromServer(server incus.InstanceServer, site string) ([]StampedFile, []StampProblem, []finding.Finding, error) {
	insts, err := server.GetInstancesAllProjects(api.InstanceTypeAny)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("listing the instances of every project: %w", err)
	}
	sort.Slice(insts, func(i, j int) bool {
		if insts[i].Project != insts[j].Project {
			return insts[i].Project < insts[j].Project
		}
		return insts[i].Name < insts[j].Name
	})
	var files []StampedFile
	var bad []StampProblem
	var notes []finding.Finding
	for _, in := range insts {
		cfg, ig := readableStamps(in)
		if ig != nil && hasDeriveStamp(in.Config) && memberOf(in.Config, true, site) {
			notes = append(notes, ig.Finding) // said for the site the stamps were in: an instance of another site is not this view's business
		}
		in.Config = cfg
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
	return files, bad, notes, nil
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
//
// One observe.Result comes back per file (Resource is the file document's name) and one for a result about the whole site (no Resource): nothing stamped,
// or a server that could not be read.
func ReconcileDerived(server incus.InstanceServer, f Files, r Restarter, site string) []observe.Result {
	files, problems, ignored, err := DerivedFromServer(server, site)
	if err != nil {
		return []observe.Result{{What: "derived", Site: site, State: observe.Failing, Reason: "the server could not be read", Err: err}}
	}
	var out []observe.Result
	for _, p := range problems {
		name := strings.TrimPrefix(p.Key, resolve.DeriveStampPrefix)
		out = append(out, observe.Result{What: "derived", Resource: name, Site: site, State: observe.Blocked, Reason: "a derived-file stamp cannot be read",
			Detail: []finding.Finding{finding.Errorf("a derived-file stamp cannot be read", "derived/"+name, "%s/%s: %s", p.Project, p.Instance, p.Reason)}})
	}
	if len(files) == 0 {
		if len(out) == 0 {
			out = append(out, observe.Result{What: "derived", Site: site, State: observe.Skipped, Detail: addMissing(nil, ignored)})
		}
		return out
	}

	blockAll := func(reason string, cause error, findings []finding.Finding) []observe.Result {
		for _, d := range files {
			out = append(out, observe.Result{What: "derived", Resource: d.Name, Site: site, State: observe.Blocked, Reason: reason, Err: cause, Detail: findings})
		}
		return out
	}
	stacks, err := FromServer(server, site)
	var se *StampError
	if errors.As(err, &se) {
		return blockAll("a publish stamp cannot be read", err, se.Findings())
	}
	if err != nil {
		for _, d := range files {
			out = append(out, observe.Result{What: "derived", Resource: d.Name, Site: site, State: observe.Failing, Reason: "the server could not be read", Err: err})
		}
		return out
	}
	res := Build(stacks)
	findings := res.Findings
	findings = addMissing(findings, ignored) // the join says each instance whose stamps were ignored; this says it too if the join was not asked about it
	if res.Errors() > 0 {
		return blockAll("the join has errors", errors.New("the join has errors"), findings)
	}

	for _, d := range files {
		o := reconcileDerivedFile(f, r, res, d)
		o.What, o.Site, o.Resource = "derived", site, d.Name
		o.Detail = append(findings[:len(findings):len(findings)], o.Detail...)
		out = append(out, o)
	}
	return out
}

// addMissing appends the findings that are not in fs yet (the same text), so a finding the join already said is not said twice.
func addMissing(fs, more []finding.Finding) []finding.Finding {
	for _, f := range more {
		if !slices.ContainsFunc(fs, func(g finding.Finding) bool { return g.String() == f.String() }) {
			fs = append(fs, f)
		}
	}
	return fs
}

// reconcileDerivedFile does one stamped file. The caller has checked that the join has no error.
func reconcileDerivedFile(f Files, r Restarter, res Result, sf StampedFile) observe.Result {
	d := sf.DerivedFile
	subject := "derived/" + d.Name
	blocked := func(reason, detail string) observe.Result {
		return observe.Result{State: observe.Blocked, Reason: reason, Detail: []finding.Finding{finding.Errorf(reason, subject, "%s", detail)}}
	}
	failing := func(reason string, err error) observe.Result {
		return observe.Result{State: observe.Failing, Reason: reason, Err: err}
	}

	if sf.Status != "Running" {
		return observe.Result{State: observe.Skipped, Reason: "the instance is not running", Detail: []finding.Finding{finding.Notef("the instance is not running", subject, "%s is %s: a file written now could land beneath a mount, so it is left for the next pass after it starts", d.Instance, strings.ToLower(sf.Status))}}
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
		return observe.Result{State: observe.Converged}
	}
	for _, fi := range resolve.CheckDerived(resolve.DerivedCheck{Render: d.Render, Path: d.Path, Instance: d.Instance, Live: live, Desired: desired, Zone: res.Zone}) {
		if fi.Severity == finding.Error {
			return blocked("the file is not the daemon's to replace", fi.Detail)
		}
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
	o := observe.Result{State: observe.Converged, Wrote: true, Detail: []finding.Finding{finding.Notef("the file was rewritten", subject, "%s on %s %s and read back", d.Path, d.Instance, verb)}}
	if d.Restart {
		if err := r.Restart(d.Project, d.Instance); err != nil {
			o.State, o.Reason, o.Err = observe.Failing, "the instance could not be restarted", fmt.Errorf("%s was rewritten, but restarting %s failed: %w", d.Path, d.Instance, err)
			return o
		}
		o.Restarted = true
		o.Detail = append(o.Detail, finding.Notef("the instance was restarted", subject, "restarted %s", d.Instance))
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
