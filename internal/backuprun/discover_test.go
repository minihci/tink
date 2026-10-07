package backuprun

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

// policyFor is the text apply writes for a volume copying to the named targets.
func policyFor(t *testing.T, targets ...string) string {
	t.Helper()
	r := vol("v", targets...)
	ts := map[string]resolve.Resource{}
	for _, n := range targets {
		ts[n] = target(n, "")
	}
	text, err := resolve.BuildPolicy(r, ts)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func listed(project, pool, name string, cfg map[string]string) volbackup.ListedVolume {
	return volbackup.ListedVolume{Volume: volbackup.Volume{Project: project, Pool: pool, Name: name}, Config: cfg}
}

func labels(items []Item) []string {
	var o []string
	for _, it := range items {
		o = append(o, it.Label)
	}
	return o
}

func TestDiscoverFindsVolumesByTheirPolicyInEveryProject(t *testing.T) {
	pol := policyFor(t, "nas")
	s := &stub{listed: []volbackup.ListedVolume{
		listed("tenant-b", "default", "lib", map[string]string{resolve.PolicyKey: pol}),
		listed("default", "default", "docs", map[string]string{resolve.PolicyKey: pol}),
		listed("tenant-a", "default", "lib", map[string]string{resolve.PolicyKey: pol}), // the same name in two projects
		listed("default", "default", "plain", map[string]string{"snapshots.schedule": "@daily"}),
	}}
	items, problems, err := Discover(s)
	if err != nil || len(problems) != 0 {
		t.Fatal(err, problems)
	}
	if want := []string{"docs", "tenant-a/lib", "tenant-b/lib"}; !reflect.DeepEqual(labels(items), want) {
		t.Errorf("items = %v, want %v (the default project's volumes are named plainly, the others by project; a volume with no policy is not an item)", labels(items), want)
	}
	it := items[1]
	if it.Volume != (volbackup.Volume{Project: "tenant-a", Pool: "default", Name: "lib"}) {
		t.Errorf("the item knows exactly where the volume is: %+v", it.Volume)
	}
	if len(it.Copies) != 1 || it.Copies[0].Target != (volbackup.Target{Name: "nas", Pool: "p-nas"}) || it.Copies[0].Schedule != "@hourly" || it.Copies[0].Retain != "30d" {
		t.Errorf("copies = %+v: the target comes from the policy, not from any stack", it.Copies)
	}
}

func TestDiscoverNeverSchedulesABackupForABackup(t *testing.T) {
	pol := policyFor(t, "nas")
	s := &stub{listed: []volbackup.ListedVolume{
		listed("", "nas", "lib-bk-1", map[string]string{resolve.PolicyKey: pol, resolve.MarkerCopyOf: "default/default/lib"}),
		listed("", "nas", "lib-bk-2", map[string]string{resolve.PolicyKey: pol, resolve.MarkerPartialOf: "default/default/lib"}),
		listed("", "default", "lib", map[string]string{resolve.PolicyKey: pol}),
	}}
	items, problems, _ := Discover(s)
	if !reflect.DeepEqual(labels(items), []string{"lib"}) || len(problems) != 0 {
		t.Errorf("a restore point, or a copy still being made, that inherited a policy is not an item: %v %v", labels(items), problems)
	}
}

func TestDiscoverSkipsAPolicyItCannotUnderstandAndReportsIt(t *testing.T) {
	good := policyFor(t, "nas")
	s := &stub{listed: []volbackup.ListedVolume{
		listed("", "default", "good", map[string]string{resolve.PolicyKey: good}),
		listed("", "default", "garbled", map[string]string{resolve.PolicyKey: "{nope"}),
		listed("t", "default", "newer", map[string]string{resolve.PolicyKey: `{"proto":2}`}),
		listed("", "default", "verify-only", map[string]string{resolve.PolicyKey: `{"proto":1,"verify":{"every":"weekly"}}`}),
	}}
	items, problems, err := Discover(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(labels(items), []string{"good"}) {
		t.Errorf("the volumes with a policy it understands still run: %v", labels(items))
	}
	if problems["garbled"] == nil || problems["t/newer"] == nil || len(problems) != 2 {
		t.Errorf("each volume that cannot be understood is reported by name (a verify-only policy is fine, it has nothing to copy): %v", problems)
	}
	if !strings.Contains(problems["t/newer"].Error(), "protocol 2") {
		t.Errorf("a newer protocol says so: %v", problems["t/newer"])
	}
}

func TestDiscoverReportsAPoolThatCannotBeListedAndKeepsTheRest(t *testing.T) {
	s := &stub{
		listed:   []volbackup.ListedVolume{listed("", "default", "lib", map[string]string{resolve.PolicyKey: policyFor(t, "nas")})},
		poolErrs: map[string]error{"nas": errors.New("unreachable")},
	}
	items, problems, err := Discover(s)
	if err != nil || len(items) != 1 || problems["pool nas"] == nil {
		t.Errorf("%v %v %v", err, labels(items), problems)
	}
	if _, _, err := Discover(&stub{listErr: errors.New("incus is down")}); err == nil {
		t.Error("when nothing can be listed at all that is an error, not an empty server")
	}
}

func TestARunOverDiscoveredVolumesUsesTheirOwnMessagesAndNames(t *testing.T) {
	pol := policyFor(t, "nas")
	s := &stub{listed: []volbackup.ListedVolume{
		listed("tenant-a", "default", "lib", map[string]string{resolve.PolicyKey: pol}),
		listed("tenant-b", "default", "lib", map[string]string{resolve.PolicyKey: pol}),
	}}
	items, _, _ := Discover(s)
	var out bytes.Buffer
	opts := Options{Now: func() time.Time { return now }, UnknownMsg: "not a volume with a copy policy", EmptyMsg: "nothing to do: no volume carries a copy policy"}

	// by project/name only that one; the other project's volume of the same name is not touched
	opts.Volumes = []string{"tenant-a/lib", "nope"}
	rep, err := Run(context.Background(), s, items, opts, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.copied, []string{"lib->nas"}) || rep.Copies[0].Volume != "tenant-a/lib" {
		t.Errorf("copied %v, report %+v", s.copied, rep.Copies)
	}
	if !strings.Contains(out.String(), "nope: not a volume with a copy policy") {
		t.Errorf("output:\n%s", out.String())
	}

	// a bare name matches both projects' volumes
	s.copied = nil
	opts.Volumes = []string{"lib"}
	if _, err := Run(context.Background(), s, items, opts, &out); err != nil || len(s.copied) != 2 {
		t.Errorf("a bare name runs every volume of that name: %v %v", s.copied, err)
	}

	out.Reset()
	opts.Volumes = nil
	if _, err := Run(context.Background(), s, nil, opts, &out); err != nil || !strings.Contains(out.String(), "no volume carries a copy policy") {
		t.Errorf("%v %q", err, out.String())
	}
}

func declared(name, project, pool string) resolve.Resource {
	r := vol(name, "t")
	r.Project, r.Pool = project, pool
	return r
}

func stackNamed(name string) resolve.Resource {
	return resolve.Resource{Kind: resolve.KindStack, Name: name}
}

func orphanLabels(os []Orphan) []string {
	var o []string
	for _, x := range os {
		l := Label(x.Volume)
		if x.Owned {
			l += "*"
		}
		o = append(o, l)
	}
	return o
}

func TestUndeclaredFindsVolumesStillCopiedThatTheStackNoLongerDeclares(t *testing.T) {
	pol := map[string]string{resolve.PolicyKey: policyFor(t, "nas")}
	stack := []resolve.Resource{declared("lib", "", ""), declared("tenant-lib", "tenant", "fast"), target("t", "")}
	s := &stub{listed: []volbackup.ListedVolume{
		listed("default", "default", "lib", pol),                                                 // declared: fine
		listed("default", "default", "dropped", pol),                                             // taken out of the YAML, still copied
		listed("tenant", "fast", "tenant-lib", pol),                                              // declared, in a project and pool of its own
		listed("tenant", "fast", "old", pol),                                                     // dropped, in that project and pool
		listed("tenant", "default", "elsewhere", pol),                                            // a pool the stack does not declare volumes in: not this stack's to speak for
		listed("other", "default", "theirs", pol),                                                // a project the stack never mentions
		listed("default", "default", "plain", map[string]string{"snapshots.schedule": "@daily"}), // no policy: nothing is copying it
		listed("default", "default", "dropped-bk-1", map[string]string{resolve.PolicyKey: pol[resolve.PolicyKey], resolve.MarkerCopyOf: "default/default/dropped"}),
		listed("default", "default", "dropped-bk-2", map[string]string{resolve.PolicyKey: pol[resolve.PolicyKey], resolve.MarkerPartialOf: "default/default/dropped"}),
	}}
	got, err := Undeclared(s, stack)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"dropped", "tenant/old"}; !reflect.DeepEqual(orphanLabels(got), want) {
		t.Errorf("undeclared = %v, want %v (an unnamed stack can only guess, so none is marked as owned)", orphanLabels(got), want)
	}
	if got[1].Volume.Pool != "fast" {
		t.Errorf("the volume keeps its pool, so it can be forgotten: %+v", got[1])
	}
}

func TestANamedStackFindsItsOwnVolumesExactlyAndLeavesOthersAlone(t *testing.T) {
	policy := policyFor(t, "nas")
	with := func(owner string) map[string]string {
		c := map[string]string{resolve.PolicyKey: policy}
		if owner != "" {
			c[resolve.StackKey] = owner
		}
		return c
	}
	stack := []resolve.Resource{stackNamed("immich"), declared("lib", "", ""), target("t", "")}
	s := &stub{listed: []volbackup.ListedVolume{
		listed("default", "default", "lib", with("immich")),       // declared
		listed("default", "default", "old", with("immich")),       // ours, dropped: exact
		listed("moved", "fast", "far", with("immich")),            // ours, in a project and pool the stack no longer mentions: still found
		listed("default", "default", "theirs", with("nextcloud")), // another stack's, in a place this stack uses: never mentioned
		listed("default", "default", "unstamped", with("")),       // no pointer, in this stack's place: a guess
		listed("elsewhere", "default", "unstamped2", with("")),    // no pointer and not in this stack's places: not mentioned
	}}
	got, err := Undeclared(s, stack)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"moved/far*", "old*", "unstamped"}; !reflect.DeepEqual(orphanLabels(got), want) {
		t.Errorf("undeclared = %v, want %v (* marks the ones that point back at this stack)", orphanLabels(got), want)
	}
	// a stack with a name but no volumes of its own left still finds what it applied
	got, _ = Undeclared(s, []resolve.Resource{stackNamed("immich")})
	if want := []string{"moved/far*", "old*", "lib*"}; len(got) != 3 {
		t.Errorf("all three of its volumes are now dropped: %v, want %v", orphanLabels(got), want)
	}
}

func TestUndeclaredRejectsTwoStackNamesAndSaysNothingForAnUnnamedStackWithNoVolumes(t *testing.T) {
	pol := map[string]string{resolve.PolicyKey: policyFor(t, "nas")}
	s := &stub{listed: []volbackup.ListedVolume{listed("default", "default", "x", pol)}}
	if got, err := Undeclared(s, []resolve.Resource{target("t", "")}); err != nil || got != nil {
		t.Errorf("an unnamed stack that declares no volume has no scope to speak for: %v %v", got, err)
	}
	if _, err := Undeclared(s, []resolve.Resource{stackNamed("a"), stackNamed("b")}); err == nil {
		t.Error("a stack is named once")
	}
	// a stack that says project: default and pool: default explicitly is the same place as one that says nothing
	got, _ := Undeclared(s, []resolve.Resource{declared("lib", "default", "default"), target("t", "")})
	if !reflect.DeepEqual(orphanLabels(got), []string{"x"}) {
		t.Errorf("explicit defaults must match the listing's: %v", orphanLabels(got))
	}
	if _, err := Undeclared(&stub{listErr: errors.New("incus is down")}, []resolve.Resource{declared("lib", "", "")}); err == nil {
		t.Error("a listing that fails is an error, not 'nothing undeclared'")
	}
}
