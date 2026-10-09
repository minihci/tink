package resolve

import (
	"net/http"
	"os"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/run"
)

// What the image says, and an instance that has copied it (as Incus does at creation) plus a person's changes on top.
var kumaRuntime = ociRuntime{
	Entrypoint: []string{"/usr/bin/dumb-init", "--"},
	Cmd:        []string{"node", "server/server.js"},
	WorkingDir: "/app",
	Env:        []string{"PATH=/usr/bin", "NODE_VERSION=22"},
}

func TestImageDerivedKeysAreTheOnesThatEqualTheImage(t *testing.T) {
	cfg := map[string]string{
		"oci.entrypoint":           "/usr/bin/dumb-init -- node server/server.js", // == Entrypoint + Cmd
		"oci.cwd":                  "/app",
		"oci.uid":                  "0",
		"oci.gid":                  "0",
		"environment.PATH":         "/usr/bin",
		"environment.NODE_VERSION": "20",             // the image says 22: a person changed it
		"environment.TZ":           "America/Denver", // not the image's at all
	}
	got := imageDerivedKeys(cfg, kumaRuntime)
	for _, k := range []string{"oci.entrypoint", "oci.cwd", "oci.uid", "oci.gid", "environment.PATH"} {
		if !got[k] {
			t.Errorf("%s equals what the image says and should be left out of the export", k)
		}
	}
	for _, k := range []string{"environment.NODE_VERSION", "environment.TZ"} {
		if got[k] {
			t.Errorf("%s was written by a person (an override, or not the image's) and must be kept", k)
		}
	}
}

func TestAnOverriddenEntrypointIsKept(t *testing.T) {
	cfg := map[string]string{"oci.entrypoint": "node other.js", "oci.cwd": "/app", "oci.uid": "0", "oci.gid": "0"}
	got := imageDerivedKeys(cfg, kumaRuntime)
	if got["oci.entrypoint"] {
		t.Error("tink run IMAGE CMD overrides oci.entrypoint; the export must keep it")
	}
}

func TestOCIRemoteIsFoundFromTheImageDescription(t *testing.T) {
	env := &imageEnv{conf: &cliconfig.Config{Remotes: map[string]cliconfig.Remote{
		"docker-oci": {Addrs: []string{"https://docker.io"}, Protocol: "oci"},
		"ghcr":       {Addrs: []string{"https://ghcr.io"}, Protocol: "oci"},
		"images":     {Addrs: []string{"https://images.linuxcontainers.org"}, Protocol: "simplestreams"},
	}}}
	if n, _ := ociRemoteFor(env, "docker.io/louislam/uptime-kuma (OCI)"); n != "docker-oci" {
		t.Errorf("docker.io -> %q, want docker-oci", n)
	}
	if n, _ := ociRemoteFor(env, "ghcr.io/home-assistant/home-assistant (OCI)"); n != "ghcr" {
		t.Errorf("ghcr.io -> %q, want ghcr", n)
	}
	if n, _ := ociRemoteFor(env, "registry.example/x (OCI)"); n != "" {
		t.Errorf("an unconfigured registry must not guess a remote, got %q", n)
	}
}

// exportServer is a server holding one project, one volume and one instance: what Export reads, and what the plan that checks it reads.
type exportServer struct {
	incus.InstanceServer
	inst   *api.Instance
	volCfg map[string]string
}

func (s *exportServer) UseProject(string) incus.InstanceServer { return s }

func (s *exportServer) GetInstance(name string) (*api.Instance, string, error) {
	if s.inst == nil || name != s.inst.Name {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	return s.inst, "", nil
}

func (s *exportServer) GetProject(name string) (*api.Project, string, error) {
	return &api.Project{Name: name, ProjectPut: api.ProjectPut{Config: map[string]string{"features.profiles": "false"}}}, "", nil
}

func (s *exportServer) GetStoragePoolVolume(pool, _, name string) (*api.StorageVolume, string, error) {
	return &api.StorageVolume{Name: name, Type: "custom", StorageVolumePut: api.StorageVolumePut{Config: s.volCfg}}, "", nil
}

func kumaInstance() *api.Instance {
	return &api.Instance{
		Name: "kuma-play",
		InstancePut: api.InstancePut{
			Profiles: []string{"default"},
			Config: map[string]string{
				"boot.autorestart":        "true",
				"environment.TZ":          "America/Denver",
				"environment.DB_PASSWORD": "hunter2",
				"image.id":                "louislam/uptime-kuma:2",
				"image.description":       "docker.io/louislam/uptime-kuma (OCI)",
				"volatile.base_image":     "bc73ff04",
				"volatile.container.oci":  "true",
				"volatile.uuid":           "x",
				run.KeyCommand:            "tink run --name kuma-play -e 'DB_PASSWORD=***' louislam/uptime-kuma:2",
			},
			Devices: map[string]map[string]string{
				"eth0":    {"type": "nic", "network": "incusbr0"},
				"proxy0":  {"type": "proxy", "listen": "tcp:0.0.0.0:3101", "connect": "tcp:127.0.0.1:3001"},
				"volume0": {"type": "disk", "pool": "default", "source": "kuma-data", "path": "/app/data"},
			},
		},
	}
}

func TestExportWritesWhatAPersonSetAndChecksItself(t *testing.T) {
	s := &exportServer{inst: kumaInstance(), volCfg: map[string]string{"volatile.idmap.last": "[]"}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	y := res.YAML

	for _, must := range []string{
		"kind: project\nname: tink-play\n",
		"kind: storage-volume\nname: kuma-data\nproject: tink-play\npool: default\n",
		"kind: instance\nname: kuma-play\nproject: tink-play\nimage: docker-oci:louislam/uptime-kuma:2\n",
		"boot.autorestart: \"true\"",
		"environment.TZ: America/Denver",
		"# created by: tink run --name kuma-play",
		"type: proxy",
	} {
		if !strings.Contains(y, must) {
			t.Errorf("export lacks %q:\n%s", must, y)
		}
	}
	for _, mustNot := range []string{"volatile.", "image.id", "hunter2", run.KeyCommand} {
		if strings.Contains(y, mustNot) {
			t.Errorf("export must not contain %q:\n%s", mustNot, y)
		}
	}
	if !strings.Contains(y, "environment.DB_PASSWORD: ${secret:kuma-play-db_password}") {
		t.Errorf("a secret-looking value is written as a reference:\n%s", y)
	}
	if !strings.Contains(strings.Join(res.Notes, "\n"), "tink secret set kuma-play-db_password") {
		t.Errorf("a note must say how to add the secret: %v", res.Notes)
	}
	if !res.Verified {
		t.Errorf("the export must plan as no changes against the server it came from: %+v", res.Plans)
	}
	for _, p := range res.Plans {
		if p.Resource.Config != nil || p.Resource.Devices != nil {
			t.Errorf("a plan returned from Export holds the resource's body, which may hold a secret: %+v", p.Resource)
		}
	}
}

func TestTheExportedYAMLLoadsAsAStack(t *testing.T) {
	s := &exportServer{inst: kumaInstance(), volCfg: map[string]string{}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, res.YAML); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("tink must be able to read what tink export wrote: %v\n%s", err, res.YAML)
	}
	if len(rs) != 3 || rs[0].Kind != KindProject || rs[1].Kind != KindStorageVolume || rs[2].Kind != KindInstance {
		t.Fatalf("want project, volume, instance in that order, got %+v", rs)
	}
	if rs[2].Devices["proxy0"]["listen"] != "tcp:0.0.0.0:3101" {
		t.Errorf("devices did not round-trip: %+v", rs[2].Devices)
	}
}

func TestExportOfAnInstanceThatIsNotThereSaysSo(t *testing.T) {
	s := &exportServer{}
	if _, err := Export(s, ExportOptions{Instances: []string{"ghost"}, Offline: true}); err == nil || !strings.Contains(err.Error(), `"ghost" does not exist`) {
		t.Errorf("err = %v", err)
	}
	if _, err := Export(s, ExportOptions{Offline: true}); err == nil {
		t.Error("exporting nothing is a mistake to catch")
	}
}

func TestRenderedScalarsAreQuotedWhereYAMLWouldReadThemAsSomethingElse(t *testing.T) {
	for _, v := range []string{"true", "3101", "null", "a: b", "", "#x", "tcp:0.0.0.0:3101", "256MiB"} {
		var back string
		if err := yaml.Unmarshal([]byte("k: "+scalar(v)+"\n"), &struct {
			K *string `yaml:"k"`
		}{&back}); err != nil {
			t.Errorf("%q: %v", v, err)
			continue
		}
		if back != v {
			t.Errorf("scalar(%q) reads back as %q", v, back)
		}
	}
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// A volume's settings (size, initial.uid...) are written back as config: and the file loads; --user made them matter (found live: a stack rebuilt from
// an export had root-owned volumes and the app could not open its database).
func TestAVolumeWithConfigExportsAsAFileTinkCanLoad(t *testing.T) {
	s := &exportServer{inst: kumaInstance(), volCfg: map[string]string{"size": "1GiB", "initial.uid": "1000", "volatile.idmap.last": "[]", "user.tink.backup.copy.nas.at": "2026-10-09T00:00:00Z", "user.tink.stack": "s"}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, res.YAML); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("the export must load: %v\n%s", err, res.YAML)
	}
	if got := rs[1].Config; got["initial.uid"] != "1000" || got["size"] != "1GiB" || len(got) != 2 {
		t.Errorf("volume config did not round-trip (and tink's own and Incus's keys must stay out): %v", got)
	}
	if !res.Verified {
		t.Errorf("must plan as no changes: %+v", res.Plans)
	}
}

// Found live exporting a --user 1000:1000 instance: the image names no user, so the group was never compared, and oci.gid was dropped.
func TestAGroupAPersonSetIsKeptWhenTheImageNamesNone(t *testing.T) {
	rt := ociRuntime{Entrypoint: []string{"/app/x"}, WorkingDir: "/app"} // no User
	base := map[string]string{"oci.entrypoint": "/app/x", "oci.cwd": "/app", "oci.uid": "1000"}

	cfg := copyMap(base)
	cfg["oci.gid"] = "1000"
	got := imageDerivedKeys(cfg, rt)
	if got["oci.gid"] {
		t.Error("oci.gid=1000 was set by --user and the image says nothing about a group: it must be kept")
	}
	if got["oci.uid"] {
		t.Error("oci.uid=1000 differs from the image's root and must be kept")
	}

	cfg["oci.gid"] = "0"
	if !imageDerivedKeys(cfg, rt)["oci.gid"] {
		t.Error("the root group is what an image with no user has")
	}
	delete(cfg, "oci.gid")
	if !imageDerivedKeys(cfg, rt)["oci.gid"] {
		t.Error("no oci.gid at all is nothing to write")
	}

	// an image that names the group: equal to it is its own, anything else is an override
	named := ociRuntime{Entrypoint: []string{"/app/x"}, WorkingDir: "/app", User: "1000:1001"}
	cfg = map[string]string{"oci.entrypoint": "/app/x", "oci.cwd": "/app", "oci.uid": "1000", "oci.gid": "1001"}
	if !imageDerivedKeys(cfg, named)["oci.gid"] {
		t.Error("the image's own group is not an override")
	}
	cfg["oci.gid"] = "5"
	if imageDerivedKeys(cfg, named)["oci.gid"] {
		t.Error("a different group is an override")
	}
}

func TestAnEphemeralInstanceKeepsItsFlagThroughExport(t *testing.T) {
	inst := kumaInstance()
	inst.Ephemeral = true
	res, err := Export(&exportServer{inst: inst, volCfg: map[string]string{}}, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.YAML, "\nephemeral: true\n") {
		t.Errorf("export dropped ephemeral:\n%s", res.YAML)
	}
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, res.YAML); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rs[2].Ephemeral {
		t.Errorf("ephemeral did not load: %+v", rs[2])
	}
	if !res.Verified {
		t.Errorf("must plan as no changes: %+v", res.Plans)
	}
	// and a plain instance does not grow the line
	plain, _ := Export(&exportServer{inst: kumaInstance(), volCfg: map[string]string{}}, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if strings.Contains(plain.YAML, "ephemeral") {
		t.Errorf("a persistent instance says nothing about it:\n%s", plain.YAML)
	}
}

func TestPlanSeesAnInstanceThatShouldBeEphemeralAndIsNot(t *testing.T) {
	s := &exportServer{inst: kumaInstance(), volCfg: map[string]string{}} // not ephemeral
	plans, err := PlanWithOptions(s, []Resource{{Kind: KindInstance, Name: "kuma-play", Ephemeral: true}}, PlanOptions{Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if plans[0].Action != ActionUpdate || !strings.Contains(strings.Join(plans[0].Changes, "\n"), "ephemeral: false -> true") {
		t.Errorf("%+v", plans[0])
	}
	// not declaring it changes nothing, as with every other field
	plans, _ = PlanWithOptions(s, []Resource{{Kind: KindInstance, Name: "kuma-play"}}, PlanOptions{Offline: true})
	if plans[0].Action != ActionNone {
		t.Errorf("%+v", plans[0])
	}
}

func TestEphemeralIsOnlyForInstances(t *testing.T) {
	if err := Validate(Resource{Kind: KindStorageVolume, Name: "x", Ephemeral: true}); err == nil {
		t.Error("a volume cannot be ephemeral")
	}
	if err := Validate(Resource{Kind: KindInstance, Name: "x", Image: "docker-oci:alpine:3", Ephemeral: true}); err != nil {
		t.Errorf("an instance can: %v", err)
	}
}
