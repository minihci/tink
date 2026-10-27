package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/join"
)

func TestAReconcileOutcomeBecomesTheDaemonsResultWithTheDetailOnlyForTheLog(t *testing.T) {
	got := resultOf(join.DNSOutcome{
		Site: "home", State: join.DNSBlocked, Reason: "the apply was refused",
		Findings: []string{"error: instance/dns: something"}, Err: errors.New("secret-looking detail"),
	})
	if got.What != "dns" || got.Site != "home" || got.State != "blocked" || got.Reason != "the apply was refused" {
		t.Errorf("result = %+v", got)
	}
	if len(got.Detail) != 2 || !strings.Contains(got.Detail[1], "secret-looking detail") {
		t.Errorf("detail = %v, want the findings then the error", got.Detail)
	}
	if strings.Contains(got.State+got.Reason, "secret-looking") {
		t.Error("error text reached the state or the reason, which the status document carries")
	}
}

func TestARewriteAndAnUnchangedPassAreBothConvergedAndOnlyTheRewriteIsSaidInTheLog(t *testing.T) {
	rewrote := resultOf(join.DNSOutcome{State: join.DNSChanged, Report: join.ApplyReport{Instance: "dns", Path: "/hosts", Added: []string{"a"}, Removed: []string{"b", "c"}}})
	if rewrote.State != "converged" || !rewrote.Wrote || len(rewrote.Detail) != 1 || !strings.Contains(rewrote.Detail[0], "1 record(s) added, 2 removed") {
		t.Errorf("rewrite = %+v", rewrote)
	}
	same := resultOf(join.DNSOutcome{State: join.DNSUnchanged})
	if same.State != "converged" || same.Wrote || len(same.Detail) != 0 {
		t.Errorf("unchanged = %+v", same)
	}
}

func TestADerivedOutcomeBecomesTheDaemonsResultNamedByItsFileWithTheDetailOnlyForTheLog(t *testing.T) {
	blocked := derivedResultOf(join.DerivedOutcome{
		Site: "home", Name: "ingress-routes", State: join.DerivedBlocked, Reason: "the file is not the daemon's to replace",
		Findings: []string{"warning: something"}, Detail: []string{"/etc/caddy/apps.caddy on ingress: nothing is published"}, Err: errors.New("secret-looking detail"),
	})
	if blocked.What != "derived/ingress-routes" || blocked.Site != "home" || blocked.State != "blocked" || blocked.Reason != "the file is not the daemon's to replace" {
		t.Errorf("result = %+v", blocked)
	}
	if len(blocked.Detail) != 3 || !strings.Contains(blocked.Detail[2], "secret-looking detail") {
		t.Errorf("detail = %v, want the findings, then the detail, then the error", blocked.Detail)
	}
	if strings.Contains(blocked.State+blocked.Reason+blocked.What, "secret-looking") {
		t.Error("error text reached what the status document carries")
	}

	if site := derivedResultOf(join.DerivedOutcome{Site: "home", State: join.DerivedSkipped}); site.What != "derived" || site.State != "skipped" {
		t.Errorf("a result about the whole site = %+v", site)
	}
	rewrote := derivedResultOf(join.DerivedOutcome{Name: "x", State: join.DerivedChanged, Wrote: true})
	same := derivedResultOf(join.DerivedOutcome{Name: "x", State: join.DerivedUnchanged})
	if rewrote.State != "converged" || !rewrote.Wrote || same.State != "converged" || same.Wrote {
		t.Errorf("rewrote = %+v, same = %+v: both are converged, only the rewrite is said in the log", rewrote, same)
	}
}

func TestDaemonRunRefusesTheTwoReconcilersTogetherBeforeItDoesAnything(t *testing.T) {
	c := newDaemonRunCmd()
	c.SetArgs([]string{"--socket", "/nonexistent/tink-test.sock", "--reconcile-dns", "--reconcile-derived"})
	c.SilenceUsage, c.SilenceErrors = true, true
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.ExecuteContext(ctx); err == nil || !strings.Contains(err.Error(), "cannot run together") {
		t.Errorf("err = %v", err)
	}
	if newDaemonRunCmd().Flags().Lookup("reconcile-derived") == nil {
		t.Error("there is no --reconcile-derived")
	}
}
