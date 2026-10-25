package helper

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/minihci/tink/internal/instance"
)

func TestParseStatusVolume(t *testing.T) {
	for in, want := range map[string][2]string{
		"default/helper-status": {"default", "helper-status"},
		"fast/x":                {"fast", "x"},
	} {
		if pool, vol, err := ParseStatusVolume(in); err != nil || pool != want[0] || vol != want[1] {
			t.Errorf("%q -> %q %q %v", in, pool, vol, err)
		}
	}
	for _, bad := range []string{"", "default", "/x", "default/", "a/b/c"} {
		if _, _, err := ParseStatusVolume(bad); err == nil {
			t.Errorf("%q is not POOL/VOLUME", bad)
		}
	}
	if StatusVolumeRef("fast", StatusVolumeName("helper")) != "fast/helper-status" {
		t.Error("the pointer is POOL/<instance>-status")
	}
	if StatusVolumeKey != "user.tink.helper.status-volume" {
		t.Errorf("the pointer key sits beside the marker: %s", StatusVolumeKey)
	}
}

// volServer lists instances and reads volumes: just what Find needs. A call it should not make panics through the nil embedded
// interface; reads counts the volume reads.
type volServer struct {
	incus.InstanceServer
	all     []api.Instance
	vols    map[string]map[string]string // "project/pool/volume" -> config
	errs    map[string]error
	project string
	reads   *[]string
}

func (s volServer) GetInstancesAllProjects(api.InstanceType) ([]api.Instance, error) {
	return s.all, nil
}

func (s volServer) UseProject(p string) incus.InstanceServer { s.project = p; return s }

func (s volServer) GetStoragePoolVolume(pool, volType, name string) (*api.StorageVolume, string, error) {
	key := s.project + "/" + pool + "/" + name
	if volType != "custom" {
		return nil, "", errors.New("not a custom volume")
	}
	*s.reads = append(*s.reads, key)
	if err := s.errs[key]; err != nil {
		return nil, "", err
	}
	cfg, ok := s.vols[key]
	if !ok {
		return nil, "", notFound
	}
	vol := &api.StorageVolume{Name: name}
	vol.Config = cfg
	return vol, "", nil
}

func helperAt(project, name, state string, cfg map[string]string) api.Instance {
	return api.Instance{Name: name, Project: project, Status: state, InstancePut: api.InstancePut{Config: cfg}}
}

func TestFindReadsTheStatusFromTheVolumeTheInstancePointsAt(t *testing.T) {
	onVolume := statusText(t, func(s *Status) { s.Version = "from-the-volume" })
	onInstance := statusText(t, func(s *Status) { s.Version = "from-the-instance" })
	cfg := map[string]string{MarkerKey: "1", StatusVolumeKey: "fast/helper-status", StatusKey: onInstance}
	var reads []string
	s := volServer{
		all:   []api.Instance{helperAt("tink-helper", "helper", "Running", cfg)},
		vols:  map[string]map[string]string{"tink-helper/fast/helper-status": {StatusKey: onVolume, "user.other": "x"}},
		reads: &reads,
	}
	got, err := Find(s)
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	f := got[0]
	if f.Config[StatusKey] != onVolume {
		t.Errorf("the document is the volume's, not a stale one left on the instance: %q", f.Config[StatusKey])
	}
	if f.StatusVolume != "fast/helper-status" || f.StatusProblem != "" {
		t.Errorf("%+v", f)
	}
	if len(reads) != 1 || reads[0] != "tink-helper/fast/helper-status" {
		t.Errorf("the volume is read in the instance's own project, in the pointer's pool: %v", reads)
	}
	if cfg[StatusKey] != onInstance {
		t.Error("the instance's config as listed is not changed under the caller")
	}
	if r := Evaluate(f, t0); r.Health != Healthy || r.Status == nil || r.Status.Version != "from-the-volume" {
		t.Errorf("Evaluate sees it as it always did: %s %+v", r.Summary(), r.Status)
	}
}

func TestAStatusVolumeThatCannotBeReadDegradesTheHelperAndNothingElse(t *testing.T) {
	stale := statusText(t, nil) // a perfectly fresh document left on the instance: it must not stand in for the volume's
	denied := api.StatusErrorf(http.StatusForbidden, "not authorized")
	tests := []struct {
		name  string
		ref   string
		err   error
		state string
		want  Health
		text  string
	}{
		{"refused", "default/helper-status", denied, "Running", Degraded, "cannot read its status volume default/helper-status: not authorized"},
		{"gone", "default/helper-status", nil, "Running", Degraded, "cannot read its status volume default/helper-status: not found"},
		{"a pointer that is not POOL/VOLUME", "nonsense", nil, "Running", Degraded, `cannot read its status volume nonsense: "nonsense" is not POOL/VOLUME`},
		{"the instance is stopped too", "default/helper-status", denied, "Stopped", Down, "cannot read its status volume"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var reads []string
			s := volServer{
				all: []api.Instance{
					helperAt("tink-helper", "helper", tc.state, map[string]string{MarkerKey: "1", StatusVolumeKey: tc.ref, StatusKey: stale}),
					helperAt("elsewhere", "other", "Running", map[string]string{MarkerKey: "1", StatusKey: stale}),
				},
				errs:  map[string]error{"tink-helper/default/helper-status": tc.err},
				reads: &reads,
			}
			got, err := Find(s)
			if err != nil {
				t.Fatalf("a volume that cannot be read must not fail the listing: %v", err)
			}
			if len(got) != 2 {
				t.Fatalf("both helpers are still listed: %+v", got)
			}
			bad := got[1] // "tink-helper/helper" sorts after "elsewhere/other"
			if bad.Name != "helper" || bad.StatusProblem == "" {
				t.Fatalf("%+v", bad)
			}
			r := Evaluate(bad, t0)
			if r.Health != tc.want {
				t.Fatalf("health = %s (%v), want %s: unreadable is never healthy", r.Health, r.Reasons, tc.want)
			}
			if !strings.Contains(r.Summary(), tc.text) {
				t.Errorf("summary = %q, want it to contain %q", r.Summary(), tc.text)
			}
			if r.Status != nil {
				t.Errorf("the stale document on the instance is not used: %+v", r.Status)
			}
			if tc.state == "Running" && strings.Contains(r.Summary(), "has not published") {
				t.Errorf("an unreadable volume is not the same as no status yet: %s", r.Summary())
			}
			// the other helper is untouched by it
			if o := Evaluate(got[0], t0); o.Health != Healthy {
				t.Errorf("a helper with no pointer is judged as before: %s", o.Summary())
			}
		})
	}
}

func TestAHelperWithAPointerAndNoDocumentYetIsJustNewNotBroken(t *testing.T) {
	var reads []string
	s := volServer{
		all:   []api.Instance{helperAt("tink-helper", "helper", "Running", map[string]string{MarkerKey: "1", StatusVolumeKey: "default/helper-status"})},
		vols:  map[string]map[string]string{"tink-helper/default/helper-status": {}},
		reads: &reads,
	}
	got, _ := Find(s)
	r := Evaluate(got[0], t0)
	if got[0].StatusProblem != "" || r.Health != Degraded || !strings.Contains(r.Summary(), "has not published") || strings.Contains(r.Summary(), "cannot read") {
		t.Errorf("a readable volume with nothing in it is a helper that has not spoken yet: %s", r.Summary())
	}
}

func TestAHelperWithNoPointerIsReadExactlyAsBeforeAndNoVolumeIsTouched(t *testing.T) {
	doc := statusText(t, nil)
	// the embedded server is nil: any volume read, or a project switch, panics
	got, err := Find(listServer{all: []api.Instance{helperAt("tink-helper", "helper", "Running", map[string]string{MarkerKey: "1", StatusKey: doc})}})
	if err != nil || len(got) != 1 {
		t.Fatalf("%v %v", got, err)
	}
	if got[0].Config[StatusKey] != doc || got[0].StatusVolume != "" || got[0].StatusProblem != "" {
		t.Errorf("%+v", got[0])
	}
	if r := Evaluate(got[0], t0); r.Health != Healthy {
		t.Errorf("%s", r.Summary())
	}
}

func TestRemotesSaysWhyWhenTheStatusVolumeCannotBeReadInsteadOfNotReportedYet(t *testing.T) {
	h := newHost()
	helperRunning(h)
	h.instances["helper"].Config[StatusVolumeKey] = "fast/helper-status"
	h.volErr["fast/helper-status"] = api.StatusErrorf(http.StatusForbidden, "not authorized")
	in, _ := installer(h, new([]*instance.Spec))
	_, _, _, err := in.Remotes("", "", t0)
	if err == nil || !strings.Contains(err.Error(), "cannot read its status volume fast/helper-status") || strings.Contains(err.Error(), "has not reported") {
		t.Errorf("err = %v", err)
	}
	// and when it is readable, the remotes come from the volume
	delete(h.volErr, "fast/helper-status")
	h.volumes["fast/helper-status"] = true
	h.volCfg["fast/helper-status"] = map[string]string{StatusKey: statusText(t, func(s *Status) { s.Remotes = []Remote{{Name: "host"}} })}
	list, _, _, err := in.Remotes("", "", t0)
	if err != nil || len(list) != 1 || list[0].Name != "host" {
		t.Errorf("%v %v", list, err)
	}
}

func TestThePublisherLeavesTheMarkerOutWhenTheDocumentIsOnAVolume(t *testing.T) {
	r := newRecorder()
	r.p.NoMarker = true
	r.p.Publish(Status{JobProto: 1})
	if _, has := r.writes[0][MarkerKey]; has || r.writes[0][StatusKey] == "" || len(r.writes[0]) != 1 {
		t.Errorf("only the document: the helper holds no right to write its instance, and the marker is on the instance already: %v", r.writes[0])
	}
}

// --- install

func TestInstallMakesTheStatusVolumeAndPointsTheInstanceAtItBeforeTheInstanceExists(t *testing.T) {
	h := newHost()
	var specs []*instance.Spec
	in, out := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	post, ok := h.volPosts["default/helper-status"]
	if !ok || !h.volumes["default/helper-status"] {
		t.Fatalf("the status volume is made: %v", h.volumes)
	}
	if post.Type != "custom" || post.ContentType != "filesystem" || len(post.Config) != 0 {
		t.Errorf("a filesystem volume with no config of its own, and so no backup policy: %+v", post)
	}
	s := specs[0]
	if s.Config[StatusVolumeKey] != "default/helper-status" || s.Config[MarkerKey] != "1" {
		t.Errorf("the pointer sits beside the marker: %v", s.Config)
	}
	entry := s.Config["oci.entrypoint"]
	for _, want := range []string{"--status-volume helper-status", "--status-pool default", "--status-project tink-helper"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entrypoint must contain %q: %s", want, entry)
		}
	}
	if strings.Contains(entry, "--status-instance") {
		t.Errorf("%s", entry)
	}
	if vol, inst := indexOf(h.log, "create volume helper-status"), indexOf(h.log, "create helper from x"); vol < 0 || inst < 0 || vol > inst {
		t.Errorf("a pointer to a volume that is not there yet is never left behind by a failed install: the volume first. %v", h.log)
	}
	if got := h.instances["helper"].Config[StatusVolumeKey]; got != "default/helper-status" {
		t.Errorf("and it is on the instance as configured: %q", got)
	}
	if !strings.Contains(out.String(), "creating the status volume default/helper-status") {
		t.Errorf("%s", out.String())
	}
}

func TestTheStatusVolumeFollowsThePoolAndTheName(t *testing.T) {
	h := newHost()
	var specs []*instance.Spec
	in, _ := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", Name: "h2", Pool: "fast", Project: "ops", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if !h.volumes["fast/h2-status"] || specs[0].Config[StatusVolumeKey] != "fast/h2-status" {
		t.Errorf("%v %v", h.volumes, specs[0].Config)
	}
	entry := specs[0].Config["oci.entrypoint"]
	for _, want := range []string{"--status-volume h2-status", "--status-pool fast", "--status-project ops"} {
		if !strings.Contains(entry, want) {
			t.Errorf("entrypoint must contain %q: %s", want, entry)
		}
	}
}

func TestStatusOnInstanceInstallsTheOldShape(t *testing.T) {
	h := newHost()
	var specs []*instance.Spec
	in, _ := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", StatusOnInstance: true, Wait: -1}); err != nil {
		t.Fatal(err)
	}
	entry := specs[0].Config["oci.entrypoint"]
	if !strings.Contains(entry, "--status-instance helper --status-project tink-helper") || strings.Contains(entry, "--status-volume") || strings.Contains(entry, "--status-pool") {
		t.Errorf("%s", entry)
	}
	if _, has := specs[0].Config[StatusVolumeKey]; has {
		t.Errorf("no pointer: %v", specs[0].Config)
	}
	if h.volumes["default/helper-status"] {
		t.Errorf("and no volume: %v", h.volumes)
	}
}

func TestInstallingAgainDoesNotMakeASecondStatusVolumeOrMigrateAnOldHelper(t *testing.T) {
	h := newHost()
	var specs []*instance.Spec
	in, _ := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	h.log = nil
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if contains(h.log, "create volume helper-status") || len(specs) != 1 {
		t.Errorf("what exists is left alone: %v", h.log)
	}

	// a volume that was deleted is made again, because the instance still points at it
	delete(h.volumes, "default/helper-status")
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if !h.volumes["default/helper-status"] {
		t.Errorf("an instance that points at a volume gets it back: %v", h.volumes)
	}

	// a helper installed before the status volume keeps publishing on itself: nothing is migrated
	h = newHost()
	in, out := installer(h, &specs)
	if err := in.Install(InstallOptions{Image: "x", StatusOnInstance: true, Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if err := in.Install(InstallOptions{Image: "x", Wait: -1}); err != nil {
		t.Fatal(err)
	}
	if h.volumes["default/helper-status"] {
		t.Errorf("no volume for a helper that has none: %v", h.volumes)
	}
	if _, has := h.instances["helper"].Config[StatusVolumeKey]; has {
		t.Error("and no pointer added behind its back")
	}
	if !strings.Contains(out.String(), "nothing is migrated") {
		t.Errorf("it says so:\n%s", out.String())
	}
}

func TestInstallSaysSoWhenItCannotMakeTheStatusVolume(t *testing.T) {
	h := newHost()
	h.volErr["default/helper-status"] = api.StatusErrorf(http.StatusForbidden, "not authorized")
	var specs []*instance.Spec
	in, _ := installer(h, &specs)
	err := in.Install(InstallOptions{Image: "x", Wait: -1})
	if err == nil || !strings.Contains(err.Error(), "status volume default/helper-status") {
		t.Errorf("err = %v", err)
	}
	if len(specs) != 0 {
		t.Error("the instance is not made with a pointer to a volume that could not be")
	}
}

// --- remove

func helperWithStatusVolume(h *fakeHost) {
	helperRunning(h) // pool "fast"
	h.instances["helper"].Config[StatusVolumeKey] = "fast/helper-status"
	h.volumes["fast/helper-status"] = true
}

func TestRemovePurgeAlsoDeletesTheStatusVolumeFoundThroughThePointer(t *testing.T) {
	h := newHost()
	helperWithStatusVolume(h)
	in, out := installer(h, new([]*instance.Spec))
	if err := in.Remove(RemoveOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	if len(h.volumes) != 0 || h.projects["tink-helper"] {
		t.Errorf("all three volumes and then the project (which cannot go while a volume is in it): %v %v", h.volumes, h.projects)
	}
	if !contains(h.log, "delete volume helper-status") || !strings.Contains(out.String(), "deleted volume helper-status") {
		t.Errorf("%v\n%s", h.log, out.String())
	}
}

func TestRemoveWithoutPurgeKeepsTheStatusVolumeAndSaysSo(t *testing.T) {
	h := newHost()
	helperWithStatusVolume(h)
	in, out := installer(h, new([]*instance.Spec))
	if err := in.Remove(RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if !h.volumes["fast/helper-status"] || contains(h.log, "delete volume helper-status") {
		t.Errorf("plain remove leaves volumes: %v", h.log)
	}
	if !strings.Contains(out.String(), "kept the status volume fast/helper-status") {
		t.Errorf("%s", out.String())
	}
}

func TestRemovePurgeDoesNotDeleteAVolumeThePointerWasEditedToName(t *testing.T) {
	h := newHost()
	helperRunning(h)
	h.instances["helper"].Config[StatusVolumeKey] = "fast/precious-data"
	h.volumes["fast/precious-data"] = true
	in, out := installer(h, new([]*instance.Spec))
	if err := in.Remove(RemoveOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
	if !h.volumes["fast/precious-data"] || contains(h.log, "delete volume precious-data") {
		t.Errorf("a pointer is not a licence to delete whatever it names: %v", h.log)
	}
	if !strings.Contains(out.String(), "kept volume fast/precious-data") {
		t.Errorf("%s", out.String())
	}

	// nor one that is not a pointer at all
	h = newHost()
	helperRunning(h)
	h.instances["helper"].Config[StatusVolumeKey] = "garbage"
	in, _ = installer(h, new([]*instance.Spec))
	if err := in.Remove(RemoveOptions{Purge: true}); err != nil {
		t.Fatal(err)
	}
}

func TestRemovePurgeOfAStatusVolumeThatIsAlreadyGoneIsNotAnError(t *testing.T) {
	h := newHost()
	helperRunning(h)
	h.instances["helper"].Config[StatusVolumeKey] = "fast/helper-status" // not in h.volumes
	in, _ := installer(h, new([]*instance.Spec))
	if err := in.Remove(RemoveOptions{Purge: true}); err != nil {
		t.Fatalf("%v", err)
	}
}

// --- upgrade

// upgradeFixtureOnVolume is the upgrade fixture for a helper that publishes on a status volume.
func upgradeFixtureOnVolume(h *fakeHost) {
	upgradeFixture(h)
	cfg := h.instances["helper"].Config
	cfg[StatusVolumeKey] = "default/helper-status"
	cfg["oci.entrypoint"] = "/usr/local/bin/tink daemon run --remote host --jobs /data/jobs --status-volume helper-status --status-pool default --status-project tink-helper"
	h.volumes["default/helper-status"] = true
}

func TestAnImageUpgradeOfAHelperOnAStatusVolumeKeepsThePointerTheEntrypointAndTheVolume(t *testing.T) {
	h := newHost()
	upgradeFixtureOnVolume(h)
	var specs []*instance.Spec
	in, out := installer(h, &specs)
	base := in.Create
	in.Create = func(s incus.InstanceServer, spec *instance.Spec, project string) error {
		if err := base(s, spec, project); err != nil {
			return err
		}
		h.instances[spec.Name].Config = map[string]string{"volatile.base_image": "sha256:new"}
		return nil
	}
	clocked(in, func(n int, now time.Time) {
		if n == 1 {
			publish(h, now, func(s *Status) { s.Draining = true }) // read from the volume
		}
		if indexOf(h.log, "start helper") > 0 {
			publish(h, now, nil)
		}
	})
	if err := in.Upgrade(UpgradeOptions{Image: "ghcr:minihci/tink-helper:v1.1.0", Wait: time.Minute}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := h.instances["helper"]
	if got.Config[StatusVolumeKey] != "default/helper-status" || !strings.Contains(got.Config["oci.entrypoint"], "--status-volume helper-status") {
		t.Errorf("the recreated instance still points at its status volume, and still publishes there: %v", got.Config)
	}
	if !h.volumes["default/helper-status"] || contains(h.log, "delete volume helper-status") || contains(h.log, "create volume helper-status") {
		t.Errorf("the status volume is never touched by an upgrade: %v", h.log)
	}
	if _, has := got.Config[StatusKey]; has {
		t.Errorf("and nothing is written to the instance's own config: %v", got.Config)
	}
	if !strings.Contains(out.String(), "healthy: tink-helper/helper") || !strings.Contains(out.String(), "drained: nothing is running") {
		t.Errorf("the drain and the report both went through the volume:\n%s", out.String())
	}
}

func TestAnImageUpgradeOfAHelperOnItsOwnInstanceStillWorksAndGainsNoPointer(t *testing.T) {
	h := newHost()
	upgradeFixture(h)
	h.instances["helper"].Config["oci.entrypoint"] += " --status-instance helper --status-project tink-helper"
	in, out := installer(h, new([]*instance.Spec))
	clocked(in, func(n int, now time.Time) {
		if n == 1 {
			publish(h, now, func(s *Status) { s.Draining = true })
		}
		if indexOf(h.log, "start helper") > 0 {
			publish(h, now, nil)
		}
	})
	if err := in.Upgrade(UpgradeOptions{Image: "ghcr:minihci/tink-helper:v1.1.0", Wait: time.Minute}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := h.instances["helper"]
	if _, has := got.Config[StatusVolumeKey]; has || !strings.Contains(got.Config["oci.entrypoint"], "--status-instance helper") {
		t.Errorf("no automatic migration: %v", got.Config)
	}
	if len(h.volumes) != 2 {
		t.Errorf("and no volume appears: %v", h.volumes)
	}
}
