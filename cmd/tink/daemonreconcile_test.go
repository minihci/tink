package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/observe"
)

func TestASiteTheDaemonCannotReachIsFailingWithAFixedReasonAndTheCauseOnlyForTheLog(t *testing.T) {
	got := unreachable("derived", "home", errors.New("secret-looking detail"))
	if got.What != "derived" || got.Site != "home" || got.State != observe.Failing || got.Reason != "the server could not be reached" {
		t.Errorf("result = %+v", got)
	}
	if got.Err == nil || strings.Contains(got.State+got.Reason, "secret-looking") {
		t.Errorf("result = %+v: the cause is for the log and must not be in the state or the reason", got)
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
