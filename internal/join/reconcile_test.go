package join

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/observe"
	"github.com/minihci/tink/internal/resolve"
)

func reconcile(t *testing.T, f *serverFake, files *fakeFiles) observe.Result {
	t.Helper()
	return ReconcileDNS(f, files, "", ApplyOptions{})
}

func TestReconcilingWritesTheRecordsOnceAndThenWritesNothing(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()

	first := reconcile(t, f, files)
	if first.State != observe.Converged || !first.Wrote {
		t.Fatalf("first reconcile = %+v, want converged and written", first)
	}
	if got := files.files[fkey("tink", "dns", DNSRecordsPath)]; got != wantFile {
		t.Errorf("the file holds %q, want %q", got, wantFile)
	}

	files.writes = nil
	second := reconcile(t, f, files)
	if second.State != observe.Converged || second.Wrote {
		t.Errorf("second reconcile = %+v, want converged and nothing written", second)
	}
	if len(files.writes) != 0 {
		t.Errorf("a reconcile that found no difference wrote %v", files.writes)
	}
}

func TestAServerWhoseStampsChangeIsReconciledWithTheNewRecords(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()
	reconcile(t, f, files)

	delete(f.whoami(t).Config, "user.tink.intent.publish.whoami-admin")
	out := reconcile(t, f, files)
	if out.State != observe.Converged || !out.Wrote {
		t.Fatalf("reconcile after a stamp was dropped = %+v, want converged and written", out)
	}
	if got, want := files.files[fkey("tink", "dns", DNSRecordsPath)], generatedNote+"192.0.2.231 whoami.home.example.org\n"; got != want {
		t.Errorf("the file holds %q, want %q", got, want)
	}
}

func TestASiteWithNoDNSHelperIsSkippedAndNothingIsRead(t *testing.T) {
	stacks := load(t)
	for _, st := range stacks {
		for _, r := range st.Resources {
			if r.Kind == resolve.KindInstance && r.Config[resolve.RoleKey] == "dns" {
				delete(r.Config, resolve.RoleKey)
			}
		}
	}
	files := platformFiles()
	out := reconcile(t, serverOf(t, stacks), files)
	if out.State != observe.Skipped {
		t.Fatalf("reconcile = %+v, want skipped", out)
	}
	if len(files.reads) != 0 || len(files.writes) != 0 {
		t.Errorf("a skipped site touched the helper: reads %v writes %v", files.reads, files.writes)
	}
}

func TestAFileTinkDidNotWriteIsBlockedAndLeftAlone(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()
	files.files[fkey("tink", "dns", DNSRecordsPath)] = "198.51.100.9 by-hand.home.example.org\n"

	out := reconcile(t, f, files)
	if out.State != observe.Blocked {
		t.Fatalf("reconcile = %+v, want blocked", out)
	}
	if len(files.writes) != 0 {
		t.Errorf("the reconciler adopted a file it did not write: %v", files.writes)
	}
	var refusal *Refusal
	if !errors.As(out.Err, &refusal) {
		t.Errorf("the error %v is not a refusal", out.Err)
	}
}

func TestAServerWithNoPublishedNameIsBlockedAndTheRecordsAreNotEmptied(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()
	reconcile(t, f, files)

	for k := range f.whoami(t).Config {
		if len(k) > len(resolve.PublishStampPrefix) && k[:len(resolve.PublishStampPrefix)] == resolve.PublishStampPrefix {
			delete(f.whoami(t).Config, k)
		}
	}
	files.writes = nil
	out := reconcile(t, f, files)
	if out.State != observe.Blocked {
		t.Fatalf("reconcile = %+v, want blocked", out)
	}
	if len(files.writes) != 0 {
		t.Errorf("the generated records were emptied: %v", files.writes)
	}
}

func TestAWriteThatFailsIsFailingNotBlocked(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()
	files.writeErr = errors.New("connection reset")

	out := reconcile(t, f, files)
	if out.State != observe.Failing || out.Reason == "" {
		t.Errorf("reconcile = %+v, want failing with a reason", out)
	}
	var refusal *Refusal
	if errors.As(out.Err, &refusal) {
		t.Errorf("a failed write was reported as a refusal: %v", out.Err)
	}
}

func TestAStampThatCannotBeReadIsBlockedWithoutWriting(t *testing.T) {
	f := serverOf(t, load(t))
	f.whoami(t).Config["user.tink.intent.publish.whoami"] = "not json"
	files := platformFiles()

	out := reconcile(t, f, files)
	if out.State != observe.Blocked || out.Reason != "a publish stamp cannot be read" {
		t.Fatalf("reconcile = %+v, want blocked on the stamp", out)
	}
	if len(out.Detail) == 0 {
		t.Error("no finding names the stamp")
	}
	if len(files.writes) != 0 {
		t.Errorf("wrote %v", files.writes)
	}
}

// Both reconcilers say what they found in the one observe.Result and in four state words; "changed" and "no changes" are gone, and a rewrite is Wrote.
func TestBothReconcilersReturnTheOneResultInTheFourStateWords(t *testing.T) {
	four := map[string]bool{observe.Converged: true, observe.Blocked: true, observe.Skipped: true, observe.Failing: true}
	var seen []observe.Result

	// dns: written, then unchanged, then a stamp that cannot be read, then a failing write
	f := serverOf(t, load(t))
	files := platformFiles()
	seen = append(seen, reconcile(t, f, files), reconcile(t, f, files))
	files.files[fkey("tink", "dns", DNSRecordsPath)] = "198.51.100.9 by-hand.home.example.org\n"
	seen = append(seen, reconcile(t, f, files))
	files = platformFiles()
	files.writeErr = errors.New("connection reset")
	seen = append(seen, reconcile(t, f, files))

	// derived: written, then unchanged, then skipped because nothing is stamped
	df := serverOf(t, withDerived(t, routesFile(false)))
	dfiles, rs := platformFiles(), &fakeRestarter{}
	dfiles.files[fkey("tink", "ingress", routesPath)] = emptyShell
	seen = append(seen, reconcileDerived(df, dfiles, rs)...)
	seen = append(seen, reconcileDerived(df, dfiles, rs)...)
	seen = append(seen, reconcileDerived(serverOf(t, load(t)), platformFiles(), rs)...)

	var wrote, converged int
	for _, r := range seen {
		if !four[r.State] {
			t.Errorf("%+v: %q is not one of the four state words", r, r.State)
		}
		if r.What != "dns" && r.What != "derived" {
			t.Errorf("%+v has no what", r)
		}
		if r.Wrote {
			wrote++
		}
		if r.State == observe.Converged {
			converged++
		}
	}
	if wrote != 2 || converged < 4 {
		t.Errorf("wrote %d, converged %d of %+v: a rewrite and a pass that found nothing are both converged, and only the first wrote", wrote, converged, seen)
	}
}

func TestTheOldStateWordsAreNotInTheCode(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, old := range []string{`"no changes"`, `= "changed"`, `State: "changed"`, "DNSOutcome", "DerivedOutcome"} {
			if strings.Contains(string(b), old) {
				t.Errorf("%s still has %s", name, old)
			}
		}
	}
}

// The state and the reason are the words the status document carries; the cause of a refusal or a failure is for the log, and a result that has one never
// puts it in either.
func TestTheCauseIsForTheLogAndNeverInTheStateOrTheReason(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()
	files.writeErr = errors.New("secret-looking detail")
	out := reconcile(t, f, files)
	if out.State != observe.Failing || out.Err == nil || !strings.Contains(out.Err.Error(), "secret-looking detail") {
		t.Fatalf("reconcile = %+v, want failing with the cause in Err", out)
	}
	if strings.Contains(out.State+out.Reason, "secret-looking") {
		t.Errorf("the cause reached the state or the reason: %+v", out)
	}
	for _, d := range out.Detail {
		if strings.Contains(d.Reason, "secret-looking") {
			t.Errorf("the cause is in the reason of a finding: %+v", d)
		}
	}
}
