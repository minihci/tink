package join

import (
	"errors"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

// fakeRestarter records the instances it was asked to restart.
type fakeRestarter struct {
	calls []string
	err   error
}

func (r *fakeRestarter) Restart(project, instance string) error {
	r.calls = append(r.calls, project+"/"+instance)
	return r.err
}

const (
	routesPath = "/etc/caddy/apps.caddy"
	meshPath   = "/etc/headscale/extra_records.json"
	emptyShell = "# my own routes\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"
)

// withDerived puts a derived file on the platform stack of the fixture, so planning it stamps the instance the way `plan apply` would.
func withDerived(t *testing.T, files ...resolve.Resource) []Stack {
	t.Helper()
	stacks := load(t)
	stacks[0].Resources = append(stacks[0].Resources, files...)
	return stacks
}

func routesFile(restart bool) resolve.Resource {
	return resolve.Resource{Kind: resolve.KindFile, Name: "ingress-routes", Project: "tink", Instance: "ingress", Path: routesPath, Derive: resolve.DeriveCaddy, Restart: restart, Content: emptyShell}
}

func ssoFile() resolve.Resource {
	return resolve.Resource{Kind: resolve.KindFile, Name: "sso-rules", Project: "tink", Instance: "sso", Path: "/app/configuration.yml", Derive: resolve.DeriveSSO, Restart: true,
		Content: "rules: []\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"}
}

func dnsFile() resolve.Resource {
	return resolve.Resource{Kind: resolve.KindFile, Name: "dns-hosts", Project: "tink", Instance: "dns", Path: "/hosts", Derive: resolve.DeriveDNS,
		Content: resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"}
}

func meshFile() resolve.Resource {
	return resolve.Resource{Kind: resolve.KindFile, Name: "mesh-records", Project: "tink", Instance: "mesh", Path: meshPath, Derive: resolve.DeriveMesh}
}

func reconcileDerived(f *serverFake, files *fakeFiles, r *fakeRestarter) []DerivedOutcome {
	return ReconcileDerived(f, files, r, "")
}

func one(t *testing.T, outs []DerivedOutcome, name string) DerivedOutcome {
	t.Helper()
	for _, o := range outs {
		if o.Name == name {
			return o
		}
	}
	t.Fatalf("no outcome for %q in %+v", name, outs)
	return DerivedOutcome{}
}

func TestAHandEditInsideTheRegionIsPutBackAndTheRestOfTheFileIsKept(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(false)))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell

	first := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if first.State != DerivedChanged || !first.Wrote {
		t.Fatalf("first = %+v, want changed", first)
	}
	got := files.files[fkey("tink", "ingress", routesPath)]
	if !strings.HasPrefix(got, "# my own routes\n"+resolve.DeriveBegin+"\n") || !strings.Contains(got, "@whoami host whoami.home.example.org") {
		t.Fatalf("the file holds:\n%s", got)
	}

	// someone edits inside the region, and adds a line of their own outside it
	edited := strings.Replace(got, "@whoami host whoami.home.example.org", "@whoami host elsewhere.example.org", 1) + "# and another of mine\n"
	files.files[fkey("tink", "ingress", routesPath)] = edited
	files.writes = nil
	second := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if second.State != DerivedChanged {
		t.Fatalf("second = %+v, want the edit put back", second)
	}
	back := files.files[fkey("tink", "ingress", routesPath)]
	if strings.Contains(back, "elsewhere.example.org") || !strings.Contains(back, "@whoami host whoami.home.example.org") {
		t.Errorf("the region was not put back:\n%s", back)
	}
	if !strings.HasSuffix(back, "# and another of mine\n") || !strings.HasPrefix(back, "# my own routes\n") {
		t.Errorf("the lines outside the region were not kept:\n%s", back)
	}

	files.writes = nil
	third := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if third.State != DerivedUnchanged || len(files.writes) != 0 {
		t.Errorf("third = %+v, writes %v: a file that is right is not written", third, files.writes)
	}
}

func TestTheInstanceIsRestartedOnlyWhenTheStampSaysSoAndTheFileChanged(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(true), dnsFile()))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	files.files[fkey("tink", "dns", "/hosts")] = resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"

	outs := reconcileDerived(f, files, rs)
	if one(t, outs, "ingress-routes").State != DerivedChanged || one(t, outs, "dns-hosts").State != DerivedChanged {
		t.Fatalf("outcomes = %+v", outs)
	}
	if len(rs.calls) != 1 || rs.calls[0] != "tink/ingress" {
		t.Errorf("restarted %v: only the file whose stamp says restart, once", rs.calls)
	}
	rs.calls = nil
	reconcileDerived(f, files, rs)
	if len(rs.calls) != 0 {
		t.Errorf("an unchanged file restarted %v", rs.calls)
	}
}

func TestARestartThatFailsIsFailingAfterAGoodWrite(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(true)))
	files, rs := platformFiles(), &fakeRestarter{err: errors.New("busy")}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	o := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if o.State != DerivedFailing || o.Reason != "the instance could not be restarted" || !o.Wrote {
		t.Errorf("outcome = %+v", o)
	}
}

func TestAFileThatIsNotThereOrHasNoMarkersIsBlockedAndLeftAlone(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(false)))
	files, rs := platformFiles(), &fakeRestarter{}
	o := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if o.State != DerivedBlocked || o.Reason != "the file is not there" || len(files.writes) != 0 {
		t.Errorf("missing file: %+v, writes %v", o, files.writes)
	}

	files.files[fkey("tink", "ingress", routesPath)] = "reverse_proxy 192.0.2.5:80\n"
	o = one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if o.State != DerivedBlocked || o.Reason != "the file has no region to fill" || len(files.writes) != 0 {
		t.Errorf("no markers: %+v, writes %v", o, files.writes)
	}
	if got := files.files[fkey("tink", "ingress", routesPath)]; got != "reverse_proxy 192.0.2.5:80\n" {
		t.Errorf("a file tink did not write was changed: %q", got)
	}
}

func TestAnEmptyRenderingDoesNotEmptyARegionThatHasLines(t *testing.T) {
	stacks := withDerived(t, routesFile(false))
	stacks = stacks[:1] // the platform alone: nothing is published
	f := serverOf(t, stacks)
	files, rs := platformFiles(), &fakeRestarter{}
	live := "# mine\n" + resolve.DeriveBegin + "\n@old host old.home.example.org\n" + resolve.DeriveEnd + "\n"
	files.files[fkey("tink", "ingress", routesPath)] = live

	o := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if o.State != DerivedBlocked || o.Reason != "the file is not the daemon's to replace" || !strings.Contains(strings.Join(o.Detail, " "), "nothing is published") {
		t.Fatalf("outcome = %+v", o)
	}
	if files.files[fkey("tink", "ingress", routesPath)] != live || len(files.writes) != 0 {
		t.Error("the region was emptied")
	}
}

func TestAJoinErrorBlocksEveryFileAndWritesNothing(t *testing.T) {
	stacks := withDerived(t, routesFile(false), ssoFile())
	for _, r := range stacks[0].Resources {
		if r.Kind == resolve.KindInstance && r.Config[resolve.RoleKey] == "ingress" {
			delete(r.Config, resolve.RoleKey) // no ingress helper: the join cannot render the routes
		}
	}
	f := serverOf(t, stacks)
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	files.files[fkey("tink", "sso", "/app/configuration.yml")] = "rules: []\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"

	outs := reconcileDerived(f, files, rs)
	for _, name := range []string{"sso-rules"} {
		o := one(t, outs, name)
		if o.State != DerivedBlocked || o.Reason != "the join has errors" {
			t.Errorf("%s = %+v", name, o)
		}
	}
	if len(files.writes) != 0 || len(rs.calls) != 0 {
		t.Errorf("a join error wrote %v and restarted %v", files.writes, rs.calls)
	}
}

func TestAStampThatCannotBeReadBlocksThatFileAndNotTheOthers(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(false), dnsFile()))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	files.files[fkey("tink", "dns", "/hosts")] = resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"
	for i := range f.instances {
		if f.instances[i].Name == "dns" {
			f.instances[i].Config[resolve.DeriveStampKey("dns-hosts")] = `{"path":"/hosts","render":"nginx","restart":false}`
		}
	}

	outs := reconcileDerived(f, files, rs)
	if o := one(t, outs, "dns-hosts"); o.State != DerivedBlocked || o.Reason != "a derived-file stamp cannot be read" {
		t.Errorf("dns-hosts = %+v", o)
	}
	if o := one(t, outs, "ingress-routes"); o.State != DerivedChanged {
		t.Errorf("ingress-routes = %+v: an unreadable stamp elsewhere must not stop it", o)
	}
}

func TestAPublishStampThatCannotBeReadBlocksEveryFile(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(false)))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	f.whoami(t).Config["user.tink.publish.whoami"] = `{"allow":"family"}`

	o := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if o.State != DerivedBlocked || o.Reason != "a publish stamp cannot be read" || len(files.writes) != 0 {
		t.Errorf("outcome = %+v, writes %v", o, files.writes)
	}
}

func TestWritesThatFailOrDoNotTakeAreFailingNotBlocked(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(false)))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell

	files.writeErr = errors.New("denied")
	if o := one(t, reconcileDerived(f, files, rs), "ingress-routes"); o.State != DerivedFailing || o.Reason != "the file could not be written" {
		t.Errorf("a failed write: %+v", o)
	}
	files.writeErr, files.corrupt = nil, true
	if o := one(t, reconcileDerived(f, files, rs), "ingress-routes"); o.State != DerivedFailing || o.Reason != "the file does not hold what was written" {
		t.Errorf("a write that does not take: %+v", o)
	}
	files.corrupt = false
	files.readErr[routesPath] = errors.New("gone")
	if o := one(t, reconcileDerived(f, files, rs), "ingress-routes"); o.State != DerivedFailing || o.Reason != "the file could not be read" {
		t.Errorf("a failed read: %+v", o)
	}
}

func TestASiteWithNoStampedFileIsSkippedAndNothingIsRead(t *testing.T) {
	f := serverOf(t, load(t))
	files, rs := platformFiles(), &fakeRestarter{}
	outs := reconcileDerived(f, files, rs)
	if len(outs) != 1 || outs[0].State != DerivedSkipped {
		t.Fatalf("outcomes = %+v", outs)
	}
	if len(files.reads) != 0 || len(files.writes) != 0 {
		t.Errorf("a skipped site touched a helper: reads %v writes %v", files.reads, files.writes)
	}
}

func TestADnsFileHoldsTheZoneFilesRule(t *testing.T) {
	f := serverOf(t, withDerived(t, dnsFile()))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "dns", "/hosts")] = resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"
	if o := one(t, reconcileDerived(f, files, rs), "dns-hosts"); o.State != DerivedChanged || !strings.Contains(files.files[fkey("tink", "dns", "/hosts")], "whoami.home.example.org") {
		t.Fatalf("the records were not written: %+v", o)
	}

	// the platform's own zone file holds a record for a published name: the generated record would take it over
	files.files[fkey("tink", "dns", "/db.home.example.org")] = labZone + "whoami IN A 192.0.2.99\n"
	files.files[fkey("tink", "dns", "/hosts")] = resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"
	files.writes = nil
	o := one(t, reconcileDerived(f, files, rs), "dns-hosts")
	if o.State != DerivedBlocked || o.Reason != "a name clashes with the platform's own" || len(files.writes) != 0 {
		t.Errorf("outcome = %+v, writes %v", o, files.writes)
	}

	delete(files.files, fkey("tink", "dns", "/db.home.example.org"))
	if o := one(t, reconcileDerived(f, files, rs), "dns-hosts"); o.State != DerivedBlocked {
		t.Errorf("no zone file: %+v, want blocked", o)
	}
}

func TestTheMeshViewIsTheWholeFileAndKeepsItsGuards(t *testing.T) {
	f := serverOf(t, withDerived(t, meshFile()))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "mesh", meshPath)] = "[]\n"

	o := one(t, reconcileDerived(f, files, rs), "mesh-records")
	if o.State != DerivedChanged || !strings.Contains(files.files[fkey("tink", "mesh", meshPath)], "whoami.home.example.org") {
		t.Fatalf("the mesh view was not written: %+v", o)
	}
	files.writes = nil
	if o := one(t, reconcileDerived(f, files, rs), "mesh-records"); o.State != DerivedUnchanged || len(files.writes) != 0 {
		t.Errorf("second = %+v", o)
	}

	// a file that is not a records file is not the daemon's to replace
	files.files[fkey("tink", "mesh", meshPath)] = "{\"not\": \"an array\"}\n"
	o = one(t, reconcileDerived(f, files, rs), "mesh-records")
	if o.State != DerivedBlocked || o.Reason != "the file is not the daemon's to replace" {
		t.Errorf("a foreign file: %+v", o)
	}
}

func TestAnotherSitesStampedFileIsNotReadOrWritten(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(false)))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	for i := range f.instances {
		if f.instances[i].Name == "ingress" {
			f.instances[i].Config[resolve.SiteKey] = "lab"
		}
	}
	outs := reconcileDerived(f, files, rs) // the unnamed site
	if len(outs) != 1 || outs[0].State != DerivedSkipped || len(files.writes) != 0 {
		t.Errorf("outcomes = %+v, writes %v: the file belongs to site lab", outs, files.writes)
	}
}

func TestAFileOnAnInstanceThatIsNotRunningIsSkippedAndNothingIsTouched(t *testing.T) {
	f := serverOf(t, withDerived(t, routesFile(true)))
	files, rs := platformFiles(), &fakeRestarter{}
	files.files[fkey("tink", "ingress", routesPath)] = emptyShell
	for i := range f.instances {
		if f.instances[i].Name == "ingress" {
			f.instances[i].Status = "Stopped"
		}
	}

	o := one(t, reconcileDerived(f, files, rs), "ingress-routes")
	if o.State != DerivedSkipped || o.Reason != "the instance is not running" {
		t.Fatalf("outcome = %+v", o)
	}
	if len(files.reads) != 0 || len(files.writes) != 0 || len(rs.calls) != 0 {
		t.Errorf("a stopped instance was touched: reads %v writes %v restarts %v", files.reads, files.writes, rs.calls)
	}

	// once it runs, the next pass keeps the file
	for i := range f.instances {
		if f.instances[i].Name == "ingress" {
			f.instances[i].Status = "Running"
		}
	}
	if o := one(t, reconcileDerived(f, files, rs), "ingress-routes"); o.State != DerivedChanged {
		t.Errorf("after it started: %+v", o)
	}
}

func TestAMissingMeshViewIsCreatedAndAMissingRegionFileIsNot(t *testing.T) {
	f := serverOf(t, withDerived(t, meshFile(), routesFile(false)))
	files, rs := platformFiles(), &fakeRestarter{}

	outs := reconcileDerived(f, files, rs)
	mesh := one(t, outs, "mesh-records")
	if mesh.State != DerivedChanged || !mesh.Wrote || !strings.Contains(files.files[fkey("tink", "mesh", meshPath)], "whoami.home.example.org") {
		t.Fatalf("the mesh view was not created: %+v", mesh)
	}
	if got := strings.Join(mesh.Detail, " "); !strings.Contains(got, "created") {
		t.Errorf("detail = %q, want it to say created", got)
	}
	if routes := one(t, outs, "ingress-routes"); routes.State != DerivedBlocked || routes.Reason != "the file is not there" {
		t.Errorf("a region file that is missing: %+v", routes)
	}
	if _, there := files.files[fkey("tink", "ingress", routesPath)]; there {
		t.Error("a region file was created")
	}

	// the directory has to be there: a write that fails is failing, not a created path
	files.files = map[string]string{"tink/dns/db.home.example.org": labZone}
	files.writeErr = errors.New("no such directory")
	if o := one(t, reconcileDerived(f, files, rs), "mesh-records"); o.State != DerivedFailing || o.Reason != "the file could not be written" {
		t.Errorf("a create that fails: %+v", o)
	}
}
