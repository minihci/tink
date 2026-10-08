package backuprun

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

var now = time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)

func vol(name string, targets ...string) resolve.Resource {
	r := resolve.Resource{Kind: resolve.KindStorageVolume, Name: name, Backup: &backupmeta.VolumeBackup{}}
	for _, t := range targets {
		r.Backup.Copies = append(r.Backup.Copies, backupmeta.BackupCopy{Target: t, Schedule: "@hourly", Retain: "30d"})
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
	nowSeen func() time.Time        // the clock the last copy was handed
	handed  []volbackup.CopyOptions // what each copy was handed, in order
	// what listing the server finds
	listed   []volbackup.ListedVolume
	poolErrs map[string]error
	listErr  error
}

func (s *stub) Volumes() ([]volbackup.ListedVolume, map[string]error, error) {
	return s.listed, s.poolErrs, s.listErr
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
	s.nowSeen = opts.Now
	s.handed = append(s.handed, opts)
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
	items, err := FromStack(rs)
	if err != nil {
		return Report{}, "", err
	}
	var out bytes.Buffer
	if o.Now == nil {
		o.Now = func() time.Time { return now }
	}
	rep, err := Run(context.Background(), s, items, o, &out)
	return rep, out.String(), err
}

func TestSelect(t *testing.T) {
	stack := []resolve.Resource{vol("a", "t"), target("t", ""), vol("b", "t"), vol("no-copies"), {Kind: resolve.KindStorageVolume, Name: "no-backup"}, vol("c", "t", "t")}
	items, err := FromStack(stack)
	if err != nil {
		t.Fatal(err)
	}
	names := func(its []Item) []string {
		var o []string
		for _, it := range its {
			o = append(o, it.Label)
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
		got, unknown := Select(items, tc.args)
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
		"fresh":   {backupmeta.CopyStampAt("t"): recent},
		"overdue": {backupmeta.CopyStampAt("t"): stale},
		"failing": {backupmeta.CopyStampAt("t"): stale, backupmeta.CopyFailAt("t"): mid.Add(-time.Minute).UTC().Format(time.RFC3339), backupmeta.CopyFailCount("t"): "2"},
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
	items, _ := FromStack([]resolve.Resource{vol("a", "t"), vol("b", "t"), target("t", "")})
	rep, err := Run(ctx, s, items, Options{Now: func() time.Time { return now }}, &out)
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

func TestAnInvalidStackCannotBeRun(t *testing.T) {
	if _, _, err := run(t, &stub{}, []resolve.Resource{vol("a", "missing")}, Options{}); err == nil {
		t.Error("a stack that does not validate must stop the run")
	}
}

func TestDueListsWhatARunWouldDoAndChangesNothing(t *testing.T) {
	mid := now.Add(30 * time.Minute)
	stale := mid.Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	recent := mid.Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	s := &stub{live: map[string]map[string]string{
		"overdue": {backupmeta.CopyStampAt("t"): stale},
		"fresh":   {backupmeta.CopyStampAt("t"): recent},
		"failing": {backupmeta.CopyStampAt("t"): stale, backupmeta.CopyFailAt("t"): mid.Add(-time.Minute).UTC().Format(time.RFC3339), backupmeta.CopyFailCount("t"): "2"},
		"never":   {},
	}, liveErr: map[string]error{"gone": errors.New("volume not found")}}
	rs := []resolve.Resource{vol("overdue", "t"), vol("fresh", "t"), vol("failing", "t"), vol("never", "t"), vol("gone", "t"), target("t", "")}
	items, err := FromStack(rs)
	if err != nil {
		t.Fatal(err)
	}
	due, problems := Due(s, items, mid)
	if want := []DueCopy{{"overdue", "t"}, {"never", "t"}}; !reflect.DeepEqual(due, want) {
		t.Errorf("due = %v, want %v (a copy backing off, one not yet due and an unreadable volume are not due)", due, want)
	}
	if len(problems) != 1 || problems["gone"] == nil {
		t.Errorf("a volume that cannot be read is a problem, not silence: %v", problems)
	}
	if len(s.copied) != 0 {
		t.Errorf("Due must never copy: %v", s.copied)
	}
}

func TestAssessReportsWhatIsDueWhatIsFailingAndWhatCouldNotBeDecided(t *testing.T) {
	mid := now.Add(30 * time.Minute)
	stale := mid.Add(-3 * time.Hour).UTC().Format(time.RFC3339)
	failedAt := mid.Add(-time.Minute).UTC().Format(time.RFC3339)
	s := &stub{live: map[string]map[string]string{
		"fresh":   {backupmeta.CopyStampAt("t"): mid.Add(-10 * time.Minute).UTC().Format(time.RFC3339)},
		"failing": {backupmeta.CopyStampAt("t"): stale, backupmeta.CopyFailAt("t"): failedAt, backupmeta.CopyFailCount("t"): "3"},
		"never":   {},
	}, liveErr: map[string]error{"gone": errors.New("volume not found")}}
	items, err := FromStack([]resolve.Resource{vol("fresh", "t"), vol("failing", "t"), vol("never", "t"), vol("gone", "t"), target("t", "")})
	if err != nil {
		t.Fatal(err)
	}
	a := Assess(s, items, mid)

	if want := []DueCopy{{"never", "t"}}; !reflect.DeepEqual(a.Due, want) {
		t.Errorf("due = %v, want %v (the failing one is backing off, the fresh one is not due, the unreadable one cannot be decided)", a.Due, want)
	}
	if len(a.Failing) != 1 || a.Failing[0].Volume != "failing" || a.Failing[0].Target != "t" || a.Failing[0].Count != 3 || a.Failing[0].Since.IsZero() {
		t.Errorf("failing = %+v: a copy that failed since its last success, with how many times and since when", a.Failing)
	}
	if len(a.Problems) != 1 || a.Problems["gone"] == nil {
		t.Errorf("a volume that cannot be read is a problem, not silence: %v", a.Problems)
	}
	if len(s.copied) != 0 {
		t.Errorf("looking never copies: %v", s.copied)
	}
}

func TestAFailureThatALaterSuccessSupersededIsNotFailing(t *testing.T) {
	// the stamp of the last success is newer than the last failure: the copy is healthy again
	s := &stub{live: map[string]map[string]string{"v": {
		backupmeta.CopyFailAt("t"): now.Add(-2 * time.Hour).UTC().Format(time.RFC3339), backupmeta.CopyFailCount("t"): "2",
		backupmeta.CopyStampAt("t"): now.Add(-time.Hour).UTC().Format(time.RFC3339)}}}
	items, _ := FromStack([]resolve.Resource{vol("v", "t"), target("t", "")})
	if a := Assess(s, items, now); len(a.Failing) != 0 {
		t.Errorf("%+v", a.Failing)
	}
}

func TestTargetFromCarriesWhatTheStackDeclaredAboutTheRemoteNormalised(t *testing.T) {
	const fp = "0f3a9c2d7b6e41805a9e3c7d2f1b8a4960d5e7c3b2a19f8e7d6c5b4a39281706"
	declared := resolve.Resource{Kind: resolve.KindBackupTarget, Name: "offsite", Remote: "vps", Pool: "backups", Address: "vps.example.com", Fingerprint: strings.ToUpper(fp)}
	want := volbackup.Target{Name: "offsite", Remote: "vps", Pool: "backups", Address: "https://vps.example.com:8443", Fingerprint: fp}
	if got := TargetFrom(declared); got != want {
		t.Errorf("TargetFrom = %+v, want the https URL with the default port and lower-case hex: %+v", got, want)
	}
	// the default is a bare remote name: nothing is carried that the stack did not write down
	plain := declared
	plain.Address, plain.Fingerprint = "", ""
	if got := TargetFrom(plain); got.Address != "" || got.Fingerprint != "" || got.Remote != "vps" {
		t.Errorf("a target that declared no address must carry none: %+v", got)
	}
}

// A schedule that cannot be read fails that copy, says so, and does not stop the others.
func TestDueWithAnUnreadableScheduleFailsThatCopyOnly(t *testing.T) {
	bad := vol("a", "t")
	bad.Backup.Copies[0].Schedule = "whenever"
	// "a" has been copied before: a copy that never ran is due without its schedule being read at all
	s := &stub{live: map[string]map[string]string{"a": {backupmeta.CopyStampAt("t"): now.Add(-3 * time.Hour).UTC().Format(time.RFC3339)}, "b": {}}}
	rep, out, err := run(t, s, []resolve.Resource{bad, vol("b", "t"), target("t", "")}, Options{Due: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a -> t: ") || !strings.Contains(out, "whenever") {
		t.Errorf("the unreadable schedule must be said:\n%s", out)
	}
	if !reflect.DeepEqual(s.copied, []string{"b->t"}) {
		t.Errorf("only the readable one runs: %v", s.copied)
	}
	if rep.Failed != 1 || rep.Tried != 1 || len(rep.Copies) != 2 || rep.Copies[0].Outcome != Failed || rep.Copies[0].Target != "t" || rep.Copies[1].Outcome != Copied {
		t.Errorf("report: %+v", rep)
	}
}

// With no clock given, Run uses the real one, for deciding what is due and for the copies it starts.
func TestRunWithoutAClockUsesTheRealOne(t *testing.T) {
	items, err := FromStack([]resolve.Resource{vol("a", "t"), target("t", "")})
	if err != nil {
		t.Fatal(err)
	}
	s := &stub{live: map[string]map[string]string{"a": {}}}
	var out bytes.Buffer
	rep, err := Run(context.Background(), s, items, Options{Due: true}, &out)
	if err != nil || rep.Tried != 1 || !reflect.DeepEqual(s.copied, []string{"a->t"}) {
		t.Fatalf("a copy that never ran is due: %v %+v %v\n%s", err, rep, s.copied, out.String())
	}
	if s.nowSeen == nil || time.Since(s.nowSeen()) > time.Minute {
		t.Errorf("the copy must be handed the real clock")
	}
}

func TestTheAbandonedPartialCopiesRemovedAreReportedAfterThePruned(t *testing.T) {
	s := &stub{results: map[string]volbackup.CopyResult{
		"a->t": {Volume: "a-bk-1", Swept: []string{"a-bk-0", "a-bk-00"}},
		"b->t": {Volume: "b-bk-1", Pruned: []string{"b-bk-0"}, Swept: []string{"b-bk-00"}},
	}}
	rep, out, err := run(t, s, []resolve.Resource{vol("a", "t"), vol("b", "t"), target("t", "")}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := "copied a -> t: restore point a-bk-1 (removed 2 abandoned partial cop(ies): a-bk-0, a-bk-00)\n" +
		"copied b -> t: restore point b-bk-1 (pruned 1 older: b-bk-0) (removed 1 abandoned partial cop(ies): b-bk-00)\n"
	if out != want {
		t.Errorf("output:\n%q\nwant:\n%q", out, want)
	}
	if got := rep.Copies[1]; !reflect.DeepEqual(got.Swept, []string{"b-bk-00"}) || !reflect.DeepEqual(got.Pruned, []string{"b-bk-0"}) {
		t.Errorf("the report carries both: %+v", got)
	}
}

// What the engine is handed is what the person asked for: a dry run MUST reach it (or "dry run" would make real copies), and so must the
// copy's own retention, the run's clock, and somewhere to report progress.
func TestTheEngineIsHandedWhatTheCopyAndTheRunSay(t *testing.T) {
	for _, dry := range []bool{false, true} {
		a, b := vol("a", "t"), vol("b", "t")
		b.Backup.Copies[0].Retain = "7d"
		s := &stub{}
		if _, _, err := run(t, s, []resolve.Resource{a, b, target("t", "")}, Options{DryRun: dry}); err != nil {
			t.Fatal(err)
		}
		if len(s.handed) != 2 {
			t.Fatalf("dry=%v: copies = %d", dry, len(s.handed))
		}
		for i, wantRetain := range []string{"30d", "7d"} {
			got := s.handed[i]
			if got.DryRun != dry || got.Retain != wantRetain || got.Progress == nil || got.Now == nil || !got.Now().Equal(now) {
				t.Errorf("dry=%v copy %d was handed %+v, want DryRun=%v Retain=%s, a progress writer and the run's clock", dry, i, got, dry, wantRetain)
			}
		}
	}
}

func TestAStoppedCopyIsRecordedWithItsReason(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	items, _ := FromStack([]resolve.Resource{vol("a", "t"), target("t", "")})
	var out bytes.Buffer
	rep, _ := Run(ctx, &stub{}, items, Options{Now: func() time.Time { return now }}, &out)
	if len(rep.Copies) != 1 || rep.Copies[0].Outcome != Skipped || rep.Copies[0].Target != "t" || rep.Copies[0].Detail != "stopped: context canceled" {
		t.Errorf("report: %+v", rep.Copies)
	}
	if !strings.Contains(out.String(), "a -> t: stopped (context canceled)") {
		t.Errorf("output: %q", out.String())
	}
}

// A volume that cannot be read at all is one failure with no target, since no copy of it was ever considered.
func TestAVolumeThatCannotBeReadIsRecordedWithoutATarget(t *testing.T) {
	s := &stub{liveErr: map[string]error{"a": errors.New("no such volume")}}
	rep, _, _ := run(t, s, []resolve.Resource{vol("a", "t", "u"), target("t", ""), target("u", "")}, Options{})
	if len(rep.Copies) != 1 || rep.Copies[0].Volume != "a" || rep.Copies[0].Target != "" || rep.Copies[0].Outcome != Failed || rep.Copies[0].Detail != "no such volume" {
		t.Errorf("report: %+v", rep.Copies)
	}
	if len(s.copied) != 0 {
		t.Errorf("none of its copies is tried: %v", s.copied)
	}
}
