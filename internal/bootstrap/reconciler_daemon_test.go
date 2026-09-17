package bootstrap

import "testing"

func TestWithoutLegacyReconcilerLine_RemovesOnlyTheReconcilerLine(t *testing.T) {
	current := "0 3 * * * /root/some-other-job.sh\n" +
		"* * * * * /root/incus-host/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1\n"

	got, found := withoutLegacyReconcilerLine(current)

	if !found {
		t.Fatal("expected the legacy reconciler line to be found")
	}
	want := "0 3 * * * /root/some-other-job.sh\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWithoutLegacyReconcilerLine_EmptyResultWhenItWasTheOnlyEntry(t *testing.T) {
	current := "* * * * * /root/incus-host/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1\n"

	got, found := withoutLegacyReconcilerLine(current)

	if !found {
		t.Fatal("expected the legacy reconciler line to be found")
	}
	if got != "" {
		t.Fatalf("expected an empty crontab, got %q", got)
	}
}

func TestWithoutLegacyReconcilerLine_NotFoundLeavesCrontabUntouched(t *testing.T) {
	current := "0 3 * * * /root/some-other-job.sh\n"

	got, found := withoutLegacyReconcilerLine(current)

	if found {
		t.Fatal("expected no legacy reconciler line to be found")
	}
	if got != current {
		t.Fatalf("got %q, want unchanged %q", got, current)
	}
}
