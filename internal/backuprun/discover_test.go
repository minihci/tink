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
