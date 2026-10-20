package volbackup

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

// This file is about `--check-project`: a verification that makes its scratch volume and throwaway instance in a project other than the volume's, and
// records the result on the volume in the volume's own. What it needs from the fake is that a volume belongs to a project, as in Incus, so that
// reaching one through the wrong project's handle fails the way it would live: the volume is not found there. Set verifyFake.perProject for that
// (newProjectsFake does); the older tests leave it off and keep one flat namespace. An instance is always found only in its own project (see verify_test.go).

const (
	sourceProject = "tink"
	checkProj     = "tink-verify"
	scratchVolume = "lib-verify-20261013-120000"
	policyText    = `{"proto":1,"verify":{"every":"weekly"}}`
)

// volumeKey is the key a volume has in the shared maps of the fake, when volumes are kept per project.
func (f *verifyFake) volumeKey(project, pool string) string {
	if !f.perProject {
		return pool
	}
	return project + "~" + pool
}

// addIn puts a volume in a project (perProject only).
func (f *verifyFake) addIn(project, pool, name string, cfg map[string]string) {
	f.add(f.volumeKey(project, pool), name, cfg)
}

// volumesIn lists the volumes of a pool in one project, sorted (perProject only).
func (f *verifyFake) volumesIn(project, pool string) []string {
	return f.names(f.volumeKey(project, pool))
}

func (f *verifyFake) at(project string) *projectView { return &projectView{f, project} }

// The volume calls of a verification, by the project of the handle they go through. Without perProject they are fakeIncus's own, unchanged.

func (f *verifyFake) GetStoragePoolVolume(pool, typ, name string) (*api.StorageVolume, string, error) {
	return f.at(hostProject).GetStoragePoolVolume(pool, typ, name)
}
func (p *projectView) GetStoragePoolVolume(pool, typ, name string) (*api.StorageVolume, string, error) {
	return p.fakeIncus.GetStoragePoolVolume(p.volumeKey(p.project, pool), typ, name)
}

func (f *verifyFake) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	return f.at(hostProject).GetStoragePoolVolumes(pool)
}
func (p *projectView) GetStoragePoolVolumes(pool string) ([]api.StorageVolume, error) {
	return p.fakeIncus.GetStoragePoolVolumes(p.volumeKey(p.project, pool))
}

func (f *verifyFake) GetStoragePoolVolumeSnapshots(pool, typ, name string) ([]api.StorageVolumeSnapshot, error) {
	return f.at(hostProject).GetStoragePoolVolumeSnapshots(pool, typ, name)
}
func (p *projectView) GetStoragePoolVolumeSnapshots(pool, typ, name string) ([]api.StorageVolumeSnapshot, error) {
	return p.fakeIncus.GetStoragePoolVolumeSnapshots(p.volumeKey(p.project, pool), typ, name)
}

func (f *verifyFake) UpdateStoragePoolVolume(pool, typ, name string, put api.StorageVolumePut, etag string) error {
	return f.at(hostProject).UpdateStoragePoolVolume(pool, typ, name, put, etag)
}
func (p *projectView) UpdateStoragePoolVolume(pool, typ, name string, put api.StorageVolumePut, etag string) error {
	p.updateLog = append(p.updateLog, p.project+"/"+pool+"/"+name)
	return p.fakeIncus.UpdateStoragePoolVolume(p.volumeKey(p.project, pool), typ, name, put, etag)
}

func (f *verifyFake) DeleteStoragePoolVolume(pool, typ, name string) error {
	return f.at(hostProject).DeleteStoragePoolVolume(pool, typ, name)
}
func (p *projectView) DeleteStoragePoolVolume(pool, typ, name string) error {
	key := p.volumeKey(p.project, pool)
	if p.perProject && !p.failDelete {
		v, ok := p.vols[key+"/"+name]
		if !ok {
			return api.StatusErrorf(http.StatusNotFound, "Storage volume not found") // delete is not idempotent in Incus
		}
		if p.deletedConfig == nil {
			p.deletedConfig = map[string]map[string]string{}
		}
		p.deletedConfig[key+"/"+name] = v.Config
	}
	return p.fakeIncus.DeleteStoragePoolVolume(key, typ, name)
}

func (f *verifyFake) CopyStoragePoolVolume(pool string, src incus.InstanceServer, srcPool string, vol api.StorageVolume, args *incus.StoragePoolVolumeCopyArgs) (incus.RemoteOperation, error) {
	return f.at(hostProject).CopyStoragePoolVolume(pool, src, srcPool, vol, args)
}

// CopyStoragePoolVolume is called on the DESTINATION, and takes the source's project from the handle the source is given, as the Incus client does.
func (p *projectView) CopyStoragePoolVolume(pool string, src incus.InstanceServer, srcPool string, vol api.StorageVolume, args *incus.StoragePoolVolumeCopyArgs) (incus.RemoteOperation, error) {
	if !p.perProject {
		return p.fakeIncus.CopyStoragePoolVolume(pool, src, srcPool, vol, args)
	}
	var srcProject string
	switch s := src.(type) {
	case *projectView:
		srcProject = s.project
	case *verifyFake:
		srcProject = hostProject
	case *fakeIncus: // another server: its volumes are not kept per project
		p.copyLog = append(p.copyLog, fmt.Sprintf("remote/%s/%s -> %s/%s/%s", srcPool, vol.Name, p.project, pool, args.Name))
		return p.fakeIncus.CopyStoragePoolVolume(p.volumeKey(p.project, pool), s, srcPool, vol, args)
	default:
		return nil, fmt.Errorf("test: a source of type %T", src)
	}
	srcKey := p.volumeKey(srcProject, srcPool)
	if _, ok := p.vols[srcKey+"/"+strings.SplitN(vol.Name, "/", 2)[0]]; !ok {
		return nil, api.StatusErrorf(http.StatusNotFound, "Storage volume not found in project %q", srcProject)
	}
	p.copyLog = append(p.copyLog, fmt.Sprintf("%s/%s/%s -> %s/%s/%s", srcProject, srcPool, vol.Name, p.project, pool, args.Name))
	return p.fakeIncus.CopyStoragePoolVolume(p.volumeKey(p.project, pool), p.fakeIncus, srcKey, vol, args)
}

// newProjectsFake is a server whose volume `lib` lives in the source project, with a copy policy on it (which a copy inherits, as a worst case, and which
// the scratch volume must be scrubbed of), and in which the given projects exist as well. The source project's `default` profile carries a NIC: the
// point of a check project is that its instance does not get that.
func newProjectsFake(pool, source string, others ...string) *verifyFake {
	f := newVerifyFake(pool)
	f.perProject = true
	f.inherit = true
	delete(f.vols, pool+"/lib")
	delete(f.snaps, pool+"/lib")
	f.addIn(source, pool, "lib", map[string]string{backupmeta.PolicyKey: policyText})
	f.snaps[f.volumeKey(source, pool)+"/lib"] = []string{"snap0"}
	f.projects = map[string]bool{source: true}
	for _, o := range others {
		f.projects[o] = true
	}
	f.setProfile(source, "default", map[string]map[string]string{"root": rootOn(pool), "eth0": {"type": "nic", "network": "incusbr0"}})
	return f
}

// recipeProfile is the `default` profile docs/volume-backup.md tells you to give a check project: a root disk and no NIC.
func recipeProfile(pool string) map[string]map[string]string {
	return map[string]map[string]string{"root": rootOn(pool)}
}

// restorePointOn puts a restore point of lib (as made in the source project) in a pool of the given project.
func (f *verifyFake) restorePointOn(project, pool, name string) {
	f.addIn(project, pool, name, map[string]string{
		backupmeta.MarkerCopyOf:     backupmeta.CopyOf(sourceProject, "default", "lib"),
		backupmeta.MarkerCopyAt:     "2026-10-12T03:00:00Z",
		backupmeta.MarkerCopyTarget: "nas",
		backupmeta.MarkerCopySnap:   "tink-copy-20261012-030000",
		backupmeta.MarkerCopyServer: "homelab",
	})
}

func TestVerifyInACheckProjectDoesItsWorkThereAndRecordsTheResultOnTheSource(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profile  map[string]map[string]string // the check project's default profile
		wantRoot bool                         // whether the creation request has to carry a root disk
	}{
		{"the recipe's profile: a root disk and no NIC", recipeProfile("default"), false},
		{"a default profile that is still empty", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir())
			f := newProjectsFake("default", sourceProject, checkProj)
			f.setProfile(checkProj, "default", tc.profile)
			var say bytes.Buffer

			res, err := Verify(f, Volume{Project: sourceProject, Name: "lib"}, VerifyOptions{Check: checkOf(""), CheckProject: checkProj, Now: verifyNow, Progress: &say})
			if err != nil {
				t.Fatal(err)
			}
			if res.With != "check" || !res.Recorded || res.Snapshot != "snap0" {
				t.Errorf("the check ran and was recorded: %+v", res)
			}
			if !reflect.DeepEqual(f.projectReads, []string{checkProj}) {
				t.Errorf("projects looked up = %v: the check project, once, and not the volume's own", f.projectReads)
			}

			// the throwaway instance is made, and found again, in the check project
			if len(f.created) != 1 || f.created[0].project != checkProj {
				t.Fatalf("instances made = %+v, want one, in %s", f.created, checkProj)
			}
			if !reflect.DeepEqual(f.created[0].req.Profiles, []string{"default"}) {
				t.Errorf("profiles = %v: the check project's bare default profile", f.created[0].req.Profiles)
			}
			if got := f.created[0].req.Devices["root"]; (got != nil) != tc.wantRoot {
				t.Errorf("the creation request's root disk = %v, want one: %v", got, tc.wantRoot)
			}
			if !reflect.DeepEqual(f.reads, []string{checkProj + "/default"}) {
				t.Errorf("profile reads = %v: the check project's default profile decides, never the volume's project's", f.reads)
			}
			if len(f.removed) != 1 || f.removed[0].project != checkProj {
				t.Fatalf("instances removed = %+v, want one, from %s", f.removed, checkProj)
			}
			for name, dev := range f.removed[0].devices {
				if dev["type"] == "nic" {
					t.Errorf("device %s is a NIC: the check instance must have no network at all", name)
				}
			}
			if got := f.removed[0].devices["data"]; got["source"] != scratchVolume || got["readonly"] != "true" || got["pool"] != "default" {
				t.Errorf("the data device = %v", got)
			}

			// the snapshot is copied from the source project into the check project, and only there is anything made or removed
			wantCopy := "tink/default/lib/snap0 -> tink-verify/default/" + scratchVolume
			if !reflect.DeepEqual(f.copyLog, []string{wantCopy}) {
				t.Errorf("copies = %v, want [%s]", f.copyLog, wantCopy)
			}
			if want := []string{"tink-verify~default/" + scratchVolume}; !reflect.DeepEqual(f.deletedVols, want) {
				t.Errorf("deleted volumes = %v, want %v", f.deletedVols, want)
			}
			if left := f.volumesIn(checkProj, "default"); len(left) != 0 || len(f.instances) != 0 {
				t.Errorf("left behind in the check project: volumes %v, instances %d", left, len(f.instances))
			}
			if got := f.volumesIn(sourceProject, "default"); !reflect.DeepEqual(got, []string{"lib"}) {
				t.Errorf("the source project holds %v afterwards, want just lib", got)
			}

			// the scratch volume was scrubbed of the copy policy it inherited, in the project it lives in, and the source keeps its own
			scrubbed, ok := f.deletedConfig["tink-verify~default/"+scratchVolume]
			if !ok {
				t.Fatalf("no record of the scratch volume's config at deletion: %v", f.deletedConfig)
			}
			if _, has := scrubbed[backupmeta.PolicyKey]; has {
				t.Errorf("the scratch volume still carries the copy policy: a scheduler would back up the scratch copy")
			}
			src := f.vols["tink~default/lib"].Config
			if src[backupmeta.PolicyKey] != policyText {
				t.Errorf("the source volume's policy = %q, must be untouched", src[backupmeta.PolicyKey])
			}

			// the result is on the source volume, written in the source project and in no other
			wantStamp := map[string]string{
				backupmeta.StampVerifiedAt:       "2026-10-13T12:00:00Z",
				backupmeta.StampVerifiedSnapshot: "snap0",
				backupmeta.StampVerifiedWith:     "check",
				backupmeta.StampVerifiedFrom:     "local",
			}
			for k, want := range wantStamp {
				if src[k] != want {
					t.Errorf("%s = %q on the source volume, want %q", k, src[k], want)
				}
			}
			var libWrites, scratchWrites []string
			for _, u := range f.updateLog {
				switch {
				case strings.HasSuffix(u, "/lib"):
					libWrites = append(libWrites, u)
				case strings.HasSuffix(u, "/"+scratchVolume):
					scratchWrites = append(scratchWrites, u)
				}
			}
			if !reflect.DeepEqual(libWrites, []string{"tink/default/lib"}) {
				t.Errorf("writes to lib = %v, want one, in the source project", libWrites)
			}
			if !reflect.DeepEqual(scratchWrites, []string{"tink-verify/default/" + scratchVolume}) {
				t.Errorf("writes to the scratch volume = %v, want one (the scrub), in the check project", scratchWrites)
			}

			for _, want := range []string{
				"to scratch volume " + scratchVolume + " in project tink-verify",
				" in project tink-verify (check-image)",
			} {
				if !strings.Contains(say.String(), want) {
					t.Errorf("progress must say where the check ran, wanted %q:\n%s", want, say.String())
				}
			}
			if got := strings.Contains(say.String(), `has no root disk, so the check instance gets one`); got != tc.wantRoot {
				t.Errorf("progress says the instance got a root disk = %v, want %v:\n%s", got, tc.wantRoot, say.String())
			}
			if tc.wantRoot && !strings.Contains(say.String(), `"default" profile of project tink-verify has no root disk`) {
				t.Errorf("the profile that was read is the check project's, and the line must say so:\n%s", say.String())
			}
		})
	}
}

func TestACheckProjectThatIsTheVolumesOwnChangesNothing(t *testing.T) {
	runIt := func(t *testing.T, checkProject string) (*verifyFake, string, error) {
		t.Setenv("INCUS_CONF", t.TempDir())
		f := newVerifyFake("default")
		f.setProfile("tink", "default", recipeProfile("default"))
		var say bytes.Buffer
		_, err := Verify(f, Volume{Project: "tink", Name: "lib"}, VerifyOptions{Check: checkOf(""), CheckProject: checkProject, Now: verifyNow, Progress: &say})
		return f, say.String(), err
	}
	plain, plainSay, err := runIt(t, "")
	if err != nil {
		t.Fatal(err)
	}
	named, namedSay, err := runIt(t, "tink")
	if err != nil {
		t.Fatal(err)
	}
	if namedSay != plainSay {
		t.Errorf("naming the volume's own project as the check project must read as before.\nwithout:\n%s\nwith:\n%s", plainSay, namedSay)
	}
	if len(named.projectReads) != 0 {
		t.Errorf("the volume's own project is not looked up: %v", named.projectReads)
	}
	if len(named.created) != 1 || named.created[0].project != "tink" || !reflect.DeepEqual(named.reads, plain.reads) {
		t.Errorf("made %+v, read profiles %v", named.created, named.reads)
	}
	if !reflect.DeepEqual(named.deletedVols, plain.deletedVols) || !reflect.DeepEqual(named.vols["default/lib"].Config, plain.vols["default/lib"].Config) {
		t.Errorf("deleted %v and recorded %v, want what the plain run did: %v and %v", named.deletedVols, named.vols["default/lib"].Config, plain.deletedVols, plain.vols["default/lib"].Config)
	}
	if left := named.leftOver("default"); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestTheDaemonsDefaultProjectCanBeNamedAsTheCheckProject(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	f := newProjectsFake("default", hostProject, hostProject)
	f.setProfile(hostProject, "default", recipeProfile("default"))

	res, err := Verify(f, Volume{Name: "lib"}, VerifyOptions{Check: checkOf(""), CheckProject: hostProject, Now: verifyNow})
	if err != nil || !res.Recorded {
		t.Fatalf("%+v %v", res, err)
	}
	if want := []string{"default/default/lib/snap0 -> default/default/" + scratchVolume}; !reflect.DeepEqual(f.copyLog, want) {
		t.Errorf("copies = %v, want %v", f.copyLog, want)
	}
	if got := f.vols["default~default/lib"].Config[backupmeta.StampVerifiedWith]; got != "check" {
		t.Errorf("verified-with = %q", got)
	}
	if left := f.volumesIn(hostProject, "default"); !reflect.DeepEqual(left, []string{"lib"}) || len(f.instances) != 0 {
		t.Errorf("left behind: volumes %v, instances %d", left, len(f.instances))
	}
}

func TestACheckProjectThatIsNotThereIsRefusedBeforeAnythingIsMade(t *testing.T) {
	for _, tc := range []struct {
		name    string
		check   *backupmeta.VerifyCheck
		perr    error
		wantErr string
	}{
		{"with a check", checkOf(""), nil, "project tink-verify does not exist: create it, see docs/volume-backup.md"},
		{"restoring only: the scratch volume needs the project as much", nil, nil, "project tink-verify does not exist: create it, see docs/volume-backup.md"},
		{"it cannot be looked up: that is not the same as being absent", checkOf(""), forbidden, "checking whether the check project tink-verify exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir())
			f := newProjectsFake("default", sourceProject) // no tink-verify
			f.projectErr = tc.perr

			_, err := Verify(f, Volume{Project: sourceProject, Name: "lib"}, VerifyOptions{Check: tc.check, CheckProject: checkProj, Now: verifyNow})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if tc.perr != nil && strings.Contains(err.Error(), "does not exist") {
				t.Errorf("a look-up that failed must not be reported as a project that is missing: %v", err)
			}
			if len(f.copyLog) != 0 || len(f.created) != 0 || len(f.reads) != 0 || len(f.updateLog) != 0 || len(f.deletedVols) != 0 {
				t.Errorf("nothing may be restored, made, read or recorded: copies %v, instances %d, profile reads %v, writes %v", f.copyLog, len(f.created), f.reads, f.updateLog)
			}
			if got := f.vols["tink~default/lib"].Config[backupmeta.StampVerifiedAt]; got != "" {
				t.Errorf("verified-at = %q on a verification that never ran", got)
			}
		})
	}
}

func TestVerifyFromATargetRestoresIntoTheCheckProject(t *testing.T) {
	const restorePoint = "lib-bk-20261012-030000"
	setup := func(t *testing.T, target Target) *verifyFake {
		t.Setenv("INCUS_CONF", t.TempDir())
		f := newProjectsFake("default", sourceProject, checkProj)
		f.setProfile(checkProj, "default", recipeProfile("default"))
		if target.Remote == "" {
			f.restorePointOn(sourceProject, target.Pool, restorePoint) // a pool target keeps its restore points in the volume's own project
		} else {
			remote := newFake("vps", "default")
			remote.add("default", restorePoint, map[string]string{
				backupmeta.MarkerCopyOf:     backupmeta.CopyOf(sourceProject, "default", "lib"),
				backupmeta.MarkerCopyAt:     "2026-10-12T03:00:00Z",
				backupmeta.MarkerCopyTarget: "vps",
				backupmeta.MarkerCopySnap:   "tink-copy-20261012-030000",
				backupmeta.MarkerCopyServer: "homelab",
			})
			useRemote(t, "vps", remote)
		}
		return f
	}
	for _, tc := range []struct {
		name     string
		target   Target
		wantCopy string
	}{
		{"a pool target on this server", Target{Name: "nas", Pool: "nas"}, "tink/nas/" + restorePoint + " -> tink-verify/default/" + scratchVolume},
		{"a remote target, relayed", Target{Name: "vps", Remote: "vps"}, "remote/default/" + restorePoint + " -> tink-verify/default/" + scratchVolume},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.target)
			target := tc.target
			var say bytes.Buffer

			res, err := Verify(f, Volume{Project: sourceProject, Name: "lib"}, VerifyOptions{Check: checkOf(""), From: &target, CheckProject: checkProj, Now: verifyNow, Progress: &say})
			if err != nil {
				t.Fatal(err)
			}
			if res.Snapshot != restorePoint || !res.Recorded {
				t.Errorf("%+v", res)
			}
			if !reflect.DeepEqual(f.copyLog, []string{tc.wantCopy}) {
				t.Errorf("copies = %v, want [%s]", f.copyLog, tc.wantCopy)
			}
			if tc.target.Remote != "" && !reflect.DeepEqual(f.modesSeen, []string{"relay"}) {
				t.Errorf("a remote is relayed through tink, modes = %v", f.modesSeen)
			}

			// the scratch volume was scrubbed of the markers that make a volume a restore point, where it lives
			cfg, ok := f.deletedConfig["tink-verify~default/"+scratchVolume]
			if !ok {
				t.Fatalf("the scratch volume was not made and removed in the check project: deleted %v", f.deletedVols)
			}
			for k := range cfg {
				if strings.HasPrefix(k, "user.tink.backup.copy-") || k == backupmeta.PolicyKey {
					t.Errorf("the scratch volume still carries %s", k)
				}
			}
			if len(f.created) != 1 || f.created[0].project != checkProj || len(f.removed) != 1 || f.removed[0].project != checkProj {
				t.Errorf("the instance must live and die in the check project: made %+v, removed %+v", f.created, f.removed)
			}

			// the restore point is not touched, and the stamp is on the source volume in its own project, saying where the backup came from
			if tc.target.Remote == "" {
				if got := f.volumesIn(sourceProject, "nas"); !reflect.DeepEqual(got, []string{restorePoint}) {
					t.Errorf("the restore point must be left alone: %v", got)
				}
			}
			src := f.vols["tink~default/lib"].Config
			if src[backupmeta.StampVerifiedFrom] != tc.target.Name || src[backupmeta.StampVerifiedSnapshot] != restorePoint {
				t.Errorf("the stamp = from %q snapshot %q, want %q and %q", src[backupmeta.StampVerifiedFrom], src[backupmeta.StampVerifiedSnapshot], tc.target.Name, restorePoint)
			}
			if left := f.volumesIn(checkProj, "default"); len(left) != 0 || len(f.instances) != 0 {
				t.Errorf("left behind in the check project: %v, instances %d", left, len(f.instances))
			}
		})
	}
}

// A failure anywhere cleans up in the check project and leaves the source volume as it was.
func TestVerifyInACheckProjectCleansUpThereOnEveryFailure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     func(f *verifyFake)
		wantErr  string
		wantMade int // instances made before it failed, all of which must be removed
	}{
		{"the check fails", func(f *verifyFake) { f.execCode, f.execOut = 3, "no such file" }, "verification check failed with exit status 3", 1},
		{"the check cannot be started", func(f *verifyFake) { f.execErr = errors.New("agent gone") }, "running the check", 1},
		{"Incus refuses to make the instance", func(f *verifyFake) { f.createErr = errors.New("storage pool is full") }, "storage pool is full", 0},
		{"Incus refuses the scratch volume's disk", func(f *verifyFake) { f.updateErr = errors.New("volume busy") }, "volume busy", 1},
		{"the check project's default profile cannot be read", func(f *verifyFake) { f.profileErr[checkProj+"/default"] = forbidden }, `reading the "default" profile of project tink-verify`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir())
			f := newProjectsFake("default", sourceProject, checkProj)
			f.setProfile(checkProj, "default", recipeProfile("default"))
			tc.fail(f)

			_, err := Verify(f, Volume{Project: sourceProject, Name: "lib"}, VerifyOptions{Check: checkOf(""), CheckProject: checkProj, Now: verifyNow})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "cleanup incomplete") {
				t.Errorf("the cleanup must have worked: %v", err)
			}
			if left := f.volumesIn(checkProj, "default"); len(left) != 0 || len(f.instances) != 0 {
				t.Errorf("left behind in the check project: volumes %v, instances %d", left, len(f.instances))
			}
			if len(f.removed) != tc.wantMade {
				t.Errorf("%d instances removed, want %d", len(f.removed), tc.wantMade)
			}
			if want := []string{"tink-verify~default/" + scratchVolume}; !reflect.DeepEqual(f.deletedVols, want) {
				t.Errorf("deleted volumes = %v, want %v: the scratch volume, from the check project, and nothing else", f.deletedVols, want)
			}
			src := f.vols["tink~default/lib"]
			if got := src.Config[backupmeta.StampVerifiedAt]; got != "" {
				t.Errorf("a failed verification must not look fresh, but verified-at = %q", got)
			}
			if got := f.volumesIn(sourceProject, "default"); !reflect.DeepEqual(got, []string{"lib"}) {
				t.Errorf("the source project holds %v afterwards, want just lib", got)
			}
			for _, r := range f.reads {
				if strings.HasPrefix(r, sourceProject+"/") {
					t.Errorf("read the source project's profile %s: only the check project's matters", r)
				}
			}
		})
	}
}

func TestACleanupThatFailsInTheCheckProjectNamesTheProject(t *testing.T) {
	const instance = "tink-verify-lib-20261013-120000"
	for _, tc := range []struct {
		name    string
		project string // the check project asked for
		want    []string
		notWant string
	}{
		{"another project", checkProj, []string{
			"cleanup incomplete, delete by hand",
			"instance " + instance + " (project tink-verify): busy",
			"volume default/" + scratchVolume + " (project tink-verify): connection lost",
		}, ""},
		{"the volume's own project, as before", "", []string{
			"instance " + instance + ": busy",
			"volume default/" + scratchVolume + ": connection lost",
		}, "(project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir())
			f := newProjectsFake("default", sourceProject, checkProj)
			f.setProfile(sourceProject, "default", recipeProfile("default"))
			f.setProfile(checkProj, "default", recipeProfile("default"))
			f.deleteInstErr = errors.New("busy")
			f.failDelete = true

			_, err := Verify(f, Volume{Project: sourceProject, Name: "lib"}, VerifyOptions{Check: checkOf(""), CheckProject: tc.project, Now: verifyNow})
			if err == nil {
				t.Fatal("a cleanup that could not finish must be an error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error must say %q:\n%v", want, err)
				}
			}
			if tc.notWant != "" && strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("the error must not name a project when there is only one: %v", err)
			}
		})
	}
}

func TestARestoreThatFailsInTheCheckProjectSaysWhichProject(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	f := newProjectsFake("default", sourceProject, checkProj)
	f.setProfile(checkProj, "default", recipeProfile("default"))
	f.failCopy = errors.New("no space left")

	_, err := Verify(f, Volume{Project: sourceProject, Name: "lib"}, VerifyOptions{Check: checkOf(""), CheckProject: checkProj, Now: verifyNow})
	if err == nil || !strings.Contains(err.Error(), "restoring into project tink-verify") || !strings.Contains(err.Error(), "no space left") {
		t.Fatalf("err = %v", err)
	}
	if len(f.created) != 0 || f.vols["tink~default/lib"].Config[backupmeta.StampVerifiedAt] != "" {
		t.Errorf("nothing may run or be recorded after a failed restore")
	}
}

// The fake is what the other tests lean on to catch a call made through the wrong project's handle, so it has to be strict about that itself.
func TestTheProjectsFakeFindsAVolumeOnlyInItsOwnProject(t *testing.T) {
	f := newProjectsFake("default", sourceProject, checkProj)
	if _, _, err := f.UseProject(sourceProject).GetStoragePoolVolume("default", "custom", "lib"); err != nil {
		t.Fatalf("the volume is in its own project: %v", err)
	}
	for _, other := range []incus.InstanceServer{f.UseProject(checkProj), f} {
		if _, _, err := other.GetStoragePoolVolume("default", "custom", "lib"); !api.StatusErrorCheck(err, http.StatusNotFound) {
			t.Errorf("a volume must not be found through another project's handle: %v", err)
		}
		if err := other.DeleteStoragePoolVolume("default", "custom", "lib"); !api.StatusErrorCheck(err, http.StatusNotFound) {
			t.Errorf("nor deleted through it: %v", err)
		}
	}
	// a copy takes its source's project from the handle the source is given
	op, err := f.UseProject(checkProj).CopyStoragePoolVolume("default", f.UseProject(checkProj), "default", api.StorageVolume{Name: "lib/snap0"}, &incus.StoragePoolVolumeCopyArgs{Name: "x"})
	if !api.StatusErrorCheck(err, http.StatusNotFound) {
		t.Errorf("copying from a project that does not hold the volume must fail: %v %v", op, err)
	}
	if names := f.volumesIn(checkProj, "default"); len(names) != 0 {
		t.Errorf("a refused copy made %v", names)
	}
}
