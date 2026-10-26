package main

import (
	"errors"
	"strings"
	"testing"

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
