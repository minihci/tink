package backupmeta

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

var (
	nasTarget    = PolicyTarget{Name: "nas", Location: "other-host", Engine: "incus", Pool: "nas"}
	vpsTarget    = PolicyTarget{Name: "vps", Location: "offsite", Engine: "incus", Remote: "vps", Pool: "backups"}
	unusedTarget = PolicyTarget{Name: "unused", Location: "same-host", Engine: "incus", Pool: "x"}
)

// escapedAmpersand is what encoding/json writes for an & unless HTML escaping is switched off. It is built in two pieces so that this
// file never contains a backslash followed by a u, which some tooling reads as an escape and turns into the character itself.
const escapedAmpersand = `\` + "u0026"

func pc(t PolicyTarget, schedule, retain string) PolicyCopy {
	return PolicyCopy{Target: t, Schedule: schedule, Retain: retain}
}

func policyOf(cs ...PolicyCopy) BackupPolicy { return BackupPolicy{Proto: PolicyProto, Copies: cs} }

// fullPolicy has everything a policy can say: two copies (one to a remote), and a verification with a check.
func fullPolicy() BackupPolicy {
	p := policyOf(pc(nasTarget, "0 4 * * *", "30d"), pc(vpsTarget, "@daily", "7d"))
	p.Verify = &PolicyVerify{Every: "weekly", Check: &PolicyCheck{Image: "docker-oci:library/alpine:3", Command: []string{"sh", "-c", "test -s /data/x && true"}}}
	return p
}

func encoded(t *testing.T, p BackupPolicy) string {
	t.Helper()
	s, err := EncodePolicy(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEncodePolicyIsOneLineAndReadsBack(t *testing.T) {
	p := fullPolicy()
	text := encoded(t, p)
	got, err := ParsePolicy(text)
	if err != nil {
		t.Fatalf("what EncodePolicy wrote must parse: %v\n%s", err, text)
	}
	if !reflect.DeepEqual(got, p) {
		t.Errorf("round trip changed the policy:\n got  %+v\n want %+v", got, p)
	}
	if strings.Contains(text, "\n") {
		t.Error("the policy is one line")
	}
	if strings.Contains(text, escapedAmpersand) || !strings.Contains(text, "&&") {
		t.Errorf("a check's && should read as && in the stored text: %s", text)
	}
	if again := encoded(t, fullPolicy()); again != text {
		t.Errorf("the same policy must encode to the same text, or plan would see drift that is not there:\n%s\n%s", text, again)
	}
}

func TestParsePolicyRefusesWhatItCannotUnderstand(t *testing.T) {
	good := encoded(t, fullPolicy())
	with := func(mut func(map[string]any)) string {
		var d map[string]any
		_ = json.Unmarshal([]byte(good), &d)
		mut(d)
		b, _ := json.Marshal(d)
		return string(b)
	}
	copiesOf := func(d map[string]any) []any { return d["copies"].([]any) }
	tests := map[string]struct {
		in   string
		want string
	}{
		"not json":              {"nope", "not a policy tink wrote"},
		"another protocol":      {with(func(d map[string]any) { d["proto"] = 2 }), "protocol 2"},
		"no protocol":           {with(func(d map[string]any) { delete(d, "proto") }), "protocol 0"},
		"an unknown field":      {with(func(d map[string]any) { d["colour"] = "red" }), "not a policy tink wrote"},
		"a copy with no target": {with(func(d map[string]any) { copiesOf(d)[0].(map[string]any)["target"] = map[string]any{} }), "no target name"},
		"a target with nowhere to go": {with(func(d map[string]any) {
			copiesOf(d)[0].(map[string]any)["target"] = map[string]any{"name": "nas"}
		}), "neither a remote nor a pool"},
		"a target named twice": {with(func(d map[string]any) { d["copies"] = append(copiesOf(d), copiesOf(d)[0]) }), "twice"},
		"a bad schedule":       {with(func(d map[string]any) { copiesOf(d)[0].(map[string]any)["schedule"] = "whenever" }), "schedule"},
		"a bad retain":         {with(func(d map[string]any) { copiesOf(d)[0].(map[string]any)["retain"] = "0d" }), "retain"},
		"a bad cadence":        {with(func(d map[string]any) { d["verify"].(map[string]any)["every"] = "hourly" }), "daily, weekly or monthly"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePolicy(tc.in); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	if _, err := ParsePolicy(good); err != nil {
		t.Errorf("the good one must parse: %v", err)
	}
}

func TestANewPolicyIsDescribedCopyByCopy(t *testing.T) {
	p := policyOf(pc(nasTarget, "0 4 * * *", "30d"), pc(vpsTarget, "@daily", "7d"))
	p.Verify = &PolicyVerify{Every: "weekly", Check: &PolicyCheck{Image: "alpine:3", Command: []string{"sh", "-c", "test -s /data/x"}, Mount: "/data"}}
	got := DescribePolicyChange("", encoded(t, p))
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
	before := encoded(t, policyOf(pc(nasTarget, "@daily", "30d"), pc(vpsTarget, "@weekly", "8w")))
	after := encoded(t, policyOf(pc(nasTarget, "@hourly", "60d"), pc(unusedTarget, "@daily", "3d")))
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
	moved := nasTarget
	moved.Pool = "nas2"
	a := encoded(t, policyOf(pc(nasTarget, "@daily", "30d")))
	b := encoded(t, policyOf(pc(moved, "@daily", "30d")))
	got := strings.Join(DescribePolicyChange(a, b), "\n")
	if !strings.Contains(got, "now at pool nas2 (was pool nas)") || strings.Contains(got, "schedule") {
		t.Errorf("%s", got)
	}
}

func TestVerifyChangesAreDescribed(t *testing.T) {
	base := policyOf(pc(nasTarget, "@daily", "30d"))
	with := func(every string) string {
		p := base
		p.Verify = &PolicyVerify{Every: every}
		return encoded(t, p)
	}
	if got := DescribePolicyChange(encoded(t, base), with("weekly")); len(got) != 1 || !strings.HasSuffix(got[0], "+ verify weekly") {
		t.Errorf("%v", got)
	}
	if got := DescribePolicyChange(with("weekly"), encoded(t, base)); len(got) != 1 || !strings.HasSuffix(got[0], "- verify weekly") {
		t.Errorf("%v", got)
	}
	if got := DescribePolicyChange(with("weekly"), with("daily")); len(got) != 1 || !strings.HasSuffix(got[0], "~ verify weekly -> verify daily") {
		t.Errorf("%v", got)
	}
}

func TestRemovingAPolicySaysWhatStops(t *testing.T) {
	got := DescribePolicyChange(encoded(t, fullPolicy()), "")
	if len(got) != 1 || !strings.Contains(got[0], "removed") || !strings.Contains(got[0], "stops being copied and verified") {
		t.Errorf("%v", got)
	}
	// a policy that cannot be read says less, and does not fail
	if got := DescribePolicyChange("not json", ""); len(got) != 1 || !strings.Contains(got[0], "removed") || strings.Contains(got[0], "stops") {
		t.Errorf("%v", got)
	}
}

func TestAPolicyTheVolumeCarriesButNobodyCanReadIsSaidToBeReplaced(t *testing.T) {
	got := DescribePolicyChange(`{"proto":9}`, encoded(t, fullPolicy()))
	if len(got) < 2 || !strings.Contains(got[0], "replaced") || !strings.Contains(got[0], "cannot be read") || !strings.Contains(got[1], "+ copy to nas") {
		t.Errorf("%v", got)
	}
}

func TestNoChangeIsNoLines(t *testing.T) {
	p := encoded(t, fullPolicy())
	if got := DescribePolicyChange(p, p); len(got) != 0 {
		t.Errorf("%v", got)
	}
	if got := DescribePolicyChange("", ""); len(got) != 0 {
		t.Errorf("%v", got)
	}
}
