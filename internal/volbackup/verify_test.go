package volbackup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/backupmeta"
)

// verifyFake is a server Verify can run its check on: fakeIncus supplies the volumes and snapshots, and on top of that it keeps profiles per project and the
// throwaway instances. It holds Incus to the rule this file is about: an instance is refused unless its profiles and the creation request, together,
// carry exactly one root disk ("No root device could be found", in Incus's own words). An instance is found only in the project it was made in, as in
// Incus. Other calls panic through the nil embedded interface.
//
// Volumes are one flat namespace per pool, whichever project a handle is scoped to, until a test sets perProject (see verify_project_test.go): then they
// belong to the project of the handle they are reached through, which is what a test of a verification that works in another project needs.
type verifyFake struct {
	*fakeIncus
	devices    map[string]map[string]map[string]string // "project/name" of a profile -> its devices
	profileErr map[string]error                        // "project/name": reading it fails
	reads      []string                                // "project/name" of every profile read

	instances map[string]*madeInstance
	created   []madeInstance // every instance made, as the creation request left it
	removed   []madeInstance // every instance deleted, as it was then

	createErr, updateErr, execErr error
	deleteInstErr                 error // deleting an instance fails
	execCode                      int
	execOut                       string
	commands                      [][]string

	projects     map[string]bool // the projects that exist, for the ones a test asks about; the volume's own project is never looked up
	projectErr   error           // looking a project up fails
	projectReads []string        // every project looked up

	perProject    bool
	copyLog       []string                     // each volume copy: "source -> destination", as project/pool/name
	updateLog     []string                     // each volume update: "project/pool/name"
	deletedConfig map[string]map[string]string // the config a volume had when it was deleted, by "project~pool/name" (perProject only)
}

type madeInstance struct {
	project string
	req     api.InstancesPost
	devices api.DevicesMap // the instance's own devices now
	config  map[string]string
	status  string
}

const hostProject = "default"

func newVerifyFake(pool string) *verifyFake {
	f := &verifyFake{fakeIncus: newFake("homelab", pool), devices: map[string]map[string]map[string]string{}, profileErr: map[string]error{}, instances: map[string]*madeInstance{}}
	f.add(pool, "lib", nil)
	f.snaps[pool+"/lib"] = []string{"snap0"}
	// a host as `incus admin init` leaves it: the default project's default profile has a root disk
	f.setProfile(hostProject, "default", map[string]map[string]string{"root": {"type": "disk", "path": "/", "pool": pool}})
	return f
}

func (f *verifyFake) setProfile(project, name string, devices map[string]map[string]string) {
	f.devices[project+"/"+name] = devices
}

type projectView struct {
	*verifyFake
	project string
}

func (f *verifyFake) UseProject(p string) incus.InstanceServer { return &projectView{f, p} }

func (f *verifyFake) GetProject(name string) (*api.Project, string, error) {
	f.projectReads = append(f.projectReads, name)
	if f.projectErr != nil {
		return nil, "", f.projectErr
	}
	if !f.projects[name] {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "Project not found")
	}
	return &api.Project{Name: name}, "", nil
}

func (f *verifyFake) getProfile(project, name string) (*api.Profile, string, error) {
	k := project + "/" + name
	f.reads = append(f.reads, k)
	if err := f.profileErr[k]; err != nil {
		return nil, "", err
	}
	d, ok := f.devices[k]
	if !ok {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "profile not found")
	}
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: d}}, "", nil
}

func (f *verifyFake) GetProfile(name string) (*api.Profile, string, error) {
	return f.getProfile(hostProject, name)
}
func (p *projectView) GetProfile(name string) (*api.Profile, string, error) {
	return p.getProfile(p.project, name)
}

func (f *verifyFake) GetImageAlias(string) (*api.ImageAliasesEntry, string, error) {
	return nil, "", api.StatusErrorf(http.StatusNotFound, "alias not found")
}

func (f *verifyFake) GetImage(fp string) (*api.Image, string, error) {
	return &api.Image{Fingerprint: fp}, "", nil
}

// checkRootDisk is Incus's rule: the profiles' devices, overridden by name by the instance's own, hold one root disk.
func (f *verifyFake) checkRootDisk(project string, profiles []string, own api.DevicesMap) error {
	if len(profiles) == 0 {
		profiles = []string{"default"}
	}
	expanded := map[string]map[string]string{}
	for _, p := range profiles {
		d, ok := f.devices[project+"/"+p]
		if !ok {
			return api.StatusErrorf(http.StatusBadRequest, "Requested profile %q doesn't exist", p)
		}
		for n, dev := range d {
			expanded[n] = dev
		}
	}
	for n, dev := range own {
		expanded[n] = dev
	}
	roots := 0
	for _, d := range expanded {
		if d["type"] == "disk" && d["path"] == "/" && d["source"] == "" {
			roots++
		}
	}
	switch {
	case roots == 0:
		return api.StatusErrorf(http.StatusInternalServerError, "Failed instance creation: Failed creating instance record: Failed initializing instance: Failed getting root disk: No root device could be found")
	case roots > 1:
		return api.StatusErrorf(http.StatusInternalServerError, "More than one root device found")
	}
	return nil
}

func (f *verifyFake) create(project string, req api.InstancesPost) (incus.RemoteOperation, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	if err := f.checkRootDisk(project, req.Profiles, req.Devices); err != nil {
		return nil, err
	}
	m := &madeInstance{project: project, req: req, devices: api.DevicesMap{}, config: map[string]string{}, status: "Stopped"}
	for n, d := range req.Devices {
		m.devices[n] = d
	}
	f.instances[req.Name] = m
	f.created = append(f.created, *m)
	return remoteOp{}, nil
}

func (f *verifyFake) CreateInstanceFromImage(_ incus.ImageServer, _ api.Image, req api.InstancesPost) (incus.RemoteOperation, error) {
	return f.create(hostProject, req)
}
func (p *projectView) CreateInstanceFromImage(_ incus.ImageServer, _ api.Image, req api.InstancesPost) (incus.RemoteOperation, error) {
	return p.create(p.project, req)
}

// instanceIn finds a throwaway instance the way Incus does: in the project the handle is scoped to, and nowhere else.
func (f *verifyFake) instanceIn(project, name string) (*madeInstance, error) {
	m, ok := f.instances[name]
	if !ok || m.project != project {
		return nil, api.StatusErrorf(http.StatusNotFound, "instance not found")
	}
	return m, nil
}

func (f *verifyFake) getInstance(project, name string) (*api.Instance, string, error) {
	m, err := f.instanceIn(project, name)
	if err != nil {
		return nil, "", err
	}
	devices, config := api.DevicesMap{}, map[string]string{}
	for k, v := range m.devices {
		devices[k] = v
	}
	for k, v := range m.config {
		config[k] = v
	}
	return &api.Instance{Name: name, Status: m.status, InstancePut: api.InstancePut{Devices: devices, Config: config, Profiles: m.req.Profiles}}, "etag", nil
}

func (f *verifyFake) updateInstance(project, name string, put api.InstancePut) (incus.Operation, error) {
	m, err := f.instanceIn(project, name)
	if err != nil {
		return nil, err
	}
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	if err := f.checkRootDisk(m.project, m.req.Profiles, put.Devices); err != nil {
		return nil, err
	}
	m.devices, m.config = put.Devices, put.Config
	return localOp{}, nil
}

func (f *verifyFake) updateInstanceState(project, name string, put api.InstanceStatePut) (incus.Operation, error) {
	m, err := f.instanceIn(project, name)
	if err != nil {
		return nil, err
	}
	switch put.Action {
	case "start", "restart":
		m.status = "Running"
	case "stop":
		m.status = "Stopped"
	}
	return localOp{}, nil
}

func (f *verifyFake) deleteInstance(project, name string) (incus.Operation, error) {
	m, err := f.instanceIn(project, name)
	if err != nil {
		return nil, err
	}
	if f.deleteInstErr != nil {
		return nil, f.deleteInstErr
	}
	f.removed = append(f.removed, *m)
	delete(f.instances, name)
	return localOp{}, nil
}

type execOp struct {
	incus.Operation
	code int
}

func (execOp) Wait() error { return nil }
func (o execOp) Get() api.Operation {
	return api.Operation{Metadata: map[string]any{"return": float64(o.code)}}
}

func (f *verifyFake) execInstance(project, name string, post api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	if _, err := f.instanceIn(project, name); err != nil {
		return nil, err
	}
	f.commands = append(f.commands, post.Command)
	if f.execErr != nil {
		return nil, f.execErr
	}
	io.WriteString(args.Stdout, f.execOut)
	close(args.DataDone)
	return execOp{code: f.execCode}, nil
}

// The host's own handle is the daemon's default project; a handle from UseProject is that project.
func (f *verifyFake) GetInstance(name string) (*api.Instance, string, error) {
	return f.getInstance(hostProject, name)
}
func (p *projectView) GetInstance(name string) (*api.Instance, string, error) {
	return p.getInstance(p.project, name)
}
func (f *verifyFake) UpdateInstance(name string, put api.InstancePut, _ string) (incus.Operation, error) {
	return f.updateInstance(hostProject, name, put)
}
func (p *projectView) UpdateInstance(name string, put api.InstancePut, _ string) (incus.Operation, error) {
	return p.updateInstance(p.project, name, put)
}
func (f *verifyFake) UpdateInstanceState(name string, put api.InstanceStatePut, _ string) (incus.Operation, error) {
	return f.updateInstanceState(hostProject, name, put)
}
func (p *projectView) UpdateInstanceState(name string, put api.InstanceStatePut, _ string) (incus.Operation, error) {
	return p.updateInstanceState(p.project, name, put)
}
func (f *verifyFake) DeleteInstance(name string) (incus.Operation, error) {
	return f.deleteInstance(hostProject, name)
}
func (p *projectView) DeleteInstance(name string) (incus.Operation, error) {
	return p.deleteInstance(p.project, name)
}
func (f *verifyFake) ExecInstance(name string, post api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	return f.execInstance(hostProject, name, post, args)
}
func (p *projectView) ExecInstance(name string, post api.InstanceExecPost, args *incus.InstanceExecArgs) (incus.Operation, error) {
	return p.execInstance(p.project, name, post, args)
}

var (
	verifyAt = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	rootOn   = func(pool string) map[string]string {
		return map[string]string{"type": "disk", "path": "/", "pool": pool}
	}
)

func verifyNow() time.Time { return verifyAt }

func checkOf(mount string) *backupmeta.VerifyCheck {
	return &backupmeta.VerifyCheck{Image: "check-image", Command: []string{"test", "-s", "/data/db"}, Mount: mount}
}

// leftOver lists what a verification left on the server: instances, and volumes other than the source.
func (f *verifyFake) leftOver(pool string) []string {
	var left []string
	for n := range f.instances {
		left = append(left, "instance "+n)
	}
	for _, n := range f.names(pool) {
		if n != "lib" {
			left = append(left, "volume "+n)
		}
	}
	sort.Strings(left)
	return left
}

func TestVerifyGivesTheCheckInstanceARootDiskOnlyWhenItsProfileHasNone(t *testing.T) {
	const scratch = "lib-verify-20261013-120000"
	nic := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}}
	cases := []struct {
		name    string
		project string                       // the volume's project; empty is the daemon's default
		pool    string                       // the volume's pool
		mount   string                       // the check's mount; empty means the default
		profile map[string]map[string]string // the default profile of the volume's project (nil: empty)
		// wantRoot is the root disk the creation request must carry; nil means the request carries nothing.
		wantRoot map[string]string
		wantSay  bool // whether the progress says the instance got a root disk of its own
	}{
		{"a new project: its default profile is empty", "tink", "default", "", nil, rootOn("default"), true},
		{"the root disk is on the pool of the volume being checked", "tink", "nas2510", "", nil, rootOn("nas2510"), true},
		{"a default profile with only a NIC has no root disk", "tink", "default", "", nic, rootOn("default"), true},
		{"a default profile with a root disk is left to supply it", "tink", "default", "", map[string]map[string]string{"root": rootOn("default")}, nil, false},
		{"a root disk under another name, on another pool, still counts", "tink", "nas2510", "", map[string]map[string]string{"system": rootOn("fast")}, nil, false},
		{"a root disk among other devices", "tink", "default", "", map[string]map[string]string{"eth0": nic["eth0"], "root": rootOn("default")}, nil, false},
		{"a disk at / with a source is a mount, not a root disk", "tink", "default", "", map[string]map[string]string{"host": {"type": "disk", "path": "/", "source": "/srv"}}, rootOn("default"), true},
		{"the daemon's default project, whose default profile has a root disk", "", "default", "", map[string]map[string]string{"root": rootOn("default")}, nil, false},
		{"the daemon's default project with an empty default profile", "", "default", "", nil, rootOn("default"), true},
		{"a mount path the check chose", "tink", "default", "/restored", nil, rootOn("default"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir())
			f := newVerifyFake(tc.pool)
			if tc.project == "" {
				f.setProfile(hostProject, "default", tc.profile)
			} else {
				f.setProfile(tc.project, "default", tc.profile)
			}
			var say bytes.Buffer

			res, err := Verify(f, Volume{Project: tc.project, Pool: tc.pool, Name: "lib"}, VerifyOptions{Check: checkOf(tc.mount), Now: verifyNow, Progress: &say})
			if err != nil {
				t.Fatalf("a verification must not depend on the project's default profile having a root disk: %v", err)
			}
			if res.With != "check" || !res.Recorded {
				t.Errorf("the check ran and was recorded: %+v", res)
			}

			if len(f.created) != 1 {
				t.Fatalf("%d throwaway instances made, want 1", len(f.created))
			}
			made := f.created[0]
			wantProject := tc.project
			if wantProject == "" {
				wantProject = hostProject
			}
			if made.project != wantProject {
				t.Errorf("the instance was made in project %q, want the volume's, %q", made.project, wantProject)
			}
			if !reflect.DeepEqual(made.req.Profiles, []string{"default"}) {
				t.Errorf("profiles = %v: the check instance gets the bare default profile", made.req.Profiles)
			}
			var wantCreate api.DevicesMap
			if tc.wantRoot != nil {
				wantCreate = api.DevicesMap{"root": tc.wantRoot}
			}
			if !reflect.DeepEqual(made.req.Devices, wantCreate) {
				t.Errorf("the creation request's devices = %v, want %v", made.req.Devices, wantCreate)
			}

			// what the instance finally looked like, when it was removed: the scratch volume read-only at the mount, and nothing else but the root disk
			if len(f.removed) != 1 {
				t.Fatalf("%d instances removed, want 1", len(f.removed))
			}
			mount := tc.mount
			if mount == "" {
				mount = backupmeta.DefaultVerifyMount
			}
			want := api.DevicesMap{"data": {"type": "disk", "pool": tc.pool, "source": scratch, "path": mount, "readonly": "true"}}
			if tc.wantRoot != nil {
				want["root"] = tc.wantRoot
			}
			if got := f.removed[0].devices; !reflect.DeepEqual(got, want) {
				t.Errorf("the instance's devices = %v, want %v", got, want)
			}
			if got := f.removed[0].config["oci.entrypoint"]; got != "sleep 3600" {
				t.Errorf("oci.entrypoint = %q: the instance must stay alive to be exec'd into", got)
			}

			// the profile that decided it is the one in the volume's project
			if len(f.reads) != 1 || f.reads[0] != wantProject+"/default" {
				t.Errorf("profile reads = %v, want exactly %s/default", f.reads, wantProject)
			}
			if got := strings.Contains(say.String(), "has no root disk, so the check instance gets one on pool "+tc.pool); got != tc.wantSay {
				t.Errorf("progress says the instance got a root disk of its own = %v, want %v:\n%s", got, tc.wantSay, say.String())
			}

			// everything is cleaned up, and the passing result is on the source volume
			if left := f.leftOver(tc.pool); len(left) != 0 {
				t.Errorf("left behind: %v", left)
			}
			if got := f.vols[tc.pool+"/lib"].Config[backupmeta.StampVerifiedWith]; got != "check" {
				t.Errorf("verified-with = %q on the source volume", got)
			}
		})
	}
}

// The root disk is what the old code did not send, so the fake's refusal is the proof that this test is about the right thing.
func TestTheFakeRefusesAnInstanceWithNoRootDiskAsIncusDoes(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	f := newVerifyFake("default")
	f.setProfile("tink", "default", nil)
	_, err := f.UseProject("tink").CreateInstanceFromImage(nil, api.Image{}, api.InstancesPost{Name: "x", InstancePut: api.InstancePut{Profiles: []string{"default"}}})
	if err == nil || !strings.Contains(err.Error(), "No root device could be found") {
		t.Fatalf("err = %v", err)
	}
	_, err = f.UseProject("tink").CreateInstanceFromImage(nil, api.Image{}, api.InstancesPost{Name: "x", InstancePut: api.InstancePut{Profiles: []string{"default"}, Devices: api.DevicesMap{"root": rootOn("default")}}})
	if err != nil {
		t.Fatalf("a root disk in the request is enough: %v", err)
	}
}

func TestVerifyCleansUpOnEveryFailure(t *testing.T) {
	cases := []struct {
		name       string
		fail       func(f *verifyFake)
		wantErr    string
		wantMade   int // instances that were made before it failed, all of which must be removed
		wantReads  int // reads of the default profile
		wantChecks int // times the check command ran
	}{
		{"the check fails", func(f *verifyFake) { f.execCode, f.execOut = 3, "no such file" }, "verification check failed with exit status 3", 1, 1, 1},
		{"the check cannot be started", func(f *verifyFake) { f.execErr = errors.New("agent gone") }, "running the check", 1, 1, 1},
		{"Incus refuses to make the instance", func(f *verifyFake) { f.createErr = errors.New("storage pool is full") }, "storage pool is full", 0, 1, 0},
		{"Incus refuses the scratch volume's disk after the instance exists", func(f *verifyFake) { f.updateErr = errors.New("volume busy") }, "volume busy", 1, 1, 0},
		{"the default profile cannot be read", func(f *verifyFake) { f.profileErr["tink/default"] = forbidden }, `reading the "default" profile of project tink`, 0, 1, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INCUS_CONF", t.TempDir())
			f := newVerifyFake("default")
			f.setProfile("tink", "default", nil)
			tc.fail(f)

			_, err := Verify(f, Volume{Project: "tink", Name: "lib"}, VerifyOptions{Check: checkOf(""), Now: verifyNow})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "cleanup incomplete") {
				t.Errorf("the cleanup must have worked: %v", err)
			}
			// the instance (if one was made) and the scratch volume are gone; the source is untouched and not stamped
			if left := f.leftOver("default"); len(left) != 0 {
				t.Errorf("left behind after a failure: %v", left)
			}
			if len(f.removed) != tc.wantMade {
				t.Errorf("%d instances removed, want %d", len(f.removed), tc.wantMade)
			}
			if len(f.reads) != tc.wantReads {
				t.Errorf("%d profile reads, want %d", len(f.reads), tc.wantReads)
			}
			if len(f.commands) != tc.wantChecks {
				t.Errorf("the check ran %d times, want %d", len(f.commands), tc.wantChecks)
			}
			if got := f.vols["default/lib"].Config[backupmeta.StampVerifiedAt]; got != "" {
				t.Errorf("a failed verification must not look fresh, but verified-at = %q", got)
			}
			if got := fmt.Sprint(f.deletedVols); got != "[default/lib-verify-20261013-120000]" {
				t.Errorf("deleted volumes = %s, want just the scratch volume", got)
			}
		})
	}
}

func TestAFailedCheckStillSaysWhatItPrinted(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir())
	f := newVerifyFake("default")
	f.setProfile("tink", "default", nil)
	f.execCode, f.execOut = 1, "db is empty\n"

	_, err := Verify(f, Volume{Project: "tink", Name: "lib"}, VerifyOptions{Check: checkOf(""), Now: verifyNow})
	var failed *CheckFailedError
	if !errors.As(err, &failed) || failed.ExitCode != 1 || failed.Output != "db is empty\n" {
		t.Fatalf("err = %#v", err)
	}
}

// A verification with no declared check only proves the restore: it makes no instance, so it has no use for the profile and must not need to read it.
func TestVerifyWithoutACheckMakesNoInstanceAndReadsNoProfile(t *testing.T) {
	f := newVerifyFake("default")
	f.profileErr[hostProject+"/default"] = forbidden
	f.profileErr["tink/default"] = forbidden

	res, err := Verify(f, Volume{Project: "tink", Name: "lib"}, VerifyOptions{Now: verifyNow})
	if err != nil || res.With != "restore" || !res.Recorded {
		t.Fatalf("%+v %v", res, err)
	}
	if len(f.created) != 0 || len(f.reads) != 0 {
		t.Errorf("made %d instances and read %v: a restore-only verification needs neither", len(f.created), f.reads)
	}
	if left := f.leftOver("default"); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}
