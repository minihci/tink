package run

import (
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// profileFake answers profile reads from a map; a profile not in it does not exist. Anything else panics through the nil embedded interface.
type profileFake struct {
	incus.InstanceServer
	profiles map[string]map[string]map[string]string // name -> devices
}

func (f *profileFake) GetProfile(name string) (*api.Profile, string, error) {
	devs, ok := f.profiles[name]
	if !ok {
		return nil, "", errNotFound
	}
	return &api.Profile{Name: name, ProfilePut: api.ProfilePut{Devices: devs}}, "", nil
}

var (
	rootOnly  = map[string]map[string]string{"root": {"type": "disk", "pool": "default", "path": "/"}}
	withNIC   = map[string]map[string]string{"root": {"type": "disk", "path": "/"}, "eth0": {"type": "nic", "network": "incusbr0"}}
	published = map[string]map[string]string{"proxy0": {"type": "proxy", "listen": "tcp:0.0.0.0:3101", "connect": "tcp:127.0.0.1:3001"}}
)

func TestPublishedPortsWithNoNICAreRefusedBeforeAnythingIsCreated(t *testing.T) {
	f := &profileFake{profiles: map[string]map[string]map[string]string{"default": rootOnly}}
	_, err := checkNetwork(f, &Spec{Name: "kuma", Devices: published})
	if err == nil {
		t.Fatal("a proxy to an instance with no address can never work, and tron's default profile has no NIC")
	}
	for _, want := range []string{"kuma", "default", "--network"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}

func TestANICFromTheInstanceOrAProfileSatisfiesTheCheck(t *testing.T) {
	f := &profileFake{profiles: map[string]map[string]map[string]string{"default": rootOnly, "lan": withNIC}}
	nic := map[string]map[string]string{"eth0": {"type": "nic", "network": "incusbr0"}, "proxy0": published["proxy0"]}
	if w, err := checkNetwork(f, &Spec{Devices: nic}); err != nil || w != "" {
		t.Errorf("--network given: %q, %v", w, err)
	}
	if w, err := checkNetwork(f, &Spec{Profiles: []string{"lan"}, Devices: published}); err != nil || w != "" {
		t.Errorf("a profile with a NIC: %q, %v", w, err)
	}
	// the default profile is what Incus uses when none is named, so it must be the one looked at
	if _, err := checkNetwork(f, &Spec{Devices: published}); err == nil {
		t.Error("no profile named means default, which has no NIC")
	}
}

func TestNoNICAndNoPortsOnlyWarns(t *testing.T) {
	f := &profileFake{profiles: map[string]map[string]map[string]string{"default": rootOnly}}
	w, err := checkNetwork(f, &Spec{Name: "isolated"})
	if err != nil {
		t.Fatalf("an isolated app is legitimate: %v", err)
	}
	if !strings.Contains(w, "no address") {
		t.Errorf("warning = %q", w)
	}
}

func TestAProfileThatDoesNotExistIsNamed(t *testing.T) {
	f := &profileFake{profiles: map[string]map[string]map[string]string{}}
	if _, err := checkNetwork(f, &Spec{Profiles: []string{"nextcloud-app"}}); err == nil || !strings.Contains(err.Error(), "nextcloud-app") {
		t.Errorf("err = %v", err)
	}
}

func TestCommandLineIsPasteableAndMasksSecrets(t *testing.T) {
	got := CommandLine(Options{
		Name: "kuma", Project: "tink-play", Restart: "unless-stopped", Network: "incusbr0",
		Env:     []string{"TZ=America/Denver", "DB_PASSWORD=hunter2", "MSG=hello world"},
		Publish: []string{"3101:3001"}, Volume: []string{"kuma-data:/app/data"},
		Image: "louislam/uptime-kuma:2", Cmd: []string{"node", "a b"},
	})
	want := `tink run --name kuma --project tink-play --restart unless-stopped --network incusbr0 -e TZ=America/Denver -e 'DB_PASSWORD=***' -e 'MSG=hello world' -p 3101:3001 -v kuma-data:/app/data louislam/uptime-kuma:2 node 'a b'`
	if got != want {
		t.Errorf("\n got: %s\nwant: %s", got, want)
	}
	if strings.Contains(got, "hunter2") {
		t.Error("a secret-looking value must never be recorded")
	}
}

func TestBuildStampsTheCommandOnTheInstance(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", Env: []string{"A=b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Config[KeyCommand]; got != "tink run --name n -e A=b i" {
		t.Errorf("%s = %q", KeyCommand, got)
	}
}

func TestVolumeLinesSayCreatedOrReused(t *testing.T) {
	spec := diskOnPool()
	if got := describeVolumes(&readFake{volErr: errNotFound}, spec); len(got) != 1 || !strings.HasPrefix(got[0], "creating volume default/vol1") {
		t.Errorf("absent: %v", got)
	}
	if got := describeVolumes(&readFake{}, spec); len(got) != 1 || !strings.HasPrefix(got[0], "reusing volume default/vol1") {
		t.Errorf("present: %v", got)
	}
	if got := describeVolumes(&readFake{volErr: errForbidden}, spec); len(got) != 0 {
		t.Errorf("a failed read is ApplyConfig's to report, not a made-up line: %v", got)
	}
}

func qualify(t *testing.T, f *readFake, image string) (ImageQualification, error) {
	t.Helper()
	return Qualify(f, image)
}

func TestABareReferenceIsDockerHubWithAWarningThatSaysHowToBeExplicit(t *testing.T) {
	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	q, err := qualify(t, none, "louislam/uptime-kuma:2")
	if err != nil || q.Image != "docker-oci:louislam/uptime-kuma:2" {
		t.Fatalf("%+v %v", q, err)
	}
	for _, want := range []string{"names no registry", "Docker Hub", "docker.io/louislam/uptime-kuma:2", "docker-oci:louislam/uptime-kuma:2"} {
		if !strings.Contains(q.Warning, want) {
			t.Errorf("warning %q should mention %q", q.Warning, want)
		}
	}
	// an official image lives under library/ on Docker Hub, and the suggestion has to say so
	q, _ = qualify(t, none, "redis:7")
	if !strings.Contains(q.Warning, "docker.io/library/redis:7") {
		t.Errorf("official image: %q", q.Warning)
	}
}

func TestAReferenceTheStackOrTheMachineAlreadyResolvesIsLeftAlone(t *testing.T) {
	for name, f := range map[string]*readFake{
		"a local alias":   {aliasTarget: "abc"},
		"a remote prefix": {aliasErr: errNotFound, imageErr: errNotFound},
		"a failed read":   {aliasErr: errForbidden},
	} {
		in := "haos-x86-64"
		if name == "a remote prefix" {
			in = "docker-oci:redis:7"
		}
		q, err := qualify(t, f, in)
		if err != nil || q.Image != in || q.Warning != "" || q.Note != "" {
			t.Errorf("%s: %+v %v", name, q, err)
		}
	}
}

func TestAReferenceThatNamesARegistryHostUsesTheRemoteForThatHostWithoutAWarning(t *testing.T) {
	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	for in, want := range map[string]string{
		"ghcr.io/advplyr/audiobookshelf:latest": "ghcr:advplyr/audiobookshelf:latest",
		"docker.io/library/redis:7":             "docker-oci:library/redis:7",
		"quay.io/prometheus/node-exporter:v1":   "quay:prometheus/node-exporter:v1",
		"lscr.io/linuxserver/sonarr:latest":     "lscr:linuxserver/sonarr:latest",
	} {
		q, err := qualify(t, none, in)
		if err != nil || q.Image != want || q.Warning != "" {
			t.Errorf("%s -> %+v %v, want %q and no warning", in, q, err, want)
		}
	}
}

func TestAnUnknownRegistryHostIsAnErrorThatSaysHowToAddIt(t *testing.T) {
	none := &readFake{aliasErr: errNotFound, imageErr: errNotFound}
	_, err := qualify(t, none, "registry.example.com/team/app:1")
	if err == nil {
		t.Fatal("sending this to Docker Hub fails with a message about docker.io/registry.example.com/..., which hides the cause")
	}
	for _, want := range []string{"registry.example.com", "incus remote add", "--protocol=oci", "NAME:team/app:1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err, want)
		}
	}
}
