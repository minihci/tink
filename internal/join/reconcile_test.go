package join

import (
	"errors"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

func reconcile(t *testing.T, f *serverFake, files *fakeFiles) DNSOutcome {
	t.Helper()
	return ReconcileDNS(f, files, "", ApplyOptions{})
}

func TestReconcilingWritesTheRecordsOnceAndThenWritesNothing(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()

	first := reconcile(t, f, files)
	if first.State != DNSChanged || !first.Report.Written {
		t.Fatalf("first reconcile = %+v, want changed and written", first)
	}
	if got := files.files[fkey("tink", "dns", DNSRecordsPath)]; got != wantFile {
		t.Errorf("the file holds %q, want %q", got, wantFile)
	}

	files.writes = nil
	second := reconcile(t, f, files)
	if second.State != DNSUnchanged {
		t.Errorf("second reconcile = %+v, want no changes", second)
	}
	if len(files.writes) != 0 {
		t.Errorf("a reconcile that found no difference wrote %v", files.writes)
	}
}

func TestAServerWhoseStampsChangeIsReconciledWithTheNewRecords(t *testing.T) {
	f := serverOf(t, load(t))
	files := platformFiles()
	reconcile(t, f, files)

	delete(f.whoami(t).Config, "user.tink.publish.whoami-admin")
	out := reconcile(t, f, files)
	if out.State != DNSChanged {
		t.Fatalf("reconcile after a stamp was dropped = %+v, want changed", out)
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
	if out.State != DNSSkipped {
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
	if out.State != DNSBlocked {
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
	if out.State != DNSBlocked {
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
	if out.State != DNSFailing || out.Reason == "" {
		t.Errorf("reconcile = %+v, want failing with a reason", out)
	}
	var refusal *Refusal
	if errors.As(out.Err, &refusal) {
		t.Errorf("a failed write was reported as a refusal: %v", out.Err)
	}
}

func TestAStampThatCannotBeReadIsBlockedWithoutWriting(t *testing.T) {
	f := serverOf(t, load(t))
	f.whoami(t).Config["user.tink.publish.whoami"] = "not json"
	files := platformFiles()

	out := reconcile(t, f, files)
	if out.State != DNSBlocked || out.Reason != "a publish stamp cannot be read" {
		t.Fatalf("reconcile = %+v, want blocked on the stamp", out)
	}
	if len(out.Findings) == 0 {
		t.Error("no finding names the stamp")
	}
	if len(files.writes) != 0 {
		t.Errorf("wrote %v", files.writes)
	}
}
