package resolve

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/ownership"
)

// Ownership stamps, phase 1 (issue #127, design #133): every kind against absent, unstamped, stamped here and stamped elsewhere; the stamp in the
// creation request; a plan that never calls the stamp a change; and a stamp-only write that restarts nothing.

// ownFake is a server that holds one object of every kind that carries a stamp, and records every write. A call a stamp has no business making
// panics through the nil embedded interface, and the calls that change an instance's state are recorded by name so a test can say there were none.
type ownFake struct {
	incus.InstanceServer

	instances map[string]*api.Instance
	profiles  map[string]*api.Profile
	projects  map[string]*api.Project
	networks  map[string]*api.Network
	acls      map[string]*api.NetworkACL
	volumes   map[string]*api.StorageVolume // "pool/name"
	images    map[string]*api.Image         // by fingerprint
	aliases   map[string]string             // alias -> fingerprint

	created       []string                     // "kind/name", in order
	createdConfig map[string]map[string]string // "kind/name" -> the config (an image: properties) the creation request carried
	updated       []string                     // "kind/name" of each whole-object write
	updatedConfig map[string]map[string]string // "kind/name" -> the config after the write
	imageWrites   []api.ImagePut
	patches       []string // "PATCH path {body}" of each raw request
	stateChanges  []string // every call that starts, stops, restarts or freezes
}

func newOwnFake() *ownFake {
	return &ownFake{
		instances: map[string]*api.Instance{}, profiles: map[string]*api.Profile{}, projects: map[string]*api.Project{},
		networks: map[string]*api.Network{}, acls: map[string]*api.NetworkACL{}, volumes: map[string]*api.StorageVolume{},
		images: map[string]*api.Image{}, aliases: map[string]string{},
		createdConfig: map[string]map[string]string{}, updatedConfig: map[string]map[string]string{},
	}
}

var notFound = api.StatusErrorf(http.StatusNotFound, "not found")

func (f *ownFake) UseProject(string) incus.InstanceServer { return f }

func (f *ownFake) GetInstance(n string) (*api.Instance, string, error) {
	if i, ok := f.instances[n]; ok {
		cp := *i
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetProfile(n string) (*api.Profile, string, error) {
	if p, ok := f.profiles[n]; ok {
		cp := *p
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetProject(n string) (*api.Project, string, error) {
	if p, ok := f.projects[n]; ok {
		cp := *p
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetNetwork(n string) (*api.Network, string, error) {
	if p, ok := f.networks[n]; ok {
		cp := *p
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetNetworkACL(n string) (*api.NetworkACL, string, error) {
	if p, ok := f.acls[n]; ok {
		cp := *p
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetStoragePoolVolume(pool, _, n string) (*api.StorageVolume, string, error) {
	if p, ok := f.volumes[pool+"/"+n]; ok {
		cp := *p
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetImageAlias(n string) (*api.ImageAliasesEntry, string, error) {
	if fp, ok := f.aliases[n]; ok {
		return &api.ImageAliasesEntry{Name: n, ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: fp}}, "", nil
	}
	return nil, "", notFound
}

func (f *ownFake) GetImage(fp string) (*api.Image, string, error) {
	if i, ok := f.images[fp]; ok {
		cp := *i
		return &cp, "etag", nil
	}
	return nil, "", notFound
}

func (f *ownFake) record(what string, config map[string]string) {
	f.updated = append(f.updated, what)
	f.updatedConfig[what] = config
}

func (f *ownFake) UpdateProfile(n string, put api.ProfilePut, _ string) error {
	f.record("profile/"+n, put.Config)
	f.profiles[n].ProfilePut = put
	return nil
}

func (f *ownFake) UpdateProject(n string, put api.ProjectPut, _ string) error {
	f.record("project/"+n, put.Config)
	f.projects[n].ProjectPut = put
	return nil
}

func (f *ownFake) UpdateNetwork(n string, put api.NetworkPut, _ string) error {
	f.record("network/"+n, put.Config)
	f.networks[n].NetworkPut = put
	return nil
}

func (f *ownFake) UpdateNetworkACL(n string, put api.NetworkACLPut, _ string) error {
	f.record("network-acl/"+n, put.Config)
	f.acls[n].NetworkACLPut = put
	return nil
}

func (f *ownFake) UpdateStoragePoolVolume(pool, _, n string, put api.StorageVolumePut, _ string) error {
	f.record("storage-volume/"+n, put.Config)
	f.volumes[pool+"/"+n].StorageVolumePut = put
	return nil
}

func (f *ownFake) UpdateImage(fp string, put api.ImagePut, _ string) error {
	f.record("image/"+fp, put.Properties)
	f.imageWrites = append(f.imageWrites, put)
	f.images[fp].ImagePut = put
	return nil
}

// UpdateInstance is the whole-instance write that converging does; adopting must never make it.
func (f *ownFake) UpdateInstance(n string, put api.InstancePut, _ string) (incus.Operation, error) {
	f.record("instance/"+n, put.Config)
	f.instances[n].InstancePut = put
	return waitOp{}, nil
}

func (f *ownFake) UpdateInstanceState(n string, put api.InstanceStatePut, _ string) (incus.Operation, error) {
	f.stateChanges = append(f.stateChanges, put.Action+" "+n)
	return waitOp{}, nil
}

func (f *ownFake) RawQuery(method, path string, data any, _ string) (*api.Response, string, error) {
	body := data.(map[string]any)["config"].(map[string]string)
	f.patches = append(f.patches, method+" "+path)
	name := strings.TrimPrefix(strings.SplitN(path, "?", 2)[0], "/1.0/instances/")
	inst := f.instances[name]
	cfg := map[string]string{}
	for k, v := range inst.Config {
		cfg[k] = v
	}
	for k, v := range body {
		cfg[k] = v
	}
	inst.Config = cfg
	f.updatedConfig["patch/"+name] = body
	return &api.Response{}, "", nil
}

func (f *ownFake) CreateProject(p api.ProjectsPost) error {
	f.created = append(f.created, "project/"+p.Name)
	f.createdConfig["project/"+p.Name] = p.Config
	return nil
}

func (f *ownFake) CreateProfile(p api.ProfilesPost) error {
	f.created = append(f.created, "profile/"+p.Name)
	f.createdConfig["profile/"+p.Name] = p.Config
	return nil
}

func (f *ownFake) CreateNetwork(p api.NetworksPost) error {
	f.created = append(f.created, "network/"+p.Name)
	f.createdConfig["network/"+p.Name] = p.Config
	return nil
}

func (f *ownFake) CreateNetworkACL(p api.NetworkACLsPost) error {
	f.created = append(f.created, "network-acl/"+p.Name)
	f.createdConfig["network-acl/"+p.Name] = p.Config
	return nil
}

func (f *ownFake) CreateStoragePoolVolume(pool string, p api.StorageVolumesPost) error {
	f.created = append(f.created, "storage-volume/"+p.Name)
	f.createdConfig["storage-volume/"+p.Name] = p.Config
	f.volumes[pool+"/"+p.Name] = &api.StorageVolume{Name: p.Name, StorageVolumePut: p.StorageVolumePut}
	return nil
}

func (f *ownFake) CreateInstanceFromImage(_ incus.ImageServer, _ api.Image, req api.InstancesPost) (incus.RemoteOperation, error) {
	f.created = append(f.created, "instance/"+req.Name)
	f.createdConfig["instance/"+req.Name] = req.Config
	f.instances[req.Name] = &api.Instance{Name: req.Name, Type: string(req.Type), Status: "Stopped", InstancePut: req.InstancePut}
	return remoteWait{}, nil
}

type remoteWait struct{ incus.RemoteOperation }

func (remoteWait) Wait() error { return nil }

// imageOp answers the fingerprint an image import reports.
type imageOp struct{ incus.Operation }

func (imageOp) Wait() error { return nil }
func (imageOp) Get() api.Operation {
	return api.Operation{Metadata: map[string]any{"fingerprint": "fp-new"}}
}

func (f *ownFake) CreateImage(_ api.ImagesPost, args *incus.ImageCreateArgs) (incus.Operation, error) {
	meta, err := io.ReadAll(args.MetaFile)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(bytes.NewReader(meta))
	if _, err := tr.Next(); err != nil {
		return nil, err
	}
	data, _ := io.ReadAll(tr)
	var md api.ImageMetadata
	if err := yaml.Unmarshal(data, &md); err != nil {
		return nil, err
	}
	f.created = append(f.created, "image/"+args.MetaName)
	f.createdConfig["image/"+args.MetaName] = md.Properties
	return imageOp{}, nil
}

func (f *ownFake) CreateImageAlias(api.ImageAliasesPost) error { return nil }

// The five states every kind is run against. "absent" is the object not being there at all.
const (
	absent        = "absent"
	unstamped     = "unstamped"
	stampedHere   = "stamped here"
	stampedElsewh = "stamped elsewhere"
)

// stampFor is the config an object in the given state carries, on top of base.
func stampFor(state string, base map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	switch state {
	case stampedHere:
		out[ownership.Key] = "lab"
	case stampedElsewh:
		out[ownership.Key] = "other"
	}
	return out
}

// seed puts one object of the kind r declares into the fake, in the given state, equal to the declaration apart from the stamp (so any difference a plan
// then finds would be the stamp). It returns the resource to plan.
func (f *ownFake) seed(kind Kind, state string) Resource {
	if state == absent {
		return stampedResource(kind)
	}
	r := stampedResource(kind)
	cfg := stampFor(state, r.Config)
	switch kind {
	case KindProject:
		f.projects["p"] = &api.Project{Name: "p", ProjectPut: api.ProjectPut{Config: cfg}}
	case KindProfile:
		f.profiles["p"] = &api.Profile{Name: "p", ProfilePut: api.ProfilePut{Config: cfg}}
	case KindStorageVolume:
		f.volumes["default/p"] = &api.StorageVolume{Name: "p", Type: "custom", StorageVolumePut: api.StorageVolumePut{Config: cfg}}
	case KindInstance:
		f.instances["p"] = &api.Instance{Name: "p", Type: "container", Status: "Running", StatusCode: api.Running, InstancePut: api.InstancePut{Config: cfg, Profiles: []string{"default"}}}
	case KindNetwork:
		f.networks["p"] = &api.Network{Name: "p", Type: "bridge", Managed: true, Status: "Created", NetworkPut: api.NetworkPut{Config: cfg}}
	case KindNetworkACL:
		f.acls["p"] = &api.NetworkACL{NetworkACLPost: api.NetworkACLPost{Name: "p"}, NetworkACLPut: api.NetworkACLPut{Config: cfg}}
	case KindImage:
		f.aliases["a"] = "fp1"
		f.images["fp1"] = &api.Image{Fingerprint: "fp1", ImagePut: api.ImagePut{Properties: stampFor(state, r.Properties), Profiles: []string{"p1", "p2"}, AutoUpdate: true}}
	}
	return r
}

// stampedResource is a minimal declaration of each kind that carries a stamp, named "p" (an image: alias "a").
func stampedResource(kind Kind) Resource {
	r := Resource{Kind: kind, Name: "p", Config: map[string]string{"user.note": "x"}}
	switch kind {
	case KindStorageVolume:
		r.Backup = &backupmeta.VolumeBackup{None: "test"}
	case KindInstance:
		r.Profiles = []string{"default"}
	case KindNetworkACL:
		r.Config = map[string]string{}
	case KindImage:
		r.Config = nil
		r.Alias, r.Source, r.Architecture, r.Properties = "a", "unused", "x86_64", map[string]string{"os": "x"}
	}
	return r
}

var stampedKinds = []Kind{KindProject, KindProfile, KindStorageVolume, KindInstance, KindNetwork, KindNetworkACL, KindImage}

func ownOpts(stack string) PlanOptions {
	o := NewPlanOptions(true)
	o.stack = stack
	o.targets = map[string]Resource{}
	return o
}

// Every kind that carries a stamp, planned against every live state: the stamp is never a change, and it is classified.
func TestEveryKindReportsWhoseItIsAndNeverCallsTheStampAChange(t *testing.T) {
	want := map[string]ownership.State{absent: ownership.Unknown, unstamped: ownership.Unstamped, stampedHere: ownership.Here, stampedElsewh: ownership.Elsewhere}
	for _, kind := range stampedKinds {
		for _, state := range []string{absent, unstamped, stampedHere, stampedElsewh} {
			t.Run(string(kind)+"/"+state, func(t *testing.T) {
				f := newOwnFake()
				r := f.seed(kind, state)
				p, err := planOne(f, r, ownOpts("lab"))
				if err != nil {
					t.Fatal(err)
				}
				if state == absent {
					if p.Action != ActionCreate {
						t.Fatalf("action = %v, want a create", p.Action)
					}
				} else if p.Action != ActionNone || len(p.Changes) != 0 {
					t.Fatalf("a stamp-only difference must not be a change: action %v, changes %q", p.Action, p.Changes)
				}
				for _, c := range p.Changes {
					if strings.Contains(c, ownership.Key) {
						t.Errorf("a change line names the stamp: %q", c)
					}
				}
				if p.Ownership.State != want[state] {
					t.Errorf("ownership = %v, want %v", p.Ownership.State, want[state])
				}
				if state == stampedElsewh && p.Ownership.Owner != "other" {
					t.Errorf("owner = %q, want other", p.Ownership.Owner)
				}
				if len(f.updated) != 0 || len(f.created) != 0 || len(f.patches) != 0 {
					t.Errorf("planning wrote: %v %v %v", f.created, f.updated, f.patches)
				}
				// phase 1 keeps today's behaviour for an object another stack stamped: only a volume says anything
				if state == stampedElsewh {
					warned := strings.Contains(strings.Join(p.Warnings, "|"), `"other"`)
					if warned != (kind == KindStorageVolume) {
						t.Errorf("warns about the other owner = %v, want %v (only volumes do, today): %q", warned, kind == KindStorageVolume, p.Warnings)
					}
				}
			})
		}
	}
}

// A stack with no name stamps, adopts and reports nothing: it works as it did.
func TestAnUnnamedStackHasNoAnswerForAnyKind(t *testing.T) {
	for _, kind := range stampedKinds {
		f := newOwnFake()
		r := f.seed(kind, unstamped)
		p, err := planOne(f, r, ownOpts(""))
		if err != nil {
			t.Fatal(err)
		}
		if p.Ownership.State != ownership.Unknown || p.Action != ActionNone {
			t.Errorf("%s: ownership %v action %v, want unknown and no changes", kind, p.Ownership.State, p.Action)
		}
	}
}

// An ordinary apply writes nothing for an object that exists and is unstamped, and when it does update one it does not add the stamp on the way.
func TestAnOrdinaryApplyNeverStampsAnExistingObject(t *testing.T) {
	for _, kind := range stampedKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			r := f.seed(kind, unstamped)
			var notes []string
			out, err := applyOne(f, r, ownOpts("lab"), func(format string, args ...any) { notes = append(notes, format) })
			if err != nil || out != outConverged {
				t.Fatalf("outcome %v, %v", out, err)
			}
			if len(f.updated)+len(f.created)+len(f.patches)+len(f.stateChanges) != 0 {
				t.Errorf("an ordinary apply wrote to an unstamped object: updated %v created %v patches %v state %v", f.updated, f.created, f.patches, f.stateChanges)
			}
		})
	}
	// an update for another reason leaves the stamp out
	f := newOwnFake()
	f.profiles["p"] = &api.Profile{Name: "p"}
	r := Resource{Kind: KindProfile, Name: "p", Config: map[string]string{"user.note": "new"}}
	if _, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if got := f.updatedConfig["profile/p"]; got["user.note"] != "new" || got[ownership.Key] != "" {
		t.Errorf("the update wrote %v, want the declared key and no stamp", got)
	}
}

// Creating: the stamp is in the creation request of every kind, from the stack's name, and the resource's own config is not changed to carry it.
func TestObjectsAreStampedInTheirCreationRequest(t *testing.T) {
	t.Setenv("INCUS_CONF", t.TempDir()) // no client configuration: an instance's image is a local one
	src := filepath.Join(t.TempDir(), "disk.img")
	if err := os.WriteFile(src, []byte("not a real disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, stack := range []string{"lab", ""} {
		for _, kind := range stampedKinds {
			t.Run(string(kind)+"/"+stack, func(t *testing.T) {
				f := newOwnFake()
				f.aliases["local-image"] = "fp0"
				f.images["fp0"] = &api.Image{Fingerprint: "fp0"}
				r := stampedResource(kind)
				if kind == KindInstance {
					r.Image = "local-image"
				}
				if kind == KindImage {
					r.Source = src
				}
				before := map[string]string{}
				for k, v := range r.Config {
					before[k] = v
				}
				if err := createOne(f, r, volumeEnv{targets: map[string]Resource{}, stack: stack}); err != nil {
					t.Fatal(err)
				}
				if len(f.created) != 1 {
					t.Fatalf("created %v", f.created)
				}
				got := f.createdConfig[f.created[0]][ownership.Key]
				if got != stack {
					t.Errorf("the creation request carried %q = %q, want %q", ownership.Key, got, stack)
				}
				if !reflect.DeepEqual(r.Config, before) && len(before) > 0 {
					t.Errorf("creating changed the declaration's config: %v, was %v", r.Config, before)
				}
				if kind == KindInstance {
					// the stamp survives the config step that follows creation, and the instance is stamped in the request, not after
					if f.instances["p"].Config[ownership.Key] != stack {
						t.Errorf("the instance ended with %q = %q, want %q", ownership.Key, f.instances["p"].Config[ownership.Key], stack)
					}
				}
				if kind == KindStorageVolume || kind == KindProfile {
					if v := f.createdConfig[f.created[0]]["user.note"]; v != "x" {
						t.Errorf("the declared config must still be sent: %v", f.createdConfig[f.created[0]])
					}
				}
				if kind == KindImage && f.createdConfig[f.created[0]]["os"] != "x" {
					t.Errorf("the declared properties must still be sent: %v", f.createdConfig[f.created[0]])
				}
			})
		}
	}
}

// A project that is only NAMED by project: is a reference and is never stamped: nothing in a plan or an apply creates or writes it.
func TestAProjectOnlyNamedByAResourceIsNeverStamped(t *testing.T) {
	f := newOwnFake()
	f.profiles["p"] = &api.Profile{Name: "p"}
	r := Resource{Kind: KindProfile, Name: "p", Project: "elsewhere", Config: map[string]string{}}
	if _, err := applyOne(f, r, ownOpts("lab"), func(string, ...any) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.projects) != 0 || len(f.created) != 0 {
		t.Errorf("a project named by project: was written: %v %v", f.projects, f.created)
	}
	if stampedKind(KindHelper) {
		t.Error("the helper's instance and project are the installer's: never stamped here")
	}
}

// Adopting: one key, narrow, and it restarts nothing.

func adoptLevels(rs ...Resource) [][]Resource { return [][]Resource{rs} }

func TestAdoptStampsEachKindWithOneKeyAndKeepsTheRest(t *testing.T) {
	for _, kind := range stampedKinds {
		t.Run(string(kind), func(t *testing.T) {
			f := newOwnFake()
			r := f.seed(kind, unstamped)
			res, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Stamped) != 1 || res.ByKind[kind] != 1 {
				t.Fatalf("stamped %v, want exactly this one", res.Stamped)
			}
			// what the object carries now: its own keys, and the stamp
			var got map[string]string
			switch kind {
			case KindProject:
				got = f.projects["p"].Config
			case KindProfile:
				got = f.profiles["p"].Config
			case KindStorageVolume:
				got = f.volumes["default/p"].Config
			case KindInstance:
				got = f.instances["p"].Config
			case KindNetwork:
				got = f.networks["p"].Config
			case KindNetworkACL:
				got = f.acls["p"].Config
			case KindImage:
				got = f.images["fp1"].Properties
			}
			if got[ownership.Key] != "lab" {
				t.Errorf("the object carries %v, want the stamp", got)
			}
			base := stampedResource(kind)
			bag := base.Config
			if kind == KindImage {
				bag = base.Properties
			}
			for k, v := range bag {
				if got[k] != v {
					t.Errorf("%s was dropped or changed by the stamp: %q", k, got[k])
				}
			}
			if len(got) != len(bag)+1 {
				t.Errorf("the write changed more than one key: %v", got)
			}
			// a second pass has nothing to do and writes nothing
			before := len(f.updated) + len(f.patches)
			again, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0)
			if err != nil || len(again.Stamped) != 0 || len(f.updated)+len(f.patches) != before {
				t.Errorf("a stamped object was written again: %v %v", again.Stamped, err)
			}
		})
	}
}

// The test the design asks for by name: a running instance with restart: true is stamped and nothing about its state is asked of the server, and the
// write is a PATCH of the one key and not the whole-instance write converging does.
func TestAdoptRestartsNothingEvenWhenTheInstanceSaysRestart(t *testing.T) {
	f := newOwnFake()
	f.instances["web"] = &api.Instance{Name: "web", Type: "container", Status: "Running", StatusCode: api.Running,
		InstancePut: api.InstancePut{Config: map[string]string{"environment.TZ": "UTC"}, Profiles: []string{"default"}}}
	r := Resource{Kind: KindInstance, Name: "web", Project: "ha", Restart: true, Config: map[string]string{"environment.TZ": "UTC"}}

	res, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.stateChanges) != 0 {
		t.Fatalf("adopt asked the server to change state: %v", f.stateChanges)
	}
	if len(f.updated) != 0 {
		t.Fatalf("adopt made a whole-instance write (which converging follows with a restart): %v", f.updated)
	}
	if len(f.patches) != 1 || f.patches[0] != "PATCH /1.0/instances/web?project=ha" {
		t.Fatalf("patches = %v, want one PATCH of the instance, in its project", f.patches)
	}
	if got := f.updatedConfig["patch/web"]; !reflect.DeepEqual(got, map[string]string{ownership.Key: "lab"}) {
		t.Errorf("the PATCH carried %v, want only the stamp", got)
	}
	if lines := res.Lines(); !strings.HasSuffix(lines[0], "restarted 0") || !strings.Contains(lines[0], "1 instance") {
		t.Errorf("summary = %q", lines)
	}
}

// The profile under a running instance and the bridge in use: stamping them is a write of the one key, and the guard that blocks a change to a bridge
// that instances use does not see the stamp.
func TestAdoptStampsABridgeInUseAndItIsNotBlocked(t *testing.T) {
	f := newOwnFake()
	r := f.seed(KindNetwork, unstamped)
	f.networks["p"].UsedBy = []string{"/1.0/instances/web", "/1.0/profiles/default"}
	if _, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0); err != nil {
		t.Fatal(err)
	}
	if f.networks["p"].Config[ownership.Key] != "lab" {
		t.Fatalf("the bridge in use was not stamped: %v", f.networks["p"].Config)
	}
	p, err := planOne(f, r, ownOpts("lab"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNone || len(p.Blocked) != 0 {
		t.Errorf("a stamped bridge in use plans %v %v, want no changes", p.Action, p.Blocked)
	}
	// unstamped, in use, nothing else different: still no changes and not BLOCKED
	f2 := newOwnFake()
	r2 := f2.seed(KindNetwork, unstamped)
	f2.networks["p"].UsedBy = []string{"/1.0/instances/web"}
	if p, _ := planOne(f2, r2, ownOpts("lab")); p.Action != ActionNone || len(p.Blocked) != 0 {
		t.Errorf("an unstamped bridge in use plans %v %v, want no changes", p.Action, p.Blocked)
	}
	// the guard itself exempts the key, should it ever reach the desired config
	live := liveBridge(map[string]string{"ipv4.nat": "true"}, "/1.0/instances/web")
	desired := Resource{Kind: KindNetwork, Name: "svc", Config: map[string]string{"ipv4.nat": "true", ownership.Key: "lab"}}
	if p, _ := planNetwork(&netServer{live: live}, desired); p.Action == ActionBlocked {
		t.Errorf("the stamp alone blocked a bridge in use: %v", p.Blocked)
	}
}

func TestAdoptLeavesWhatTinkWillNotChange(t *testing.T) {
	f := newOwnFake()
	f.networks["lan"] = &api.Network{Name: "lan", Type: "bridge", Managed: false}
	f.networks["ovn"] = &api.Network{Name: "ovn", Type: "ovn", Managed: true, Status: "Created"}
	rs := []Resource{{Kind: KindNetwork, Name: "lan"}, {Kind: KindNetwork, Name: "ovn"}}
	res, err := Adopt(t.Context(), f, adoptLevels(rs...), "lab", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stamped) != 0 || len(res.Skipped) != 2 || len(f.updated) != 0 {
		t.Errorf("stamped %v skipped %v updated %v, want both skipped and nothing written", res.Stamped, res.Skipped, f.updated)
	}
}

func TestAdoptLeavesAnObjectAnotherStackStampedAndOneThatIsNotThere(t *testing.T) {
	f := newOwnFake()
	rEl := f.seed(KindProfile, stampedElsewh)
	rAbsent := stampedResource(KindInstance)
	res, err := Adopt(t.Context(), f, adoptLevels(rEl, rAbsent), "lab", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stamped) != 0 || len(f.updated)+len(f.patches)+len(f.created) != 0 {
		t.Errorf("stamped %v, wrote %v %v %v: another stack's object is its stack's, and a missing one is created stamped by apply", res.Stamped, f.updated, f.patches, f.created)
	}
	if f.profiles["p"].Config[ownership.Key] != "other" {
		t.Errorf("the other stack's stamp was changed: %v", f.profiles["p"].Config)
	}
}

// An image write that left the profiles out would reset them to ["default"]: the write echoes what the image had.
func TestAnImageStampKeepsTheImagesProfiles(t *testing.T) {
	f := newOwnFake()
	r := f.seed(KindImage, unstamped)
	if _, err := Adopt(t.Context(), f, adoptLevels(r), "lab", 0); err != nil {
		t.Fatal(err)
	}
	if len(f.imageWrites) != 1 {
		t.Fatalf("image writes = %d", len(f.imageWrites))
	}
	w := f.imageWrites[0]
	if !reflect.DeepEqual(w.Profiles, []string{"p1", "p2"}) || !w.AutoUpdate {
		t.Errorf("the write was %+v, want the image's profiles and auto_update echoed", w)
	}
}

func TestAdoptCountsARunningVirtualMachineBecauseIncusMarksItForAReset(t *testing.T) {
	f := newOwnFake()
	f.instances["vm1"] = &api.Instance{Name: "vm1", Type: "virtual-machine", Status: "Running", StatusCode: api.Running}
	f.instances["vm2"] = &api.Instance{Name: "vm2", Type: "virtual-machine", Status: "Stopped", StatusCode: api.Stopped}
	res, err := Adopt(t.Context(), f, adoptLevels(Resource{Kind: KindInstance, Name: "vm1"}, Resource{Kind: KindInstance, Name: "vm2"}), "lab", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.RunningVMs != 1 || len(res.Stamped) != 2 {
		t.Errorf("running VMs %d, stamped %v, want 1 and 2", res.RunningVMs, res.Stamped)
	}
	if !strings.Contains(strings.Join(res.Lines(), "\n"), "full restart") {
		t.Errorf("the summary does not say what a running VM costs: %q", res.Lines())
	}
}

func TestAdoptNeedsAStackNameAndStopsWhenInterrupted(t *testing.T) {
	f := newOwnFake()
	if _, err := Adopt(t.Context(), f, nil, "", 0); err == nil {
		t.Error("a stack with no name has nothing to stamp with")
	}
	r := f.seed(KindProfile, unstamped)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Adopt(ctx, f, adoptLevels(r), "lab", 0); err == nil || len(f.updated) != 0 {
		t.Errorf("an interrupted adopt must stop before writing: %v %v", err, f.updated)
	}
}

// The line plan prints, and the summary.

func TestTheOwnershipLineIsOneGroupedLine(t *testing.T) {
	rep := NewOwnershipReport("lab", false)
	for i, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		kind := KindInstance
		if i%2 == 1 {
			kind = KindProfile
		}
		rep.Add(PlannedResource{Resource: Resource{Kind: kind, Name: n, Project: "ha"}, Ownership: StampState{State: ownership.Unstamped}})
	}
	rep.Add(PlannedResource{Resource: Resource{Kind: KindInstance, Name: "stamped"}, Ownership: StampState{State: ownership.Here}})
	rep.Add(PlannedResource{Resource: Resource{Kind: KindInstance, Name: "foreign"}, Ownership: StampState{State: ownership.Elsewhere, Owner: "o"}})
	rep.Add(PlannedResource{Resource: Resource{Kind: KindFile, Name: "f"}})
	line := rep.Line()
	if strings.Contains(line, "\n") {
		t.Fatalf("not one line:\n%s", line)
	}
	for _, want := range []string{"7 declared objects have no stamp", "instance/a@ha", "(+2 more)", "--adopt", "restarts none"} {
		if !strings.Contains(line, want) {
			t.Errorf("line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(line, "stamped") && strings.Contains(line, "instance/stamped") || strings.Contains(line, "foreign") {
		t.Errorf("an object that has a stamp is listed: %s", line)
	}
	// nothing to say, nothing printed
	none := NewOwnershipReport("lab", false)
	none.Add(PlannedResource{Resource: Resource{Kind: KindInstance, Name: "x"}, Ownership: StampState{State: ownership.Here}})
	none.Add(PlannedResource{Resource: Resource{Kind: KindInstance, Name: "new"}}) // created: no state
	if none.Line() != "" {
		t.Errorf("a stack whose objects are all stamped (or new) prints nothing: %q", none.Line())
	}
	// standing adoption changes the promise
	adopting := NewOwnershipReport("lab", true)
	adopting.Add(PlannedResource{Resource: Resource{Kind: KindInstance, Name: "x"}, Ownership: StampState{State: ownership.Unstamped}})
	if l := adopting.Line(); !strings.Contains(l, "1 declared object has no stamp") || !strings.Contains(l, "the next apply stamps it") {
		t.Errorf("line = %s", l)
	}
}

func TestAnUnnamedStackSaysSoOnce(t *testing.T) {
	rep := NewOwnershipReport("", false)
	rep.Add(PlannedResource{Resource: Resource{Kind: KindInstance, Name: "a"}})
	rep.Add(PlannedResource{Resource: Resource{Kind: KindProfile, Name: "b"}})
	rep.Add(PlannedResource{Resource: Resource{Kind: KindFile, Name: "c"}}) // no object: not counted
	line := rep.Line()
	if strings.Contains(line, "\n") || !strings.Contains(line, "has no name") || !strings.Contains(line, "2 declared") {
		t.Errorf("line = %q", line)
	}
	if l := NewOwnershipReport("", false).Line(); l != "" {
		t.Errorf("a stack with nothing that carries a stamp says nothing: %q", l)
	}
}

func TestAdoptSummaryNamesTheKindsAndCountsNoRestart(t *testing.T) {
	a := AdoptResult{Stamped: make([]string, 14), ByKind: map[Kind]int{KindInstance: 9, KindProfile: 3, KindStorageVolume: 2}}
	got := a.Lines()[0]
	want := "adopt: stamped user.tink.stack on 14 objects (9 instance, 3 profile, 2 storage-volume); restarted 0"
	if got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if l := (AdoptResult{ByKind: map[Kind]int{}}).Lines()[0]; !strings.Contains(l, "nothing to stamp") {
		t.Errorf("an empty pass says so: %q", l)
	}
}

// Validate: the stamp cannot be declared, for any kind that has a config or properties, and `ownership:` is the stack's only.

func TestTheStampCannotBeDeclaredForAnyKind(t *testing.T) {
	for _, kind := range stampedKinds {
		r := stampedResource(kind)
		if kind == KindImage {
			r.Properties = map[string]string{ownership.Key: "x"}
		} else {
			r.Config = map[string]string{ownership.Key: "x"}
		}
		err := Validate(r)
		if err == nil || !strings.Contains(err.Error(), ownership.Key) || !strings.Contains(err.Error(), "--adopt") {
			t.Errorf("%s: declaring %s must be refused with the way to adopt: %v", kind, ownership.Key, err)
		}
	}
	// through the loader too, as a person would meet it
	path := writeTemp(t, "kind: profile\nname: p\nconfig:\n  user.tink.stack: x\n")
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "written by tink itself") {
		t.Errorf("load err = %v", err)
	}
}

func TestOwnershipIsAFieldOfTheStackAlone(t *testing.T) {
	if err := Validate(Resource{Kind: KindStack, Name: "lab", Ownership: OwnershipAdoptOnApply}); err != nil {
		t.Errorf("adopt-on-apply: %v", err)
	}
	if err := Validate(Resource{Kind: KindStack, Name: "lab", Ownership: OwnershipExplicit}); err != nil {
		t.Errorf("explicit: %v", err)
	}
	if err := Validate(Resource{Kind: KindStack, Name: "lab", Ownership: "always"}); err == nil {
		t.Error("an unknown mode must be refused")
	}
	if err := Validate(Resource{Kind: KindProfile, Name: "p", Ownership: OwnershipAdoptOnApply}); err == nil {
		t.Error("ownership: belongs to kind: stack")
	}
	rs, err := LoadFile(writeTemp(t, "kind: stack\nname: lab\nownership: adopt-on-apply\n"))
	if err != nil || !AdoptOnApply(rs) {
		t.Errorf("loaded %v, %v: adopt-on-apply must read through", rs, err)
	}
	if AdoptOnApply([]Resource{{Kind: KindStack, Name: "lab"}}) {
		t.Error("adopting is not the default")
	}
}

// Export never writes the stamp, on any kind it exports, and what it writes still loads and plans as no changes.
func TestExportNeverWritesTheStamp(t *testing.T) {
	inst := kumaInstance()
	inst.Config[ownership.Key] = "lab"
	s := &stampedExportServer{exportServer: exportServer{inst: inst, volCfg: map[string]string{ownership.Key: "lab", "size": "1GiB"}}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.YAML, ownership.Key) || strings.Contains(res.YAML, "user.tink.stack") {
		t.Errorf("the export wrote the stamp:\n%s", res.YAML)
	}
	path := filepath.Join(t.TempDir(), "tink.yaml")
	if err := os.WriteFile(path, []byte(res.YAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Errorf("the export must load: %v", err)
	}
	if !res.Verified {
		t.Errorf("the export must plan as no changes: %+v", res.Plans)
	}
}

type stampedExportServer struct{ exportServer }

func (s *stampedExportServer) UseProject(string) incus.InstanceServer { return s }

func (s *stampedExportServer) GetProject(name string) (*api.Project, string, error) {
	return &api.Project{Name: name, ProjectPut: api.ProjectPut{Config: map[string]string{"features.profiles": "false", ownership.Key: "lab"}}}, "", nil
}

// The stamp on an instance is tink's own key, not something a person set: plan --extras does not list it.
func TestTheStampIsNotAnExtra(t *testing.T) {
	live := Resource{Config: map[string]string{ownership.Key: "lab", "user.mine": "y"}}
	e := undeclaredExtras(live, Resource{})
	if !reflect.DeepEqual(e.Config, []string{"user.mine"}) {
		t.Errorf("extras = %v, want only the key a person set", e.Config)
	}
	if got := undeclaredConfig(map[string]string{ownership.Key: "lab"}, nil, nil); len(got) != 0 {
		t.Errorf("the stamp on a profile, volume, network or project is an extra: %v", got)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		cfg   map[string]string
		stack string
		state ownership.State
		owner string
	}{
		{nil, "lab", ownership.Unstamped, ""},
		{map[string]string{ownership.Key: ""}, "lab", ownership.Unstamped, ""},
		{map[string]string{ownership.Key: "lab"}, "lab", ownership.Here, "lab"},
		{map[string]string{ownership.Key: "other"}, "lab", ownership.Elsewhere, "other"},
		{map[string]string{ownership.Key: "lab"}, "", ownership.Unknown, ""},
	}
	for _, c := range cases {
		if s, o := ownership.Classify(c.cfg, c.stack); s != c.state || o != c.owner {
			t.Errorf("Classify(%v, %q) = %v, %q; want %v, %q", c.cfg, c.stack, s, o, c.state, c.owner)
		}
	}
}
