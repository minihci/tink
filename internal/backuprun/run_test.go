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

var now = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

func vol(name string, targets ...string) resolve.Resource {
	r := resolve.Resource{Kind: resolve.KindStorageVolume, Name: name, Backup: &resolve.VolumeBackup{}}
	for _, t := range targets {
		r.Backup.Copies = append(r.Backup.Copies, resolve.BackupCopy{Target: t, Schedule: "@hourly", Retain: "30d"})
	}
	return r
}

func target(name, remote string) resolve.Resource {
	return resolve.Resource{Kind: resolve.KindBackupTarget, Name: name, Engine: "incus", Location: "other-host", Pool: "p-" + name, Remote: remote}
}

// stub is an Engine that records copies and fails or reports as told.
type stub struct {
	live    map[string]map[string]string // volume -> live config
	liveErr map[string]error
	results map[string]volbackup.CopyResult // "vol->target"
	errs    map[string]error
	copied  []string
}

func (s *stub) LiveConfig(v volbackup.Volume) (map[string]string, error) {
	if err := s.liveErr[v.Name]; err != nil {
		return nil, err
	}
	return s.live[v.Name], nil
}

func (s *stub) Copy(v volbackup.Volume, t volbackup.Target, opts volbackup.CopyOptions) (volbackup.CopyResult, error) {
	k := v.Name + "->" + t.Name
	s.copied = append(s.copied, k)
	if err := s.errs[k]; err != nil {
		return volbackup.CopyResult{}, err
	}
	if res, ok := s.results[k]; ok {
		return res, nil
	}
	return volbackup.CopyResult{Volume: v.Name + "-bk-X", Planned: []string{"copy it"}}, nil
}

func run(t *testing.T, s *stub, rs []resolve.Resource, o Options) (Report, string, error) {
	t.Helper()
	var out bytes.Buffer
	if o.Now == nil {
		o.Now = func() time.Time { return now }
	}
	rep, err := Run(context.Background(), s, rs, o, &out)
	return rep, out.String(), err
}

func TestSelect(t *testing.T) {
	stack := []resolve.Resource{vol("a", "t"), target("t", ""), vol("b", "t"), vol("no-copies"), {Kind: resolve.KindStorageVolume, Name: "no-backup"}, vol("c", "t", "t")}
	names := func(rs []resolve.Resource) []string {
		var o []string
		for _, r := range rs {
			o = append(o, r.Name)
		}
		return o
	}
	for name, tc := range map[string]struct {
		args          []string
		want, unknown []string
	}{
		"no names means every volume with copies": {nil, []string{"a", "b", "c"}, nil},
		// The regression that once ran volumes the caller had not asked for: after the last named volume the filter switched off.
		"a named volume is the only one run, even with volumes after it": {[]string{"a"}, []string{"a"}, nil},
		"the middle one":                      {[]string{"b"}, []string{"b"}, nil},
		"several, in stack order":             {[]string{"c", "a"}, []string{"a", "c"}, nil},
		"a volume without copies is unknown":  {[]string{"no-copies"}, nil, []string{"no-copies"}},
		"a volume without a backup block":     {[]string{"no-backup"}, nil, []string{"no-backup"}},
		"a target is not a volume":            {[]string{"t"}, nil, []string{"t"}},
		"an unknown name beside a valid one":  {[]string{"nope", "a"}, []string{"a"}, []string{"nope"}},
		"a name given twice is reported once": {[]string{"nope", "nope"}, nil, []string{"nope"}},
		"a name given twice runs it once":     {[]string{"a", "a"}, []string{"a"}, nil},
	} {
		got, unknown := Select(stack, tc.args)
		if !reflect.DeepEqual(names(got), tc.want) || !reflect.DeepEqual(unknown, tc.unknown) {
			t.Errorf("%s: selected %v unknown %v; want %v %v", name, names(got), unknown, tc.want, tc.unknown)
		}
	}
}

func TestCheck(t *testing.T) {
	if err := Check(nil); err == nil || !strings.Contains(err.Error(), "no stack") {
		t.Errorf("an empty stack: %v", err)
	}
	if err := Check([]resolve.Resource{vol("a", "missing-target")}); err == nil {
		t.Error("a copy to an undeclared target must be an error")
	}
	if err := Check([]resolve.Resource{vol("a", "t"), target("t", "")}); err != nil {
		t.Errorf("a valid stack: %v", err)
	}
}

// The lines the command has always printed, exactly.
func TestRunPrintsWhatTheCommandAlwaysPrinted(t *testing.T) {
	s := &stub{results: map[string]volbackup.CopyResult{
		"a->t": {Volume: "a-bk-1", Pruned: []string{"a-bk-0", "a-bk-00"}},
		"b->t": {Volume: "b-bk-1", OtherServers: []string{"other"}},
	}}
	rep, out, err := run(t, s, []resolve.Resource{vol("a", "t"), vol("b", "t"), target("t", "")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := "copied a -> t: restore point a-bk-1 (pruned 2 older: a-bk-0, a-bk-00)\n" +
		"note: b -> t also holds restore points of a volume with this name made by other server(s) (other); tink leaves them alone\n" +
		"copied b -> t: restore point b-bk-1\n"
	if out != want {
		t.Errorf("output:\n%q\nwant:\n%q", out, want)
	}
	if rep.Tried != 2 || rep.Failed != 0 || rep.Err() != nil || len(rep.Copies) != 2 || rep.Copies[0].Outcome != Copied || rep.Copies[0].RestorePoint != "a-bk-1" {
		t.Errorf("report: %+v", rep)
	}
}

func TestDueSkipsWhatIsNotDueAndSaysWhy(t *testing.T) {
	mid := now.Add(30 * time.Minute) // 10:30: an hourly copy that ran at 10:20 is not due until 11:00
	recent := mid.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	stale := mid.Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	s := &stub{live: map[string]map[string]string{
		"fresh":   {resolve.CopyStampAt("t"): recent},
		"overdue": {resolve.CopyStampAt("t"): stale},
		"failing": {resolve.CopyStampAt("t"): stale, resolve.CopyFailAt("t"): mid.Add(-time.Minute).UTC().Format(time.RFC3339), resolve.CopyFailCount("t"): "2"},
		"never":   {},
	}}
	rep, out, err := run(t, s, []resolve.Resource{vol("fresh", "t"), vol("overdue", "t"), vol("failing", "t"), vol("never", "t"), target("t", "")},
		Options{Due: true, Now: func() time.Time { return mid }})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.copied, []string{"overdue->t", "never->t"}) {
		t.Errorf("only the due copies may run, got %v", s.copied)
	}
	if !strings.Contains(out, "fresh -> t: not yet due, next at") || !strings.Contains(out, "failing -> t: backing off after 2 failed attempt(s)") {
		t.Errorf("the reasons must be printed:\n%s", out)
	}
	if rep.Skipped != 2 || rep.Tried != 2 || rep.Failed != 0 {
		t.Errorf("report: %+v", rep)
	}
}

func TestOneFailureDoesNotStopTheOthers(t *testing.T) {
	s := &stub{errs: map[string]error{"a->t": errors.New("target is full")}}
	rep, out, err := run(t, s, []resolve.Resource{vol("a", "t"), vol("b", "t"), target("t", "")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.copied, []string{"a->t", "b->t"}) {
		t.Errorf("b must still be copied: %v", s.copied)
	}
	if !strings.Contains(out, "FAILED a -> t: target is full") || !strings.Contains(out, "copied b -> t") {
		t.Errorf("output:\n%s", out)
	}
	if rep.Failed != 1 || rep.Err() == nil || !strings.Contains(rep.Err().Error(), "1 copy operation(s) failed") {
		t.Errorf("report: %+v err=%v", rep, rep.Err())
	}
	if rep.Copies[0].Outcome != Failed || rep.Copies[0].Detail != "target is full" {
		t.Errorf("the failure must be in the report: %+v", rep.Copies[0])
	}
}

func TestABusyCopyIsSkippedNotFailed(t *testing.T) {
	s := &stub{errs: map[string]error{"a->t": volbackup.ErrBusy}}
	rep, out, _ := run(t, s, []resolve.Resource{vol("a", "t"), target("t", "")}, Options{})
	if rep.Failed != 0 || rep.Skipped != 1 || rep.Tried != 0 || rep.Err() != nil || !strings.Contains(out, "a -> t: already running") {
		t.Errorf("a busy copy is not a failure: %+v\n%s", rep, out)
	}
}

func TestDryRunPrintsWhatWouldHappen(t *testing.T) {
	s := &stub{}
	rep, out, _ := run(t, s, []resolve.Resource{vol("a", "t"), target("t", "")}, Options{DryRun: true})
	if !strings.Contains(out, "a -> t: would copy it") || strings.Contains(out, "copied") {
		t.Errorf("output:\n%s", out)
	}
	if rep.Copies[0].Outcome != Planned {
		t.Errorf("report: %+v", rep.Copies[0])
	}
}

func TestUnknownVolumesAreFailures(t *testing.T) {
	rep, out, _ := run(t, &stub{}, []resolve.Resource{vol("a", "t"), target("t", "")}, Options{Volumes: []string{"a", "nope"}})
	if rep.Failed != 1 || !strings.Contains(out, "nope: not a storage-volume with copies in the stack") || !reflect.DeepEqual(rep.Unknown, []string{"nope"}) {
		t.Errorf("%+v\n%s", rep, out)
	}
}

func TestNothingToDo(t *testing.T) {
	_, out, err := run(t, &stub{}, []resolve.Resource{target("t", "")}, Options{})
	if err != nil || !strings.Contains(out, "nothing to do") {
		t.Errorf("%v %q", err, out)
	}
}

func TestALiveConfigErrorFailsThatVolumeOnly(t *testing.T) {
	s := &stub{liveErr: map[string]error{"a": errors.New("no such volume")}}
	rep, out, _ := run(t, s, []resolve.Resource{vol("a", "t"), vol("b", "t"), target("t", "")}, Options{})
	if rep.Failed != 1 || !strings.Contains(out, "a: no such volume") || !strings.Contains(out, "copied b -> t") {
		t.Errorf("%+v\n%s", rep, out)
	}
}

func TestCancellationStopsBetweenCopies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	s := &stub{}
	cancel()
	rep, err := Run(ctx, s, []resolve.Resource{vol("a", "t"), vol("b", "t"), target("t", "")}, Options{Now: func() time.Time { return now }}, &out)
	if err != nil || len(s.copied) != 0 || rep.Skipped != 2 || rep.Failed != 0 {
		t.Errorf("a cancelled run copies nothing and does not call it a failure: %v copied=%v %+v", err, s.copied, rep)
	}
}

func TestTheRelayNoteIsPrintedOncePerRemoteTarget(t *testing.T) {
	rs := []resolve.Resource{vol("a", "far"), vol("b", "far"), vol("c", "near"), target("far", "vps"), target("near", "")}
	_, out, _ := run(t, &stub{}, rs, Options{Remote: "tron"})
	if strings.Count(out, "is relayed through this machine") != 1 {
		t.Errorf("one note, for the remote target only:\n%s", out)
	}
	_, out, _ = run(t, &stub{}, rs, Options{})
	if strings.Contains(out, "relayed") {
		t.Errorf("no note when tink is not pointed at a remote:\n%s", out)
	}
}

func TestAnInvalidStackStopsTheRun(t *testing.T) {
	if _, _, err := run(t, &stub{}, []resolve.Resource{vol("a", "missing")}, Options{}); err == nil {
		t.Error("a stack that does not validate must stop the run")
	}
}
