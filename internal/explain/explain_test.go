package explain

import (
	"strings"
	"testing"

	"github.com/minihci/tink/internal/prepare"
	"github.com/minihci/tink/internal/resolve"
)

func app(cfg map[string]string, devs map[string]map[string]string) resolve.Resource {
	return resolve.Resource{Kind: resolve.KindInstance, Name: "app", Project: "p", Image: "x", Config: cfg, Devices: devs}
}

// A pass that makes a resource and changes another is recorded as +, with its fields, and ~, with only the fields it made.
func TestWhatAPassMadeAndChanged(t *testing.T) {
	r := &Recorder{}
	rs := []resolve.Resource{app(map[string]string{"user.tink.offer.web.port": "80"}, nil)}
	r.Before(prepare.PassOwnNetwork, rs)
	rs[0].Devices = map[string]map[string]string{"eth0": {"type": "nic", "network": "tink-blog", "ipv4.address": "10.1.2.2"}}
	rs = append(rs, resolve.Resource{Kind: resolve.KindNetwork, Name: "tink-blog", Config: map[string]string{"ipv4.address": "10.1.2.1/24"}})
	r.After(prepare.PassOwnNetwork, rs, []string{"stack blog: network: own: bridge tink-blog 10.1.2.0/24 (new)"})

	if len(r.Passes) != 1 || len(r.Passes[0].Resources) != 2 {
		t.Fatalf("passes = %+v", r.Passes)
	}
	got := r.Render("blog")
	for _, want := range []string{
		"stack blog, as the passes expanded it",
		"network: own\n  stack blog: network: own: bridge tink-blog 10.1.2.0/24 (new)\n",
		"  ~ instance/app (project p)\n      devices.eth0.ipv4.address: 10.1.2.2\n      devices.eth0.network: tink-blog\n      devices.eth0.type: nic\n",
		"  + network/tink-blog\n      config.ipv4.address: 10.1.2.1/24\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "config.user.tink.offer") {
		t.Errorf("a field the pass did not touch is not listed:\n%s", got)
	}
}

// A value changed shows both sides.
func TestAChangedValueShowsBothSides(t *testing.T) {
	r := &Recorder{}
	rs := []resolve.Resource{app(map[string]string{"limits.memory": "64MiB"}, nil)}
	r.Before(prepare.PassDefaults, rs)
	rs[0].Config = map[string]string{"limits.memory": "128MiB"}
	r.After(prepare.PassDefaults, rs, nil)
	if got := r.Render(""); !strings.Contains(got, "config.limits.memory: 64MiB -> 128MiB") {
		t.Errorf("got:\n%s", got)
	}
}

// A file's content is never printed whole. A derived one shows the region and says where it is; any other shows its size.
func TestFilesShowTheRegionOrASize(t *testing.T) {
	r := &Recorder{}
	derived := resolve.Resource{Kind: resolve.KindFile, Name: "routes", Instance: "ingress", Path: "/r", Derive: resolve.DeriveCaddy,
		Content: "# header\n" + resolve.DeriveBegin + "\n" + resolve.DeriveEnd + "\n"}
	plain := resolve.Resource{Kind: resolve.KindFile, Name: "motd", Instance: "ingress", Path: "/m", Content: "old"}
	rs := []resolve.Resource{derived, plain}
	r.Before(prepare.PassDerivedFiles, rs)
	rs[0].Content = "# header\n" + resolve.DeriveBegin + "\n@a host a.example.org\n" + resolve.DeriveEnd + "\n"
	rs[1].Content = "a longer content than before"
	r.After(prepare.PassDerivedFiles, rs, nil)
	got := r.Render("")
	for _, want := range []string{"content: (the caddy region, below)", "        | @a host a.example.org", "content: (3 bytes) -> (28 bytes)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "# header") {
		t.Errorf("the file around the region is not printed:\n%s", got)
	}
}

// Nothing secret is printed: a value whose key looks like one, or that came from one, is hidden, and the secrets pass is a count.
func TestNothingSecretIsPrinted(t *testing.T) {
	r := &Recorder{}
	rs := []resolve.Resource{app(map[string]string{"environment.DB": "${secret:db}"}, nil)}
	r.Before(prepare.PassSecrets, rs)
	rs[0].Config = map[string]string{"environment.DB": "hunter2"}
	rs[0].SecretKeys = map[string]bool{"environment.DB": true}
	r.After(prepare.PassSecrets, rs, nil)

	r.Before(prepare.PassDefaults, rs)
	rs[0].Config = map[string]string{"environment.DB": "hunter2", "environment.API_TOKEN": "tok-123", "environment.DB_URL": "postgres://x:hunter2@db/x"}
	rs[0].SecretKeys["environment.DB_URL"] = true
	r.After(prepare.PassDefaults, rs, nil)
	got := r.Render("")
	for _, leaked := range []string{"hunter2", "tok-123"} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q is printed:\n%s", leaked, got)
		}
	}
	if !strings.Contains(got, "1 secret reference(s) expanded; the values are not shown") {
		t.Errorf("the secrets pass should be a count:\n%s", got)
	}
	if !strings.Contains(got, "config.environment.API_TOKEN: (value hidden)") {
		t.Errorf("a key that looks like a secret is hidden:\n%s", got)
	}
}

func TestNothingDerived(t *testing.T) {
	r := &Recorder{}
	rs := []resolve.Resource{app(nil, nil)}
	r.Before(prepare.PassDefaults, rs)
	r.After(prepare.PassDefaults, rs, nil)
	if !r.Empty() || !strings.Contains(r.Render("blog"), "nothing was derived") {
		t.Errorf("an unchanged stack: %q", r.Render("blog"))
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{"Config": "config", "DeriveIngress": "derive_ingress", "OnImageChange": "on_image_change", "VM": "vm", "DependsOn": "depends_on", "SecretKeys": "secret_keys"} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}
