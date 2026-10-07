package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

func target(name, location, remote, pool string) Resource {
	return Resource{Kind: KindBackupTarget, Name: name, Location: location, Engine: EngineIncus, Remote: remote, Pool: pool}
}

func volWith(name, pool string, snap bool, copies ...string) Resource {
	b := &VolumeBackup{}
	if snap {
		b.Snapshots = &SnapshotPolicy{Schedule: "@daily", Retain: "14d"}
	}
	for _, t := range copies {
		b.Copies = append(b.Copies, BackupCopy{Target: t, Schedule: "@daily", Retain: "30d"})
	}
	return Resource{Kind: KindStorageVolume, Name: name, Pool: pool, Backup: b}
}

func TestValidateBackupTarget(t *testing.T) {
	ok := func(r Resource) {
		t.Helper()
		if err := Validate(r); err != nil {
			t.Errorf("%+v: unexpected error %v", r, err)
		}
	}
	bad := func(r Resource, want string) {
		t.Helper()
		if err := Validate(r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: error = %v, want it to contain %q", r, err, want)
		}
	}
	ok(target("t", LocationOtherHost, "macpro", ""))
	ok(target("t", LocationOffsite, "vps", "default"))
	ok(target("t", LocationOtherHost, "", "nas")) // another pool on this server, e.g. the truenas driver

	bad(target("t", "", "macpro", ""), "needs location")
	bad(target("t", "moon", "macpro", ""), "location must be")
	bad(Resource{Kind: KindBackupTarget, Name: "t", Location: LocationOffsite, Remote: "r"}, "needs engine")
	bad(Resource{Kind: KindBackupTarget, Name: "t", Location: LocationOffsite, Engine: "restic", Remote: "r"}, "not supported yet")
	bad(target("t", LocationOffsite, "", ""), "remote (another Incus server) or pool")
	bad(target("t", LocationOffsite, "host:8443", ""), "bare Incus remote name")
	bad(target("t", LocationOffsite, "r", "a/b"), "bare storage pool name")
	// fields that belong to other kinds
	bad(Resource{Kind: KindStorageVolume, Name: "v", Location: LocationOffsite}, "does not use field")
	bad(Resource{Kind: KindInstance, Name: "i", Remote: "r"}, "does not use field")
}

func TestValidateBackupCopiesAndVerify(t *testing.T) {
	bad := func(b *VolumeBackup, want string) {
		t.Helper()
		err := Validate(Resource{Kind: KindStorageVolume, Name: "v", Backup: b})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: error = %v, want it to contain %q", b, err, want)
		}
	}
	cp := BackupCopy{Target: "t", Schedule: "@daily", Retain: "30d"}

	for _, b := range []*VolumeBackup{
		{Copies: []BackupCopy{cp}}, // copies without snapshots is fine
		{Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}, Copies: []BackupCopy{cp}, Verify: "weekly"},
		{Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}, Verify: "monthly"}, // verify a local snapshot
	} {
		if err := Validate(Resource{Kind: KindStorageVolume, Name: "v", Backup: b}); err != nil {
			t.Errorf("%+v: unexpected error %v", b, err)
		}
	}

	bad(&VolumeBackup{Verify: "weekly"}, "give snapshots") // nothing to verify
	bad(&VolumeBackup{None: "x", Copies: []BackupCopy{cp}}, "mutually exclusive")
	bad(&VolumeBackup{None: "x", Verify: "daily"}, "mutually exclusive")
	bad(&VolumeBackup{Copies: []BackupCopy{cp}, Verify: "hourly"}, "verify must be daily, weekly or monthly")
	bad(&VolumeBackup{Copies: []BackupCopy{{Schedule: "@daily", Retain: "1d"}}}, "target is required")
	bad(&VolumeBackup{Copies: []BackupCopy{cp, cp}}, "twice")
	bad(&VolumeBackup{Copies: []BackupCopy{{Target: "t", Schedule: "", Retain: "1d"}}}, "schedule: required")
	bad(&VolumeBackup{Copies: []BackupCopy{{Target: "t", Schedule: "@daily", Retain: ""}}}, "retain: required")
	bad(&VolumeBackup{Copies: []BackupCopy{{Target: "t", Schedule: "@daily", Retain: "0d"}}}, "expiry syntax")
}

func TestLevelsRejectsUnknownCopyTarget(t *testing.T) {
	_, err := Levels([]Resource{target("macpro", LocationOtherHost, "macpro", ""), volWith("v", "", true, "macpr0")})
	if err == nil || !strings.Contains(err.Error(), `target "macpr0"`) {
		t.Fatalf("err = %v, want a hard error naming the unknown target (a typo must not silently drop a backup leg)", err)
	}
}

func TestLevelsOrdersTargetsBeforeVolumes(t *testing.T) {
	levels, err := Levels([]Resource{volWith("v", "", true, "macpro"), target("macpro", LocationOtherHost, "macpro", "")})
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 2 || levels[0][0].Name != "macpro" || levels[1][0].Name != "v" {
		t.Errorf("levels = %v, want the target in a level before the volume that copies to it", levels)
	}
}

func TestBackupWarnings(t *testing.T) {
	targets := map[string]Resource{
		"macpro":  target("macpro", LocationOtherHost, "macpro", ""),
		"macpro2": target("macpro2", LocationOtherHost, "macpro", "second"),
		"vps":     target("vps", LocationOffsite, "vps", ""),
		"nas":     target("nas", LocationOtherHost, "", "nas"),
		"local":   target("local", LocationSameHost, "", "default"),
		"tank2":   target("tank2", LocationSameHost, "", "tank"),
	}
	tests := []struct {
		name string
		vol  Resource
		want []string // substrings, each must appear in the joined warnings
		deny []string // substrings that must not
	}{
		{"snapshots only: no copies at all", volWith("v", "", true),
			[]string{"3-2-1 not met (1 of 3 copies", "2 copies in other failure domains", "off-site copy"}, nil},
		{"one copy, other host", volWith("v", "", true, "macpro"),
			[]string{"(2 of 3 copies", "1 more copy in another failure domain", "off-site copy"}, nil},
		{"two copies in different domains, one off-site: met, and nothing to say",
			volWith("v", "", true, "macpro", "vps"), nil, nil},
		{"NAS pool + off-site VPS: met, and nothing to say", volWith("v", "", true, "nas", "vps"), nil, nil},
		{"two copies on different hosts but none off-site", volWith("v", "", false, "macpro", "nas"),
			[]string{"(3 of 3 copies", "off-site copy"}, []string{"share one failure domain"}},
		{"two copies on the same remote and pool share a domain",
			volWith("v", "", true, "macpro", "macpro"), // duplicate target is rejected by Validate; domains still collapse
			[]string{"share one failure domain"}, nil},
		{"two pools on one remote are separate domains", volWith("v", "", true, "macpro", "macpro2"),
			[]string{"off-site copy"}, []string{"share one failure domain"}},
		{"copy to the live volume's own pool is not counted", volWith("v", "", true, "local", "vps"),
			[]string{"not a separate failure domain and is not counted", "(2 of 3 copies", "1 more copy"}, nil},
		{"live volume on another pool: same-named target pool matters", volWith("v", "tank", true, "tank2", "vps"),
			[]string{"live volume's own pool (local:tank)", "(2 of 3 copies"}, nil},
		{"opt-out is not evaluated", Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{None: "x"}}, nil, nil},
		{"no backup block is not evaluated here", Resource{Kind: KindStorageVolume, Name: "v"}, nil, nil},
		{"not a volume", Resource{Kind: KindInstance, Name: "i"}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(backupWarnings(tc.vol, targets), "\n")
			if tc.want == nil && got != "" {
				t.Fatalf("expected no warnings, got:\n%s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("warnings missing %q:\n%s", w, got)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Errorf("warnings must not contain %q:\n%s", d, got)
				}
			}
		})
	}
}

func TestLoadFileParsesBackupTargetsAndCopies(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tink.yaml")
	doc := `kind: backup-target
name: macpro
location: other-host
engine: incus
remote: macpro
---
kind: backup-target
name: nas
location: other-host
engine: incus
pool: nas
---
kind: storage-volume
name: lib
backup:
  snapshots: {schedule: "0 3 * * *", retain: 14d}
  copies:
    - {target: macpro, schedule: "0 4 * * *", retain: 30d}
    - {target: nas, schedule: "0 5 * * *", retain: 30d}
  verify: weekly
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 3 || rs[0].Remote != "macpro" || rs[0].Location != LocationOtherHost || rs[0].Engine != EngineIncus || rs[1].Pool != "nas" {
		t.Fatalf("targets parsed wrong: %+v %+v", rs[0], rs[1])
	}
	b := rs[2].Backup
	if b == nil || len(b.Copies) != 2 || b.Copies[1] != (BackupCopy{Target: "nas", Schedule: "0 5 * * *", Retain: "30d"}) || b.Verify != "weekly" || b.Snapshots == nil {
		t.Fatalf("volume backup parsed wrong: %+v", b)
	}
	if _, err := Levels(rs); err != nil {
		t.Errorf("a well-formed stack must resolve: %v", err)
	}
}

func TestWithTargetsNeverLetsASubsetReplaceTheStack(t *testing.T) {
	stack := []Resource{target("macpro", LocationOtherHost, "macpro", ""), volWith("v", "", true, "macpro")}

	// `tink plan` plans one dependency level at a time: the volume's level holds no targets at all.
	// That used to wipe them, and every volume then reported "1 of 3 copies".
	opts := PlanOptions{}.ForResources(stack).withTargets(stack[1:])
	if _, ok := opts.targets["macpro"]; !ok {
		t.Fatalf("targets = %v; a level without targets must not replace the stack's", opts.targets)
	}
	// A caller that passes the whole stack and sets nothing still gets them.
	if _, ok := (PlanOptions{}).withTargets(stack).targets["macpro"]; !ok {
		t.Error("withTargets should fill unset targets from the resources it is given")
	}
}

func TestBackupWarningsSaysSoWhenATargetIsUnknown(t *testing.T) {
	got := strings.Join(backupWarnings(volWith("v", "", true, "macpro"), nil), "\n")
	if !strings.Contains(got, `copy target "macpro" is not known to the planner`) {
		t.Errorf("a missing target must be reported loudly, got:\n%s", got)
	}
}

func TestVerifyWarning(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	stampAt := func(age time.Duration) map[string]string {
		return map[string]string{StampVerifiedAt: now.Add(-age).Format(time.RFC3339)}
	}
	vol := func(cadence string) Resource {
		return Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
			Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}, Verify: cadence}}
	}
	tests := []struct {
		name    string
		r       Resource
		current map[string]string
		want    string // substring; "" means no warning
	}{
		{"never verified", vol("weekly"), nil, "never been verified -- run `tink backup verify lib`"},
		{"fresh within the cadence", vol("weekly"), stampAt(6 * 24 * time.Hour), ""},
		{"stale by days", vol("weekly"), stampAt(12 * 24 * time.Hour), "last verified 12d ago, older than the declared verify: weekly"},
		{"stale by hours (daily cadence)", vol("daily"), stampAt(30 * time.Hour), "last verified 30h ago"},
		{"monthly allows 31 days", vol("monthly"), stampAt(30 * 24 * time.Hour), ""},
		{"a stamp tink did not write is replaced, not trusted", vol("weekly"), map[string]string{StampVerifiedAt: "last tuesday"}, "not a timestamp tink wrote"},
		{"no cadence declared: no nagging", vol(""), nil, ""},
		{"fresh restore-only stamp does not satisfy a declared check",
			func() Resource {
				r := vol("weekly")
				r.Backup.VerifyCheck = &VerifyCheck{Image: "i", Command: []string{"true"}}
				return r
			}(),
			map[string]string{StampVerifiedAt: now.Add(-time.Hour).Format(time.RFC3339), StampVerifiedWith: "restore"},
			"only proved the snapshot restores"},
		{"fresh check stamp satisfies a declared check",
			func() Resource {
				r := vol("weekly")
				r.Backup.VerifyCheck = &VerifyCheck{Image: "i", Command: []string{"true"}}
				return r
			}(),
			map[string]string{StampVerifiedAt: now.Add(-time.Hour).Format(time.RFC3339), StampVerifiedWith: "check"},
			""},
		{"restore-only is fine when no check is declared", vol("weekly"),
			map[string]string{StampVerifiedAt: now.Add(-time.Hour).Format(time.RFC3339), StampVerifiedWith: "restore"}, ""},
		{"opt-out: nothing to verify", Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{None: "x"}}, nil, ""},
		{"no backup block", Resource{Kind: KindStorageVolume, Name: "lib"}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyWarning(tc.r, tc.current, now)
			if tc.want == "" && got != "" || !strings.Contains(got, tc.want) {
				t.Errorf("warning = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestDecideVolumeWarnsAboutStaleVerificationOnlyForExistingVolumes(t *testing.T) {
	r := Resource{Kind: KindStorageVolume, Name: "lib", Backup: &VolumeBackup{
		Snapshots: &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}, Verify: "weekly"}}
	if w := strings.Join(decideVolume(r, nil, volumeEnv{}).Warnings, "|"); strings.Contains(w, "verified") {
		t.Errorf("a volume that does not exist yet has nothing to verify, got %q", w)
	}
	existing := &api.StorageVolume{StorageVolumePut: api.StorageVolumePut{Config: map[string]string{}}}
	if w := strings.Join(decideVolume(r, existing, volumeEnv{}).Warnings, "|"); !strings.Contains(w, "never been verified") {
		t.Errorf("an existing, never-verified volume must be warned about, got %q", w)
	}
}

func TestParseVerifyScalarAndMapping(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tink.yaml")
	doc := `kind: storage-volume
name: a
backup:
  snapshots: {schedule: "@daily", retain: 7d}
  verify: weekly
---
kind: storage-volume
name: b
backup:
  snapshots: {schedule: "@daily", retain: 7d}
  verify:
    every: daily
    check:
      image: docker-oci:library/alpine:3
      command: [sh, -c, "test -s /data/x"]
      mount: /mnt/restored
---
kind: storage-volume
name: c
backup:
  snapshots: {schedule: "@daily", retain: 7d}
  verify:
    check: {image: docker-oci:library/alpine:3, command: [true]}
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if rs[0].Backup.Verify != "weekly" || rs[0].Backup.VerifyCheck != nil {
		t.Errorf("scalar form: %+v", rs[0].Backup)
	}
	if b := rs[1].Backup; b.Verify != "daily" || b.VerifyCheck == nil || b.VerifyCheck.Image != "docker-oci:library/alpine:3" ||
		len(b.VerifyCheck.Command) != 3 || b.VerifyCheck.Mount != "/mnt/restored" {
		t.Errorf("mapping form: %+v / %+v", b, b.VerifyCheck)
	}
	if b := rs[2].Backup; b.Verify != "" || b.VerifyCheck == nil {
		t.Errorf("a check with no cadence is allowed (verify on demand, no nagging): %+v", b)
	}
}

func TestValidateVerifyCheck(t *testing.T) {
	snap := &SnapshotPolicy{Schedule: "@daily", Retain: "7d"}
	chk := func(c *VerifyCheck) error {
		return Validate(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{Snapshots: snap, VerifyCheck: c}})
	}
	if err := chk(&VerifyCheck{Image: "i", Command: []string{"true"}}); err != nil {
		t.Errorf("valid check rejected: %v", err)
	}
	for _, c := range []*VerifyCheck{
		{Command: []string{"true"}},
		{Image: "i"},
		{Image: "i", Command: []string{"true"}, Mount: "relative/path"},
	} {
		if err := chk(c); err == nil {
			t.Errorf("%+v: expected an error", c)
		}
	}
	if err := Validate(Resource{Kind: KindStorageVolume, Name: "v", Backup: &VolumeBackup{
		None: "x", VerifyCheck: &VerifyCheck{Image: "i", Command: []string{"true"}}}}); err == nil {
		t.Error("none excludes a verify check")
	}
}
