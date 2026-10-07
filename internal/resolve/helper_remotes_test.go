package resolve

import (
	"strings"
	"testing"
)

// A copy to an Incus remote is made through the remote of that name in the helper's own client configuration. One the helper does not have
// fails on every attempt, which plan can say before the first one.

func volumeCopyingToVPS() Resource {
	return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
		Copies: []BackupCopy{{Target: "nas", Schedule: "@daily", Retain: "30d"}, {Target: "vps", Schedule: "@daily", Retain: "7d"}}}}
}

func TestACopyToARemoteTheHelperDoesNotHaveIsWarnedAbout(t *testing.T) {
	env := volumeEnv{targets: policyTargets(), helperLabel: "tink-helper/helper", helperRemotes: map[string]bool{"host": true}}
	got := env.helperRemoteWarnings(volumeCopyingToVPS())
	if len(got) != 1 {
		t.Fatalf("one warning, for the one remote target: %v", got)
	}
	for _, want := range []string{`"vps"`, "will fail", "tink-helper/helper", "tink helper remote add vps"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the warning must say %q: %s", want, got[0])
		}
	}
	if strings.Contains(got[0], "nas") {
		t.Errorf("a pool target on the helper's own server needs no remote: %s", got[0])
	}
}

func TestNothingIsWarnedWhenTheHelperHasTheRemoteOrHasNotSaid(t *testing.T) {
	r := volumeCopyingToVPS()
	for name, env := range map[string]volumeEnv{
		"it has the remote":       {targets: policyTargets(), helperRemotes: map[string]bool{"vps": true}},
		"it has not said":         {targets: policyTargets(), helperRemotes: nil},
		"there is no helper":      {targets: policyTargets()},
		"a copy to the pool only": {targets: policyTargets(), helperRemotes: map[string]bool{}},
	} {
		rr := r
		if name == "a copy to the pool only" {
			rr = volumeWithCopies()
		}
		if got := env.helperRemoteWarnings(rr); len(got) != 0 {
			t.Errorf("%s: %v", name, got)
		}
	}
	// a volume that opted out of backup copies nowhere
	optedOut := Resource{Kind: KindStorageVolume, Name: "scratch", Backup: &VolumeBackup{None: "regenerable"}}
	if got := (volumeEnv{targets: policyTargets(), helperRemotes: map[string]bool{}}).helperRemoteWarnings(optedOut); len(got) != 0 {
		t.Errorf("opted out: %v", got)
	}
}

func TestAHelperThatHasNoRemotesAtAllIsNotTheSameAsOneThatHasNotSaid(t *testing.T) {
	env := volumeEnv{targets: policyTargets(), helperRemotes: map[string]bool{}}
	if got := env.helperRemoteWarnings(volumeCopyingToVPS()); len(got) != 1 {
		t.Errorf("an empty list is a statement, and the copy to the remote will fail: %v", got)
	}
}

func TestThePlanOfAVolumeCarriesTheWarningEvenWhenNothingChanges(t *testing.T) {
	// the policy is already on the volume: nothing to apply, and the copy still cannot work, so the warning stays until the remote is added
	targets := policyTargets()
	want, err := BuildPolicy(volumeCopyingToVPS(), targets)
	if err != nil {
		t.Fatal(err)
	}
	srv := &volumeServer{vol: map[string]string{PolicyKey: want}}
	p, err := planStorageVolume(srv, volumeCopyingToVPS(), volumeEnv{targets: targets, helperLabel: "tink-helper/helper", helperRemotes: map[string]bool{"host": true}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNone {
		t.Fatalf("converged: %v %v", p.Action, p.Changes)
	}
	if !strings.Contains(strings.Join(p.Warnings, "|"), `remote "vps"`) {
		t.Errorf("warnings = %v", p.Warnings)
	}
}

func TestWithHelperRemotes(t *testing.T) {
	o := (PlanOptions{}).WithHelperRemotes("h", []string{"host", "vps"})
	if !o.helperRemotes["vps"] || !o.helperRemotes["host"] || o.helperLabel != "h" {
		t.Errorf("%+v", o)
	}
	if o := (PlanOptions{}).WithHelperRemotes("h", nil); o.helperRemotes != nil {
		t.Error("a helper that has not said what it has is checked against nothing")
	}
	if o := (PlanOptions{}).WithHelperRemotes("h", []string{}); o.helperRemotes == nil {
		t.Error("an empty list says it has none")
	}
	// the label an earlier WithHelperPolicy gave stays
	if o := (PlanOptions{}).WithHelperPolicy("first", 1).WithHelperRemotes("second", []string{}); o.helperLabel != "first" {
		t.Errorf("label = %q", o.helperLabel)
	}
}
