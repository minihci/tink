package resolve

import (
	"strings"
	"testing"

	"github.com/minihci/tink/internal/backupmeta"
)

func policyText(t *testing.T, r Resource) string {
	t.Helper()
	s, err := BuildPolicy(r, policyTargets())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func copies(cs ...backupmeta.BackupCopy) Resource {
	return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &backupmeta.VolumeBackup{Copies: cs}}
}

func TestANewPolicyIsDescribedCopyByCopy(t *testing.T) {
	r := volWithCopies()
	r.Backup.Verify = "weekly"
	r.Backup.VerifyCheck = &backupmeta.VerifyCheck{Image: "alpine:3", Command: []string{"sh", "-c", "test -s /data/x"}, Mount: "/data"}
	got := DescribePolicyChange("", policyText(t, r))
	want := []string{
		`backup policy: + copy to nas (pool nas): schedule "0 4 * * *", keep 30d`,
		`backup policy: + copy to vps (remote vps, pool backups): schedule "@daily", keep 7d`,
		`backup policy: + verify weekly, check in alpine:3: sh -c test -s /data/x (volume mounted at /data)`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, l := range got {
		if strings.Contains(l, `{"`) || strings.Contains(l, `\"`) {
			t.Errorf("the JSON is not what a reader should see: %s", l)
		}
	}
}

func TestAChangedPolicyNamesOnlyWhatDiffers(t *testing.T) {
	before := policyText(t, copies(backupmeta.BackupCopy{Target: "nas", Schedule: "@daily", Retain: "30d"}, backupmeta.BackupCopy{Target: "vps", Schedule: "@weekly", Retain: "8w"}))
	after := policyText(t, copies(backupmeta.BackupCopy{Target: "nas", Schedule: "@hourly", Retain: "60d"}, backupmeta.BackupCopy{Target: "unused", Schedule: "@daily", Retain: "3d"}))
	got := strings.Join(DescribePolicyChange(before, after), "\n")
	for _, want := range []string{
		`~ copy to nas: schedule "@daily" -> "@hourly", keep 30d -> 60d`,
		`+ copy to unused (pool x): schedule "@daily", keep 3d`,
		`- copy to vps (remote vps, pool backups): schedule "@weekly", keep 8w`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing from:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("one line per difference, and none for what did not change:\n%s", got)
	}
}

func TestMovingACopyToAnotherPlaceSaysWhereFromAndTo(t *testing.T) {
	a := policyText(t, copies(backupmeta.BackupCopy{Target: "nas", Schedule: "@daily", Retain: "30d"}))
	t2 := policyTargets()
	t2["nas"] = Resource{Kind: KindBackupTarget, Name: "nas", Location: LocationOtherHost, Engine: EngineIncus, Pool: "nas2"}
	b, err := BuildPolicy(copies(backupmeta.BackupCopy{Target: "nas", Schedule: "@daily", Retain: "30d"}), t2)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(DescribePolicyChange(a, b), "\n")
	if !strings.Contains(got, "now at pool nas2 (was pool nas)") || strings.Contains(got, "schedule") {
		t.Errorf("%s", got)
	}
}

func TestVerifyChangesAreDescribed(t *testing.T) {
	base := copies(backupmeta.BackupCopy{Target: "nas", Schedule: "@daily", Retain: "30d"})
	with := func(every string) string {
		r := base
		r.Backup = &backupmeta.VolumeBackup{Copies: base.Backup.Copies, Verify: every}
		return policyText(t, r)
	}
	if got := DescribePolicyChange(policyText(t, base), with("weekly")); len(got) != 1 || !strings.HasSuffix(got[0], "+ verify weekly") {
		t.Errorf("%v", got)
	}
	if got := DescribePolicyChange(with("weekly"), policyText(t, base)); len(got) != 1 || !strings.HasSuffix(got[0], "- verify weekly") {
		t.Errorf("%v", got)
	}
	if got := DescribePolicyChange(with("weekly"), with("daily")); len(got) != 1 || !strings.HasSuffix(got[0], "~ verify weekly -> verify daily") {
		t.Errorf("%v", got)
	}
}

func TestRemovingAPolicySaysWhatStops(t *testing.T) {
	r := volWithCopies()
	r.Backup.Verify = "weekly"
	got := DescribePolicyChange(policyText(t, r), "")
	if len(got) != 1 || !strings.Contains(got[0], "removed") || !strings.Contains(got[0], "stops being copied and verified") {
		t.Errorf("%v", got)
	}
	// a policy that cannot be read says less, and does not fail
	if got := DescribePolicyChange("not json", ""); len(got) != 1 || !strings.Contains(got[0], "removed") || strings.Contains(got[0], "stops") {
		t.Errorf("%v", got)
	}
}

func TestAPolicyTheVolumeCarriesButNobodyCanReadIsSaidToBeReplaced(t *testing.T) {
	got := DescribePolicyChange(`{"proto":9}`, policyText(t, volWithCopies()))
	if len(got) < 2 || !strings.Contains(got[0], "replaced") || !strings.Contains(got[0], "cannot be read") || !strings.Contains(got[1], "+ copy to nas") {
		t.Errorf("%v", got)
	}
}

func TestNoChangeIsNoLines(t *testing.T) {
	p := policyText(t, volWithCopies())
	if got := DescribePolicyChange(p, p); len(got) != 0 {
		t.Errorf("%v", got)
	}
	if got := DescribePolicyChange("", ""); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestAPlanOfAVolumeDoesNotShowTheRawPolicy(t *testing.T) {
	p := planVolume(t, nil, volumeEnv{})
	joined := strings.Join(p.Changes, "\n")
	if strings.Contains(joined, backupmeta.PolicyKey) || strings.Contains(joined, `\"`) {
		t.Errorf("the key and its escaped JSON are not for a reader:\n%s", joined)
	}
	if !strings.Contains(joined, "backup policy: + copy to nas") {
		t.Errorf("%s", joined)
	}
}
