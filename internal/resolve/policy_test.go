package resolve

import (
	"encoding/json"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func policyTargets() map[string]Resource {
	return map[string]Resource{
		"nas":    {Kind: KindBackupTarget, Name: "nas", Location: LocationOtherHost, Engine: EngineIncus, Pool: "nas"},
		"vps":    {Kind: KindBackupTarget, Name: "vps", Location: LocationOffsite, Engine: EngineIncus, Remote: "vps", Pool: "backups"},
		"unused": {Kind: KindBackupTarget, Name: "unused", Location: LocationSameHost, Engine: EngineIncus, Pool: "x"},
	}
}

func volWithCopies() Resource {
	return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
		Snapshots: &SnapshotPolicy{Schedule: "0 3 * * *", Retain: "14d"},
		Copies: []BackupCopy{
			{Target: "nas", Schedule: " 0 4 * * * ", Retain: "30d"},
			{Target: "vps", Schedule: "@daily", Retain: "7d"},
		},
		Verify:      "weekly",
		VerifyCheck: &VerifyCheck{Image: "docker-oci:library/alpine:3", Command: []string{"sh", "-c", "test -s /data/x && true"}},
	}}
}

func TestBuildPolicyResolvesTargetsInline(t *testing.T) {
	text, err := BuildPolicy(volWithCopies(), policyTargets())
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePolicy(text)
	if err != nil {
		t.Fatalf("what BuildPolicy wrote must parse: %v\n%s", err, text)
	}
	if p.Proto != PolicyProto || len(p.Copies) != 2 {
		t.Fatalf("policy = %+v", p)
	}
	nas, vps := p.Copies[0], p.Copies[1]
	if nas.Target != (PolicyTarget{Name: "nas", Location: LocationOtherHost, Engine: EngineIncus, Pool: "nas"}) || nas.Schedule != "0 4 * * *" || nas.Retain != "30d" {
		t.Errorf("nas copy = %+v (schedule is trimmed, the target resolved)", nas)
	}
	if vps.Target.Remote != "vps" || vps.Target.Pool != "backups" {
		t.Errorf("vps copy = %+v", vps)
	}
	if p.Verify == nil || p.Verify.Every != "weekly" || p.Verify.Check == nil || p.Verify.Check.Image == "" {
		t.Errorf("verify = %+v", p.Verify)
	}
	if strings.Contains(text, "unused") {
		t.Error("a target no copy names must not be in the policy")
	}
	if strings.Contains(text, "\\u0026") || !strings.Contains(text, "&&") {
		t.Errorf("a check's && should read as && in the stored text: %s", text)
	}
	if strings.Contains(text, "\n") {
		t.Error("the policy is one line")
	}
}

func TestBuildPolicyIsDeterministic(t *testing.T) {
	a, _ := BuildPolicy(volWithCopies(), policyTargets())
	b, _ := BuildPolicy(volWithCopies(), policyTargets())
	if a != b || a == "" {
		t.Errorf("the same declaration must build the same text, or plan would see drift that is not there:\n%s\n%s", a, b)
	}
}

func TestBuildPolicyIsEmptyWhenThereIsNothingToCopyOrVerify(t *testing.T) {
	for name, r := range map[string]Resource{
		"no block":       {Kind: KindStorageVolume, Name: "v"},
		"opt-out":        {Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "regenerable"}},
		"snapshots only": {Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}}},
	} {
		if got, err := BuildPolicy(r, policyTargets()); got != "" || err != nil {
			t.Errorf("%s: policy = %q, err = %v; want none (snapshots live on Incus's own keys)", name, got, err)
		}
	}
	// a verify cadence on a volume with snapshots only is still worth recording
	r := Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}, Verify: "daily"}}
	if got, _ := BuildPolicy(r, nil); !strings.Contains(got, `"verify"`) || strings.Contains(got, `"copies"`) {
		t.Errorf("verify-only policy = %q", got)
	}
}

func TestBuildPolicyRejectsAnUnknownTarget(t *testing.T) {
	if _, err := BuildPolicy(volWithCopies(), map[string]Resource{"nas": policyTargets()["nas"]}); err == nil || !strings.Contains(err.Error(), `"vps"`) {
		t.Errorf("err = %v, want it to name the missing target", err)
	}
}

func TestParsePolicyRefusesWhatItCannotUnderstand(t *testing.T) {
	good, _ := BuildPolicy(volWithCopies(), policyTargets())
	var doc map[string]any
	_ = json.Unmarshal([]byte(good), &doc)
	with := func(mut func(map[string]any)) string {
		var d map[string]any
		_ = json.Unmarshal([]byte(good), &d)
		mut(d)
		b, _ := json.Marshal(d)
		return string(b)
	}
	copies := func(d map[string]any) []any { return d["copies"].([]any) }
	tests := map[string]struct {
		in   string
		want string
	}{
		"not json":              {"nope", "not a policy tink wrote"},
		"another protocol":      {with(func(d map[string]any) { d["proto"] = 2 }), "protocol 2"},
		"no protocol":           {with(func(d map[string]any) { delete(d, "proto") }), "protocol 0"},
		"an unknown field":      {with(func(d map[string]any) { d["colour"] = "red" }), "not a policy tink wrote"},
		"a copy with no target": {with(func(d map[string]any) { copies(d)[0].(map[string]any)["target"] = map[string]any{} }), "no target name"},
		"a bad schedule":        {with(func(d map[string]any) { copies(d)[0].(map[string]any)["schedule"] = "whenever" }), "schedule"},
		"a bad retain":          {with(func(d map[string]any) { copies(d)[0].(map[string]any)["retain"] = "0d" }), "retain"},
		"a bad cadence":         {with(func(d map[string]any) { d["verify"].(map[string]any)["every"] = "hourly" }), "daily, weekly or monthly"},
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

func liveVolume(cfg map[string]string) *api.StorageVolume {
	return &api.StorageVolume{StorageVolumePut: api.StorageVolumePut{Config: cfg}}
}

func TestDecideVolumeConvergesThePolicy(t *testing.T) {
	r, targets := volWithCopies(), policyTargets()
	policy, _ := BuildPolicy(r, targets)
	snaps := map[string]string{"snapshots.schedule": "0 3 * * *", "snapshots.expiry": "14d"}
	with := func(extra map[string]string) *api.StorageVolume {
		c := map[string]string{}
		for k, v := range snaps {
			c[k] = v
		}
		for k, v := range extra {
			c[k] = v
		}
		return liveVolume(c)
	}

	if p := decideVolume(r, nil, volumeEnv{targets: targets}); p.Action != ActionCreate || !strings.Contains(strings.Join(p.Changes, "|"), "backup policy: ") {
		t.Errorf("a new volume is created with the policy: %v %v", p.Action, p.Changes)
	}
	if p := decideVolume(r, with(nil), volumeEnv{targets: targets}); p.Action != ActionUpdate || !strings.Contains(strings.Join(p.Changes, "|"), "backup policy: ") {
		t.Errorf("a volume that lacks the policy is updated: %v %v", p.Action, p.Changes)
	}
	if p := decideVolume(r, with(map[string]string{PolicyKey: policy}), volumeEnv{targets: targets}); p.Action != ActionNone {
		t.Errorf("a volume carrying exactly the policy is converged: %v %v", p.Action, p.Changes)
	}
	// drift: someone edited the key by hand, or the YAML moved on
	r2 := volWithCopies()
	r2.Backup.Copies[0].Retain = "60d"
	p := decideVolume(r2, with(map[string]string{PolicyKey: policy}), volumeEnv{targets: targets})
	if p.Action != ActionUpdate || !strings.Contains(strings.Join(p.Changes, "|"), "backup policy: ") {
		t.Errorf("a changed declaration must show as an update, which is the point of keeping the policy on the volume: %v %v", p.Action, p.Changes)
	}
}

func TestDecideVolumeRemovesAPolicyTheDeclarationNoLongerHas(t *testing.T) {
	targets := policyTargets()
	stale := liveVolume(map[string]string{PolicyKey: `{"proto":1}`})
	for name, r := range map[string]Resource{
		"copies removed": {Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}}},
		"opted out":      {Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "regenerable"}},
		"block removed":  {Kind: KindStorageVolume, Name: "v"},
	} {
		p := decideVolume(r, stale, volumeEnv{targets: targets})
		if p.Action != ActionUpdate || !strings.Contains(strings.Join(p.Changes, "|"), "removed") {
			t.Errorf("%s: the stale policy must be removed, or the scheduler keeps copying a volume that opted out: %v %v", name, p.Action, p.Changes)
		}
	}
	// and nothing to do when it is already gone
	if p := decideVolume(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "x"}}, liveVolume(nil), volumeEnv{targets: targets}); p.Action != ActionNone {
		t.Errorf("no policy wanted and none present is converged, got %v %v", p.Action, p.Changes)
	}
}

func TestDecideVolumeBlocksOnAMissingTarget(t *testing.T) {
	p := decideVolume(volWithCopies(), nil, volumeEnv{targets: map[string]Resource{}})
	if p.Action != ActionBlocked || len(p.Blocked) != 1 {
		t.Errorf("a copy naming a target that is not there must block the volume (it cannot be resolved): %v %v", p.Action, p.Blocked)
	}
}

func TestVolumeBackupConfigSetAndRemove(t *testing.T) {
	set, remove, err := volumeBackupConfig(volWithCopies(), volumeEnv{targets: policyTargets()})
	if err != nil || remove != nil || set[PolicyKey] == "" || set["snapshots.schedule"] != "0 3 * * *" {
		t.Errorf("set = %v remove = %v err = %v", set, remove, err)
	}
	set, remove, err = volumeBackupConfig(Resource{Kind: KindStorageVolume, Name: "v"}, volumeEnv{})
	if err != nil || len(set) != 0 || len(remove) != 1 || remove[0] != PolicyKey {
		t.Errorf("a bare volume sets nothing and clears the policy: set = %v remove = %v err = %v", set, remove, err)
	}
}

// The helper reads copy policies up to some protocol. A policy of a newer one would be skipped by it, silently, and the volume's copies
// would stop: so it is not written.

func volumeWithCopies() Resource {
	return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
		Copies: []BackupCopy{{Target: "nas", Schedule: "@daily", Retain: "30d"}}}}
}

func planVolume(t *testing.T, current map[string]string, env volumeEnv) PlannedResource {
	t.Helper()
	env.targets = policyTargets()
	srv := &volumeServer{vol: current}
	p, err := planStorageVolume(srv, volumeWithCopies(), env)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAPolicyTheHelperCannotReadIsNotWritten(t *testing.T) {
	// this tink writes protocol 2; the helper reads up to 1
	p := planVolume(t, nil, volumeEnv{helperReads: 1, helperLabel: "tink-helper/helper", policyProto: 2})
	if p.Action != ActionBlocked || len(p.Blocked) != 1 {
		t.Fatalf("action = %v %v: a create that would write a policy the helper skips must be blocked", p.Action, p.Blocked)
	}
	for _, want := range []string{"tink-helper/helper", "up to protocol 1", "writes protocol 2", "silently", "tink helper upgrade"} {
		if !strings.Contains(p.Blocked[0], want) {
			t.Errorf("the reason must say %q: %s", want, p.Blocked[0])
		}
	}
	// an update that would write it too
	if p := planVolume(t, map[string]string{PolicyKey: "{}"}, volumeEnv{helperReads: 1, policyProto: 2}); p.Action != ActionBlocked {
		t.Errorf("an update: %v", p.Action)
	}
}

func TestAPolicyTheHelperCanReadIsWrittenAsBefore(t *testing.T) {
	for name, env := range map[string]volumeEnv{
		"the helper reads what this tink writes":   {helperReads: 2, policyProto: 2},
		"the helper reads newer":                   {helperReads: 3, policyProto: 2},
		"there is no helper":                       {helperReads: 0, policyProto: 2},
		"the real protocol and a helper that does": {helperReads: PolicyProto},
	} {
		if p := planVolume(t, nil, env); p.Action != ActionCreate {
			t.Errorf("%s: action = %v %v", name, p.Action, p.Blocked)
		}
	}
}

func TestAPolicyAlreadyOnTheVolumeIsNotBlockedAndAVolumeWithoutCopiesNeverIs(t *testing.T) {
	// the same policy is already there: nothing is being written, so nothing to refuse (the helper's own status says it skips it)
	want, _ := BuildPolicy(volumeWithCopies(), policyTargets())
	env := volumeEnv{helperReads: 1, policyProto: 2}
	if p := planVolume(t, map[string]string{PolicyKey: want}, env); p.Action == ActionBlocked {
		t.Errorf("nothing is written, so nothing is refused: %v", p.Blocked)
	}
	// a volume that declares no copies writes no policy, whatever the helper reads
	env.targets = policyTargets()
	plain := Resource{Kind: KindStorageVolume, Name: "scratch", Backup: &VolumeBackup{None: "regenerable"}}
	if p, err := planStorageVolume(&volumeServer{}, plain, env); err != nil || p.Action == ActionBlocked {
		t.Errorf("%v %v", p.Action, err)
	}
}

func TestWithHelperPolicyIsWhatPlanningIsToldAndNothingIsCheckedWithout(t *testing.T) {
	o := (PlanOptions{}).WithHelperPolicy("h", 1)
	if o.helperReads != 1 || o.helperLabel != "h" {
		t.Errorf("%+v", o)
	}
	if (volumeEnv{}).helperCannotRead() != "" {
		t.Error("a plan that was told nothing about a helper checks nothing")
	}
}

// volumeServer answers the one read planStorageVolume makes: the volume's config, or not found.
type volumeServer struct {
	incus.InstanceServer
	vol map[string]string
}

func (s *volumeServer) UseProject(string) incus.InstanceServer { return s }
func (s *volumeServer) GetStoragePoolVolume(_, _, n string) (*api.StorageVolume, string, error) {
	if s.vol == nil {
		return nil, "", errNotFound
	}
	return &api.StorageVolume{Name: n, StorageVolumePut: api.StorageVolumePut{Config: s.vol}}, "", nil
}
