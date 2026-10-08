package backuprun

import (
	"errors"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/volbackup"
)

func item(project, pool, name string, copies ...Copy) Item {
	v := volbackup.Volume{Project: project, Pool: pool, Name: name}
	return Item{Volume: v, Label: Label(v), Copies: copies}
}

func cp(target, pool, remote, schedule, retain string) Copy {
	return Copy{Target: volbackup.Target{Name: target, Pool: pool, Remote: remote}, Schedule: schedule, Retain: retain}
}

func TestUnappliedIsEmptyWhenTheVolumesCarryWhatTheStackDeclares(t *testing.T) {
	declared := []Item{item("", "", "lib", cp("nas", "nas2510", "", "0 3 * * *", "7"), cp("off", "", "vps", "0 4 * * *", "3"))}
	applied := []Item{item("default", "default", "lib", cp("off", "", "vps", "0 4 * * *", "3"), cp("nas", "nas2510", "", "0 3 * * *", "7"))}
	if got := Unapplied(declared, applied, nil); len(got) != 0 {
		t.Errorf("spelling out the defaults and reordering the copies is not a difference: %v", got)
	}
	// what a stack adds to find a remote is not part of what runs
	declared[0].Copies[1].Target.Address = "https://10.0.0.7:8443"
	declared[0].Copies[1].Target.Fingerprint = "ab"
	if got := Unapplied(declared, applied, nil); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestUnappliedSaysHowTheyDiffer(t *testing.T) {
	declared := []Item{
		item("", "", "none", cp("nas", "p", "", "0 3 * * *", "7")),
		item("", "", "bad", cp("nas", "p", "", "0 3 * * *", "7")),
		item("", "", "changed", cp("nas", "p", "", "0 3 * * *", "7")),
		item("x", "", "twice", cp("nas", "p", "", "0 3 * * *", "7")),
	}
	applied := []Item{
		item("", "", "changed", cp("nas", "p", "", "0 5 * * *", "7")),
		item("x", "default", "twice", cp("nas", "p", "", "0 3 * * *", "7")),
		item("x", "other", "twice", cp("nas", "p", "", "0 3 * * *", "7")),
	}
	got := strings.Join(Unapplied(declared, applied, map[string]error{"bad": errors.New("unknown field")}), "\n")
	for _, want := range []string{
		"none: the volume carries no copy policy",
		"bad: its copy policy cannot be read: unknown field",
		"changed: the copies on the volume are not the stack's",
		"x/twice: more than one volume of that name carries a copy policy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}
