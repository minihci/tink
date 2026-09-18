package run

import (
	"reflect"
	"testing"
)

func TestBuild_RequiresNameAndImage(t *testing.T) {
	if _, err := Build(Options{Image: "docker-oci:redis:7"}); err == nil {
		t.Error("expected an error when --name is missing")
	}
	if _, err := Build(Options{Name: "redis"}); err == nil {
		t.Error("expected an error when the image is missing")
	}
}

func TestBuild_Env(t *testing.T) {
	spec, err := Build(Options{
		Name:  "n",
		Image: "i",
		Env:   []string{"FOO=bar", "BAZ=qux=extra"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"environment.FOO": "bar",
		"environment.BAZ": "qux=extra", // only the first "=" splits, matching docker
	}
	if !reflect.DeepEqual(spec.Config, want) {
		t.Errorf("Config = %v, want %v", spec.Config, want)
	}
}

func TestBuild_EnvMissingEquals(t *testing.T) {
	if _, err := Build(Options{Name: "n", Image: "i", Env: []string{"NOEQUALS"}}); err == nil {
		t.Error("expected an error for an --env value with no '='")
	}
}

func TestBuild_Cmd(t *testing.T) {
	spec, err := Build(Options{
		Name:  "n",
		Image: "i",
		Cmd:   []string{"--enable-app", "webdav", "--enable-app", "calendar"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "--enable-app webdav --enable-app calendar"
	if got := spec.Config["oci.entrypoint"]; got != want {
		t.Errorf("oci.entrypoint = %q, want %q", got, want)
	}
}

func TestBuild_NoCmdMeansNoEntrypointOverride(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := spec.Config["oci.entrypoint"]; ok {
		t.Error("oci.entrypoint should be unset when no Cmd is given")
	}
}

func TestBuild_Publish(t *testing.T) {
	spec, err := Build(Options{
		Name:    "n",
		Image:   "i",
		Publish: []string{"8080:80"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"type":    "proxy",
		"listen":  "tcp:0.0.0.0:8080",
		"connect": "tcp:127.0.0.1:80",
	}
	if got := spec.Devices["proxy0"]; !reflect.DeepEqual(got, want) {
		t.Errorf("proxy0 device = %v, want %v", got, want)
	}
}

func TestBuild_PublishRejectsNonNumericPorts(t *testing.T) {
	if _, err := Build(Options{Name: "n", Image: "i", Publish: []string{"http:80"}}); err == nil {
		t.Error("expected an error for a non-numeric host port")
	}
	if _, err := Build(Options{Name: "n", Image: "i", Publish: []string{"8080:http"}}); err == nil {
		t.Error("expected an error for a non-numeric container port")
	}
}

func TestBuild_VolumeBindMount(t *testing.T) {
	spec, err := Build(Options{
		Name:   "n",
		Image:  "i",
		Volume: []string{"/host/path:/container/path"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"type":   "disk",
		"source": "/host/path",
		"path":   "/container/path",
	}
	if got := spec.Devices["volume0"]; !reflect.DeepEqual(got, want) {
		t.Errorf("volume0 device = %v, want %v", got, want)
	}
}

func TestBuild_VolumeManagedVolume(t *testing.T) {
	spec, err := Build(Options{
		Name:   "n",
		Image:  "i",
		Pool:   "default",
		Volume: []string{"nextcloud-data:/var/www/html"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{
		"type":   "disk",
		"pool":   "default",
		"source": "nextcloud-data",
		"path":   "/var/www/html",
	}
	if got := spec.Devices["volume0"]; !reflect.DeepEqual(got, want) {
		t.Errorf("volume0 device = %v, want %v", got, want)
	}
}

func TestBuild_Network(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", Network: "incusbr0"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]string{"type": "nic", "network": "incusbr0"}
	if got := spec.Devices["eth0"]; !reflect.DeepEqual(got, want) {
		t.Errorf("eth0 device = %v, want %v", got, want)
	}
}

func TestBuild_Restart(t *testing.T) {
	cases := []struct {
		restart string
		want    string
		wantErr bool
	}{
		{restart: "always", want: "true"},
		{restart: "unless-stopped", want: "true"},
		{restart: "on-failure", want: "true"},
		{restart: "no", want: "false"},
		{restart: "on-failure:5", wantErr: true},
		{restart: "bogus", wantErr: true},
	}
	for _, c := range cases {
		spec, err := Build(Options{Name: "n", Image: "i", Restart: c.restart})
		if c.wantErr {
			if err == nil {
				t.Errorf("--restart=%s: expected an error, got none", c.restart)
			}
			continue
		}
		if err != nil {
			t.Errorf("--restart=%s: unexpected error: %v", c.restart, err)
			continue
		}
		if got := spec.Config["boot.autorestart"]; got != c.want {
			t.Errorf("--restart=%s: boot.autorestart = %q, want %q", c.restart, got, c.want)
		}
	}
}

func TestBuild_ProfilesPassThrough(t *testing.T) {
	spec, err := Build(Options{Name: "n", Image: "i", Profiles: []string{"ingress-shared"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(spec.Profiles, []string{"ingress-shared"}) {
		t.Errorf("Profiles = %v, want [ingress-shared]", spec.Profiles)
	}
}
