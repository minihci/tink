package bootstrap

import "testing"

func TestBuildCrontab_AddsEntryToEmptyCrontab(t *testing.T) {
	got := buildCrontab("", "/root/incus-host")
	want := "* * * * * /root/incus-host/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildCrontab_ReplacesExistingEntryWithoutDuplicating(t *testing.T) {
	current := "0 3 * * * /root/some-other-job.sh\n" +
		"* * * * * /root/incus-host/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1\n"

	got := buildCrontab(current, "/root/incus-host")

	want := "0 3 * * * /root/some-other-job.sh\n" +
		"* * * * * /root/incus-host/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildCrontab_PreservesUnrelatedEntries(t *testing.T) {
	current := "0 3 * * * /root/some-other-job.sh\n"
	got := buildCrontab(current, "/root/incus-host")
	want := "0 3 * * * /root/some-other-job.sh\n" +
		"* * * * * /root/incus-host/reconciler/reconcile.sh >> /var/log/ingress-reconciler.log 2>&1\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
