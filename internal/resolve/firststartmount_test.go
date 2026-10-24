package resolve

import (
	"errors"
	"io"
	"net/http"
	"path"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func disk(path, source, pool string) map[string]string {
	d := map[string]string{"type": "disk", "path": path}
	if source != "" {
		d["source"] = source
	}
	if pool != "" {
		d["pool"] = pool
	}
	return d
}

func volumeDisk(path, source string) map[string]string { return disk(path, source, "default") }
func rootDisk() map[string]string                      { return disk("/", "", "default") }

func fileAt(name, p string, dependsOn ...string) Resource {
	return Resource{Kind: KindFile, Name: name, Instance: "ha", Project: "p", Path: p, Content: name, DependsOn: dependsOn}
}

func instanceWith(devices map[string]map[string]string, profiles ...string) Resource {
	return Resource{Kind: KindInstance, Name: "ha", Project: "p", Devices: devices, Profiles: profiles}
}

// placementFor runs the whole decision for the instance "ha" in a stack, without a server: which files are candidates (firstStartFiles), then where each
// goes (placeBy), with the profiles the stack declares as the only ones that can be read.
func placementFor(rs []Resource) placement {
	var inst Resource
	for _, r := range rs {
		if r.Kind == KindInstance && r.Name == "ha" {
			inst = r
		}
	}
	return placeBy(inst, firstStartFiles(rs)[instanceKey(inst.Project, inst.Name)], profileLookup(nil, declaredProfiles(rs)))
}

// show reduces a placement to what a test compares: the files that go into the instance by name, those that go into a volume as name->pool/volume:path,
// then the skipped as skip:name.
func show(pl placement) []string {
	out := []string{}
	for _, pa := range pl.Place {
		if pa.Mount == nil {
			out = append(out, pa.File.Name)
		} else {
			out = append(out, pa.File.Name+"->"+pa.Mount.Pool+"/"+pa.Mount.Volume+":"+pa.Mount.inVolume(path.Clean(pa.File.Path)))
		}
	}
	for _, s := range pl.Skipped {
		out = append(out, "skip:"+s.File.Name)
	}
	return out
}

func TestPlaceBy_FilesUnderADiskMount(t *testing.T) {
	haVolume := map[string]map[string]string{"root": rootDisk(), "config": volumeDisk("/config", "e2e-ha-config")}
	cases := []struct {
		name string
		rs   []Resource
		want []string
	}{
		{"a file under a volume mount goes into the volume, not the rootfs",
			[]Resource{instanceWith(haVolume), fileAt("ha-yaml", "/config/configuration.yaml")},
			[]string{"ha-yaml->default/e2e-ha-config:/configuration.yaml"}},
		{"a file deeper under the mount keeps its directories",
			[]Resource{instanceWith(haVolume), fileAt("f", "/config/packages/x.yaml")},
			[]string{"f->default/e2e-ha-config:/packages/x.yaml"}},
		{"the mount path itself is skipped, even for a volume",
			[]Resource{instanceWith(haVolume), fileAt("f", "/config")},
			[]string{"skip:f"}},
		{"a Caddyfile next to a /config mount is still pushed into the instance, in declaration order",
			[]Resource{instanceWith(haVolume), fileAt("ha-yaml", "/config/configuration.yaml"), fileAt("caddy", "/etc/caddy/Caddyfile")},
			[]string{"ha-yaml->default/e2e-ha-config:/configuration.yaml", "caddy"}},
		{"the root disk does not count as a mount",
			[]Resource{instanceWith(map[string]map[string]string{"root": rootDisk()}), fileAt("f", "/etc/x"), fileAt("g", "/x")},
			[]string{"f", "g"}},
		{"the root disk with a source-less path of / hides nothing even when written /./",
			[]Resource{instanceWith(map[string]map[string]string{"root": disk("/./", "", "default")}), fileAt("f", "/etc/x")},
			[]string{"f"}},
		{"a host directory is skipped",
			[]Resource{instanceWith(map[string]map[string]string{"media": disk("/media", "/srv/media", "")}), fileAt("f", "/media/list.txt"), fileAt("g", "/etc/x")},
			[]string{"g", "skip:f"}},
		{"a similarly named path is not under the mount: /configuration is not under /config",
			[]Resource{instanceWith(haVolume), fileAt("f", "/configuration"), fileAt("g", "/configuration/x.yaml"), fileAt("h", "/config.yaml")},
			[]string{"f", "g", "h"}},
		{"a path is cleaned before it is compared",
			[]Resource{instanceWith(haVolume), fileAt("in", "/config//sub/../x.yaml"), fileAt("out", "/config/../etc/x")},
			[]string{"in->default/e2e-ha-config:/x.yaml", "out"}},
		{"a mount path with a trailing slash still holds its files",
			[]Resource{instanceWith(map[string]map[string]string{"config": volumeDisk("/config/", "v")}), fileAt("f", "/config/x")},
			[]string{"f->default/v:/x"}},
		{"nested mounts: the deepest one decides",
			[]Resource{instanceWith(map[string]map[string]string{"config": volumeDisk("/config", "v"), "media": disk("/config/media", "/srv/media", "")}),
				fileAt("a", "/config/media/list.txt"), fileAt("b", "/config/x")},
			[]string{"b->default/v:/x", "skip:a"}},
		{"a device that is not a disk does not count, whatever its path",
			[]Resource{instanceWith(map[string]map[string]string{"gpu": {"type": "unix-char", "path": "/dev/dri"}}), fileAt("f", "/dev/dri/x")},
			[]string{"f"}},
		{"a disk with no path is not a mount",
			[]Resource{instanceWith(map[string]map[string]string{"iso": {"type": "disk", "source": "/srv/x.iso"}}), fileAt("f", "/x")},
			[]string{"f"}},
		{"a disk whose source is neither a plain volume nor a host path is a mount, and skipped",
			[]Resource{instanceWith(map[string]map[string]string{"d": disk("/data", "pool/vol/snap", "default"), "e": {"type": "disk", "path": "/other", "source": "vol"}}),
				fileAt("a", "/data/x"), fileAt("b", "/other/x")},
			[]string{"skip:a", "skip:b"}},
		{"a virtual machine is not placed before its start, mount or not",
			[]Resource{{Kind: KindInstance, Name: "ha", Project: "p", VM: true, Devices: haVolume}, fileAt("f", "/config/x"), fileAt("g", "/etc/x")},
			[]string{}},
		{"a file that waits for an exec is left to its own turn, mount or not",
			[]Resource{instanceWith(haVolume), {Kind: KindExec, Name: "prepare", Instance: "ha", Project: "p"},
				fileAt("after-exec", "/config/x", "ha", "prepare"), fileAt("plain", "/etc/x", "ha", "prepare"), fileAt("fine", "/config/y", "ha")},
			[]string{"fine->default/e2e-ha-config:/y"}},
		{"a file that waits for a skipped file waits for the start too, and is not itself reported",
			[]Resource{instanceWith(map[string]map[string]string{"media": disk("/media", "/srv/media", "")}),
				fileAt("hidden", "/media/a"), fileAt("later", "/etc/b", "hidden"), fileAt("muchlater", "/etc/c", "later"), fileAt("free", "/etc/d")},
			[]string{"free", "skip:hidden"}},
		{"a file that waits for a file that goes into a volume is placed",
			[]Resource{instanceWith(haVolume), fileAt("cfg", "/config/x"), fileAt("after", "/etc/y", "cfg")},
			[]string{"cfg->default/e2e-ha-config:/x", "after"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := show(placementFor(tc.rs)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

func TestPlaceBy_ProfileDevices(t *testing.T) {
	storage := Resource{Kind: KindProfile, Name: "storage", Devices: map[string]map[string]string{"config": volumeDisk("/config", "shared-config")}}
	cases := []struct {
		name string
		rs   []Resource
		want []string
	}{
		{"a disk from a profile the stack declares counts",
			[]Resource{storage, instanceWith(nil, "storage"), fileAt("f", "/config/x")},
			[]string{"f->default/shared-config:/x"}},
		{"a profile the stack does not declare cannot be read, so nothing is skipped",
			[]Resource{instanceWith(nil, "storage"), fileAt("f", "/config/x")},
			[]string{"f"}},
		{"an instance's device of the same name replaces the profile's whole device",
			[]Resource{storage, instanceWith(map[string]map[string]string{"config": volumeDisk("/elsewhere", "other")}, "storage"), fileAt("f", "/config/x"), fileAt("g", "/elsewhere/y")},
			[]string{"f", "g->default/other:/y"}},
		{"a later profile replaces an earlier one's device of the same name",
			[]Resource{storage, {Kind: KindProfile, Name: "moved", Devices: map[string]map[string]string{"config": volumeDisk("/moved", "m")}}, instanceWith(nil, "storage", "moved"), fileAt("f", "/config/x"), fileAt("g", "/moved/y")},
			[]string{"f", "g->default/m:/y"}},
		{"a profile's root disk is no mount",
			[]Resource{{Kind: KindProfile, Name: "base", Devices: map[string]map[string]string{"root": rootDisk()}}, instanceWith(nil, "base"), fileAt("f", "/etc/x")},
			[]string{"f"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := show(placementFor(tc.rs)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %v\nwant %v", got, tc.want)
			}
		})
	}
}

// The root disk is a disk device with a path, but it mounts nothing over the instance's filesystem: it is the filesystem.
func TestMountsOf_TheRootDiskIsNotAMount(t *testing.T) {
	inst := instanceWith(map[string]map[string]string{"root": rootDisk(), "also": disk("/", "", "other"), "config": volumeDisk("/config", "v")})
	got := mountsOf(inst, profileLookup(nil, nil))
	if len(got) != 1 || got[0].Device != "config" {
		t.Errorf("mounts = %+v, want only the /config volume", got)
	}
}

// Incus gives an instance that names no profile its "default".
func TestMountsOf_AnInstanceThatNamesNoProfileGetsDefault(t *testing.T) {
	asked := []string{}
	lookup := func(name string) (map[string]map[string]string, bool) {
		asked = append(asked, name)
		return map[string]map[string]string{"data": volumeDisk("/data", "d")}, true
	}
	got := mountsOf(Resource{Kind: KindInstance, Name: "x"}, lookup)
	if !reflect.DeepEqual(asked, []string{"default"}) || len(got) != 1 || got[0].Path != "/data" || got[0].Volume != "d" || got[0].Pool != "default" || got[0].Device != "data" {
		t.Errorf("asked %v, mounts %+v", asked, got)
	}
}

// mountProfileServer answers GetProfile from a table and counts the asks; any other call panics (nil embedded interface).
type mountProfileServer struct {
	incus.InstanceServer
	profiles map[string]map[string]map[string]string
	err      error
	asked    []string
}

func (s *mountProfileServer) GetProfile(name string) (*api.Profile, string, error) {
	s.asked = append(s.asked, name)
	if s.err != nil {
		return nil, "", s.err
	}
	devs, ok := s.profiles[name]
	if !ok {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "Profile not found")
	}
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: devs}}, "", nil
}

func TestProfileLookup(t *testing.T) {
	declared := map[string]Resource{"stack": {Kind: KindProfile, Name: "stack", Devices: map[string]map[string]string{"a": volumeDisk("/a", "va")}}}
	onServer := map[string]map[string]map[string]string{
		"live":  {"b": volumeDisk("/b", "vb")},
		"stack": {"c": volumeDisk("/c", "vc")}, // the stack's profile wins over what the server holds today
	}
	cases := []struct {
		name       string
		server     *mountProfileServer
		profile    string
		wantOK     bool
		wantDevice string
		wantAsked  []string
	}{
		{"a profile the stack declares is read from the stack, not the server", &mountProfileServer{profiles: onServer}, "stack", true, "a", nil},
		{"any other profile is read from the server", &mountProfileServer{profiles: onServer}, "live", true, "b", []string{"live"}},
		{"a profile that is nowhere cannot be read", &mountProfileServer{profiles: onServer}, "gone", false, "", []string{"gone"}},
		{"a server that fails cannot be read from: unreadable, not absent, and not an error", &mountProfileServer{err: api.StatusErrorf(http.StatusForbidden, "no")}, "live", false, "", []string{"live"}},
		{"a dropped connection is unreadable too", &mountProfileServer{err: errors.New("connection reset")}, "live", false, "", []string{"live"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			devs, ok := profileLookup(tc.server, declared)(tc.profile)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if _, has := devs[tc.wantDevice]; tc.wantOK && !has {
				t.Errorf("devices = %v, want one named %q", devs, tc.wantDevice)
			}
			if !reflect.DeepEqual(tc.server.asked, tc.wantAsked) {
				t.Errorf("server asked %v, want %v", tc.server.asked, tc.wantAsked)
			}
		})
	}
	if _, ok := profileLookup(nil, declared)("live"); ok {
		t.Error("with no server, only the stack's profiles are known")
	}
}

// A profile that exists only on the server, and mounts a host directory, hides the file as well.
func TestPlaceBy_AServerSideProfileCounts(t *testing.T) {
	srv := &mountProfileServer{profiles: map[string]map[string]map[string]string{"media": {"m": disk("/media", "/srv/media", "")}}}
	inst := instanceWith(nil, "media")
	pl := placeBy(inst, []Resource{fileAt("f", "/media/x"), fileAt("g", "/etc/x")}, profileLookup(srv, nil))
	if got := show(pl); !reflect.DeepEqual(got, []string{"g", "skip:f"}) {
		t.Errorf("got %v", got)
	}
}

func TestSkippedNote_Text(t *testing.T) {
	// the wording for a volume that cannot take the file (the mount path itself); a file under a volume mount normally goes into the volume
	got := skippedNote(skippedFile{File: Resource{Name: "ha-yaml"}, Why: "because /config is a mounted volume (device config)"})
	want := "file/ha-yaml: not placed before the first start, because /config is a mounted volume (device config); it is written right after the start, so the app may read its config first"
	if got != want {
		t.Errorf("note = %q\nwant   %q", got, want)
	}

	pl := placementFor([]Resource{instanceWith(map[string]map[string]string{"media": disk("/media", "/srv/media", "")}), fileAt("list", "/media/list.txt")})
	if len(pl.Skipped) != 1 {
		t.Fatalf("skipped = %v", pl.Skipped)
	}
	want = "file/list: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first"
	if got := skippedNote(pl.Skipped[0]); got != want {
		t.Errorf("note = %q\nwant   %q", got, want)
	}
}

// What an apply prints for an instance it created: only a file that is in place is ever called placed.
func TestFirstStartOutcome_Lines(t *testing.T) {
	hidden := skippedFile{File: Resource{Name: "ha-yaml"}, Why: "because /media is a mounted host directory (device media)"}
	cases := []struct {
		name string
		o    firstStartOutcome
		want []string
	}{
		{"nothing to say", firstStartOutcome{}, nil},
		{"placed only", firstStartOutcome{Placed: []string{"a", "b (in volume default/v)"}},
			[]string{"instance/ha: placed 2 file(s) before its first start: file/a, file/b (in volume default/v)"}},
		{"skipped only: no claim that anything was placed", firstStartOutcome{Skipped: []skippedFile{hidden}},
			[]string{"file/ha-yaml: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first"}},
		{"both: the count is of the placed files alone", firstStartOutcome{Placed: []string{"caddy"}, Skipped: []skippedFile{hidden}},
			[]string{"instance/ha: placed 1 file(s) before its first start: file/caddy", "file/ha-yaml: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.o.lines("ha"); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// mountFake is a server with volume files and instance files held in maps, recording each call. Any other method panics (nil embedded interface).
type mountFake struct {
	incus.InstanceServer
	volumes   map[string]string // "pool/volume:/path" -> content
	instance  map[string]string // "instance:/path" -> content
	dirs      map[string]bool   // volume paths that are directories
	readErr   error
	writeErr  error
	log       []string
	lastWrite incus.InstanceFileArgs
}

func newMountFake() *mountFake {
	return &mountFake{volumes: map[string]string{}, instance: map[string]string{}, dirs: map[string]bool{}}
}

func (s *mountFake) UseProject(string) incus.InstanceServer { return s }

func (s *mountFake) GetStorageVolumeFile(pool, volType, volume, p string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	key := pool + "/" + volume + ":" + p
	s.log = append(s.log, "read "+volType+" "+key)
	if s.readErr != nil {
		return nil, nil, s.readErr
	}
	if s.dirs[key] {
		return nil, &incus.InstanceFileResponse{Type: "directory"}, nil
	}
	c, ok := s.volumes[key]
	if !ok {
		return nil, nil, api.StatusErrorf(http.StatusNotFound, "not found")
	}
	return io.NopCloser(strings.NewReader(c)), &incus.InstanceFileResponse{Type: "file"}, nil
}

func (s *mountFake) CreateStorageVolumeFile(pool, volType, volume, p string, args incus.InstanceFileArgs) error {
	key := pool + "/" + volume + ":" + p
	s.log = append(s.log, "write "+volType+" "+key)
	if s.writeErr != nil {
		return s.writeErr
	}
	b, _ := io.ReadAll(args.Content)
	s.volumes[key] = string(b)
	s.lastWrite = args
	return nil
}

func (s *mountFake) CreateInstanceFile(instance, p string, args incus.InstanceFileArgs) error {
	s.log = append(s.log, "push "+instance+":"+p)
	b, _ := io.ReadAll(args.Content)
	s.instance[instance+":"+p] = string(b)
	return nil
}

var haMount = mount{Device: "config", Path: "/config", Pool: "default", Volume: "e2e-ha-config"}

func TestPlaceVolumeFile_WritesWhatIsNotThereAndLeavesWhatIsRight(t *testing.T) {
	f := fileAt("ha-yaml", "/config/configuration.yaml")
	f.Content = "http:\n  server_host: [203.0.113.10]\n"

	t.Run("absent: it is written, relative to the volume root, as placeFile writes", func(t *testing.T) {
		s := newMountFake()
		if err := placeVolumeFile(s, f, haMount); err != nil {
			t.Fatal(err)
		}
		if got := s.volumes["default/e2e-ha-config:/configuration.yaml"]; got != f.Content {
			t.Errorf("volume holds %q", got)
		}
		if want := []string{"read custom default/e2e-ha-config:/configuration.yaml", "write custom default/e2e-ha-config:/configuration.yaml"}; !reflect.DeepEqual(s.log, want) {
			t.Errorf("calls = %v", s.log)
		}
		if a := s.lastWrite; a.Type != "file" || a.WriteMode != "overwrite" || a.Mode != 0o644 {
			t.Errorf("args = %+v, want the ones placeFile uses", a)
		}
		if len(s.instance) != 0 {
			t.Errorf("nothing may be pushed into the instance's own filesystem: %v", s.instance)
		}
	})

	t.Run("already right: it is read and not written", func(t *testing.T) {
		s := newMountFake()
		s.volumes["default/e2e-ha-config:/configuration.yaml"] = f.Content
		if err := placeVolumeFile(s, f, haMount); err != nil {
			t.Fatal(err)
		}
		if len(s.log) != 1 || !strings.HasPrefix(s.log[0], "read ") {
			t.Errorf("calls = %v, want a single read", s.log)
		}
	})

	t.Run("different: it is overwritten", func(t *testing.T) {
		s := newMountFake()
		s.volumes["default/e2e-ha-config:/configuration.yaml"] = "old"
		if err := placeVolumeFile(s, f, haMount); err != nil {
			t.Fatal(err)
		}
		if got := s.volumes["default/e2e-ha-config:/configuration.yaml"]; got != f.Content || len(s.log) != 2 {
			t.Errorf("volume holds %q after %v", got, s.log)
		}
	})

	t.Run("a subdirectory path is passed through as it is", func(t *testing.T) {
		s := newMountFake()
		g := fileAt("g", "/config//packages/../packages/x.yaml")
		if err := placeVolumeFile(s, g, haMount); err != nil {
			t.Fatal(err)
		}
		if _, ok := s.volumes["default/e2e-ha-config:/packages/x.yaml"]; !ok {
			t.Errorf("volume holds %v", s.volumes)
		}
	})
}

func TestPlaceVolumeFile_Failures(t *testing.T) {
	f := fileAt("ha-yaml", "/config/sub/configuration.yaml")
	cases := []struct {
		name       string
		set        func(*mountFake)
		wantErr    string
		wantHint   bool
		wantWrites int
	}{
		{"a read that fails for a reason other than absence is an error, and writes nothing",
			func(s *mountFake) { s.readErr = api.StatusErrorf(http.StatusForbidden, "not authorized") }, "reading /sub/configuration.yaml in volume default/e2e-ha-config failed", false, 0},
		{"a directory in the way is an error, and writes nothing",
			func(s *mountFake) { s.dirs["default/e2e-ha-config:/sub/configuration.yaml"] = true }, "is a directory", false, 0},
		{"a write that finds no parent directory tries to make it (parentdirs.go), and says which directory could not be made",
			func(s *mountFake) { s.writeErr = api.StatusErrorf(http.StatusNotFound, "no such file or directory") }, "creating directory /sub", true, 2},
		{"any other failed write is reported with its cause and no hint",
			func(s *mountFake) { s.writeErr = api.StatusErrorf(http.StatusInternalServerError, "disk full") }, "disk full", false, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newMountFake()
			tc.set(s)
			err := placeVolumeFile(s, f, haMount)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if got := strings.Contains(err.Error(), "check that the volume exists"); got != tc.wantHint {
				t.Errorf("hint present = %v, want %v: %v", got, tc.wantHint, err)
			}
			writes := 0
			for _, l := range s.log {
				if strings.HasPrefix(l, "write") {
					writes++
				}
			}
			if writes != tc.wantWrites {
				t.Errorf("writes = %d, want %d (%v)", writes, tc.wantWrites, s.log)
			}
		})
	}
}

// The whole sequence, with the steps createInstance hands createInOrder: the file in the rootfs is pushed, the one under the volume mount is written into
// the volume, one that cannot be is reported and the instance still starts, and the apply says where each went.
func TestPlacer_CarriesOutAPlacementInOrder(t *testing.T) {
	s := newMountFake()
	rs := []Resource{
		instanceWith(map[string]map[string]string{"config": volumeDisk("/config", "e2e-ha-config"), "media": disk("/media", "/srv/media", "")}),
		fileAt("ha-yaml", "/config/configuration.yaml"),
		fileAt("caddy", "/etc/caddy/Caddyfile"),
		fileAt("playlist", "/media/list.txt"),
		fileAt("secrets", "/config/secrets.yaml"),
	}
	s.volumes["default/e2e-ha-config:/configuration.yaml"] = rs[1].Content // already right

	pl := placementFor(rs)
	pr := newPlacer(s, pl)
	// make the write of secrets.yaml fail, as a missing parent directory would
	failing := &failOn{mountFake: s, path: "default/e2e-ha-config:/secrets.yaml"}
	pr.server = failing

	var events []string
	names, err := createInOrder(instanceSteps{
		create: func() error { events = append(events, "create"); return nil },
		config: func() error { events = append(events, "config"); return nil },
		place:  func(f Resource) error { events = append(events, "place "+f.Name); return pr.place(f) },
		start:  func() error { events = append(events, "start"); return nil },
	}, pl.files())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(events, ", "); got != "create, config, place ha-yaml, place caddy, place secrets, start" {
		t.Errorf("order = %s", got)
	}
	if !reflect.DeepEqual(names, []string{"ha-yaml", "caddy"}) {
		t.Errorf("placed = %v", names)
	}
	if got := s.instance["ha:/etc/caddy/Caddyfile"]; got != "caddy" {
		t.Errorf("the Caddyfile was not pushed into the instance: %v", s.instance)
	}
	if _, ok := s.instance["ha:/config/configuration.yaml"]; ok {
		t.Error("a file under the volume mount was pushed into the rootfs, where the mount hides it")
	}

	lines := pr.outcome(names).lines("ha")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if want := "instance/ha: placed 2 file(s) before its first start: file/ha-yaml (in volume default/e2e-ha-config), file/caddy"; lines[0] != want {
		t.Errorf("line 0 = %q\nwant     %q", lines[0], want)
	}
	if want := "file/playlist: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first"; lines[1] != want {
		t.Errorf("line 1 = %q\nwant     %q", lines[1], want)
	}
	if !strings.HasPrefix(lines[2], "file/secrets: not placed before the first start, because writing /secrets.yaml into volume default/e2e-ha-config failed: ") ||
		!strings.HasSuffix(lines[2], "; it is written right after the start, so the app may read its config first") {
		t.Errorf("line 2 = %q", lines[2])
	}
}

// failOn fails the volume write of one path, as a server without that directory would.
type failOn struct {
	*mountFake
	path string
}

func (f *failOn) CreateStorageVolumeFile(pool, volType, volume, p string, args incus.InstanceFileArgs) error {
	if pool+"/"+volume+":"+p == f.path {
		f.log = append(f.log, "write "+volType+" "+f.path)
		return api.StatusErrorf(http.StatusNotFound, "no such file or directory")
	}
	return f.mountFake.CreateStorageVolumeFile(pool, volType, volume, p, args)
}

func (f *failOn) UseProject(string) incus.InstanceServer { return f }

// planServer is a server where the instance does not exist.
type planServer struct {
	incus.InstanceServer
	profiles map[string]map[string]map[string]string
	exists   bool
}

func (s *planServer) UseProject(string) incus.InstanceServer { return s }
func (s *planServer) GetInstance(name string) (*api.Instance, string, error) {
	if s.exists {
		return &api.Instance{Name: name, Type: "container"}, "", nil
	}
	return nil, "", api.StatusErrorf(http.StatusNotFound, "Instance not found")
}
func (s *planServer) GetProfile(name string) (*api.Profile, string, error) {
	devs, ok := s.profiles[name]
	if !ok {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "Profile not found")
	}
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: devs}}, "", nil
}

// plan says the same fact for the instance it would create, and says nothing for a file it can place.
func TestPlanInstance_SaysWhichFilesAMountKeepsFromTheFirstStart(t *testing.T) {
	rs := []Resource{
		instanceWith(map[string]map[string]string{"config": volumeDisk("/config", "v"), "media": disk("/media", "/srv/media", "")}, "live"),
		fileAt("ha-yaml", "/config/configuration.yaml"),
		fileAt("playlist", "/media/list.txt"),
		fileAt("art", "/art/x"),
		fileAt("caddy", "/etc/caddy/Caddyfile"),
	}
	srv := &planServer{profiles: map[string]map[string]map[string]string{"live": {"art": disk("/art", "/srv/art", "")}}}
	opts := PlanOptions{Offline: true}.withTargets(rs)

	p, err := planInstance(srv, rs[0], opts)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionCreate {
		t.Errorf("action = %v, want create", p.Action)
	}
	want := []string{
		"file/playlist: not placed before the first start, because /media is a mounted host directory (device media); it is written right after the start, so the app may read its config first",
		"file/art: not placed before the first start, because /art is a mounted host directory (device art); it is written right after the start, so the app may read its config first",
	}
	if !reflect.DeepEqual(p.Notes, want) {
		t.Errorf("notes = %q\nwant    %q", p.Notes, want)
	}

	// an instance that exists has its mounts in place already: its files are pushed into them, and there is nothing to say
	srv.exists = true
	p, err = planInstance(srv, rs[0], opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Notes) != 0 {
		t.Errorf("an existing instance has no first start to wait for: %q", p.Notes)
	}
}

func TestPlanInstance_NoCandidateFilesNoNotes(t *testing.T) {
	inst := instanceWith(map[string]map[string]string{"media": disk("/media", "/srv/media", "")}, "somewhere")
	srv := &planServer{} // GetProfile would answer 404, which is fine; the point is the result
	p, err := planInstance(srv, inst, PlanOptions{Offline: true}.withTargets([]Resource{inst}))
	if err != nil || p.Action != ActionCreate || len(p.Notes) != 0 {
		t.Errorf("%+v %v", p, err)
	}
}
