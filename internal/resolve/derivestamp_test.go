package resolve

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func derived(name, instance, path, render string, restart bool) Resource {
	return Resource{Kind: KindFile, Name: name, Project: "site", Instance: instance, Path: path, Derive: render, Restart: restart}
}

func derivedStack() []Resource {
	return []Resource{
		{Kind: KindInstance, Name: "ingress", Project: "site"},
		derived("ingress-routes", "ingress", "/etc/caddy/apps.caddy", DeriveCaddy, true),
		{Kind: KindFile, Name: "ingress-caddyfile", Project: "site", Instance: "ingress", Path: "/etc/caddy/Caddyfile", Content: "x\n"},
	}
}

func TestADerivedFileStampIsCompactJSONWithSortedKeys(t *testing.T) {
	golden, err := os.ReadFile("testdata/derivestamp.golden")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]Resource{
		"dns-hosts":         derived("dns-hosts", "dns", "/hosts", DeriveDNS, false),
		"ingress-routes":    derived("ingress-routes", "ingress", "/etc/caddy/apps.caddy", DeriveCaddy, true),
		"mesh-records":      derived("mesh-records", "mesh", "/etc/headscale/extra_records.json", DeriveMesh, false),
		"sso-configuration": derived("sso-configuration", "sso", "/config/configuration.yml", DeriveSSO, true),
	}
	var got strings.Builder
	for _, name := range []string{"dns-hosts", "ingress-routes", "mesh-records", "sso-configuration"} {
		v, err := EncodeDeriveStamp(files[name])
		if err != nil {
			t.Fatal(err)
		}
		got.WriteString(DeriveStampKey(name) + "=" + v + "\n")
	}
	if got.String() != string(golden) {
		t.Errorf("stamps differ from testdata/derivestamp.golden:\n%s\nwant:\n%s", got.String(), golden)
	}
}

func TestADerivedFileStampReadsBackIntoTheSameFile(t *testing.T) {
	for _, line := range strings.Split(strings.TrimSpace(readGolden(t)), "\n") {
		key, value, _ := strings.Cut(line, "=")
		f, err := DecodeDeriveStamp("site", "inst", key, value)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if f.Name != strings.TrimPrefix(key, DeriveStampPrefix) || f.Project != "site" || f.Instance != "inst" {
			t.Errorf("%s: %+v", key, f)
		}
		again, err := EncodeDeriveStamp(Resource{Name: f.Name, Path: f.Path, Derive: f.Render, Restart: f.Restart})
		if err != nil || again != value {
			t.Errorf("%s: stamped again as %q (%v), want %q", key, again, err, value)
		}
	}
}

func readGolden(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/derivestamp.golden")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestADerivedFileStampIsReadStrictly(t *testing.T) {
	const key = DeriveStampPrefix + "routes"
	cases := []struct{ name, value, want string }{
		{"not an object", `["a"]`, "not a JSON object"},
		{"not json", `{"path":`, "not valid JSON"},
		{"an unknown field", `{"path":"/a","render":"caddy","restart":false,"mode":"0644"}`, `"mode" is not a field`},
		{"a missing field", `{"path":"/a","render":"caddy"}`, `no "restart"`},
		{"a repeated field", `{"path":"/a","path":"/b","render":"caddy","restart":false}`, "given twice"},
		{"text after", `{"path":"/a","render":"caddy","restart":false} x`, "more after"},
		{"case folded name", `{"Path":"/a","render":"caddy","restart":false}`, `"Path" is not a field`},
		{"path not a string", `{"path":1,"render":"caddy","restart":false}`, "path is not a string"},
		{"restart not a bool", `{"path":"/a","render":"caddy","restart":"yes"}`, "restart is not true or false"},
		{"a relative path", `{"path":"etc/a","render":"caddy","restart":false}`, "absolute path"},
		{"a path with a line break", "{\"path\":\"/a\\nb\",\"render\":\"caddy\",\"restart\":false}", "absolute path"},
		{"an unknown render", `{"path":"/a","render":"nginx","restart":false}`, `render "nginx"`},
	}
	for _, c := range cases {
		if _, err := DecodeDeriveStamp("site", "inst", key, c.value); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", c.name, err, c.want)
		}
	}
	if _, err := DecodeDeriveStamp("site", "inst", "user.tink.publish.x", `{}`); err == nil || !strings.Contains(err.Error(), "not a derived file's stamp") {
		t.Errorf("a key outside the prefix: %v", err)
	}
	if _, err := DecodeDeriveStamp("site", "inst", DeriveStampPrefix+"a b", `{"path":"/a","render":"caddy","restart":false}`); err == nil || !strings.Contains(err.Error(), "config key") {
		t.Errorf("a name that is not a key: %v", err)
	}
}

func TestPlanStampsADerivedFileOntoItsInstanceAndKeepsTheFileInTheGraph(t *testing.T) {
	levels, err := Levels(derivedStack())
	if err != nil {
		t.Fatal(err)
	}
	var files int
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindFile {
				files++
			}
		}
	}
	if files != 2 {
		t.Errorf("the files are still resources to converge: found %d", files)
	}
	var inst Resource
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindInstance {
				inst = r
			}
		}
	}
	want := map[string]string{DeriveStampKey("ingress-routes"): `{"path":"/etc/caddy/apps.caddy","render":"caddy","restart":true}`}
	if !reflect.DeepEqual(inst.Config, want) {
		t.Errorf("config = %v, want only the stamp of the file that says derive: %v", inst.Config, want)
	}
}

func TestAFileWhoseInstanceIsNotInTheStackIsLeftAloneAndIsNotAnError(t *testing.T) {
	rs := derivedStack()
	rs[1].Instance = "elsewhere"
	levels, err := Levels(rs)
	if err != nil {
		t.Fatalf("a file on an instance this stack does not declare worked before and must go on working: %v", err)
	}
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindInstance && len(r.Config) != 0 {
				t.Errorf("nothing to stamp, but %s has %v", r.Name, r.Config)
			}
		}
	}
	// and the same name in another project is not the instance
	rs = derivedStack()
	rs[1].Project = "other"
	if levels, err = Levels(rs); err != nil {
		t.Fatal(err)
	}
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindInstance && len(r.Config) != 0 {
				t.Errorf("a file in another project stamped %s: %v", r.Name, r.Config)
			}
		}
	}
}

func TestAFileNamedAsNoConfigKeyCanBeIsRefusedWhenItIsStamped(t *testing.T) {
	rs := derivedStack()
	rs[1].Name = "routes for caddy"
	if _, err := Levels(rs); err == nil || !strings.Contains(err.Error(), "config key") {
		t.Errorf("err = %v", err)
	}
	// a file that does not derive anything has no stamp, so its name is nobody's business
	rs = derivedStack()
	rs[2].Name = "the caddyfile"
	if _, err := Levels(rs); err != nil {
		t.Errorf("a plain file: %v", err)
	}
}

func TestPlanConvergesTheDerivedFileStampAndSecondPlanSaysNoChanges(t *testing.T) {
	rs := derivedStack()
	for i := range rs {
		rs[i].Image = ""
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	var want Resource
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindInstance {
				want = r
			}
		}
	}
	live := map[string]string{}
	srv := func() *stampServer {
		in := liveWhoami(live)
		in.Name = "ingress"
		return &stampServer{inst: in}
	}
	p, err := planInstance(srv(), want, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionUpdate || !strings.Contains(strings.Join(p.Changes, "\n"), "config.user.tink.derive.ingress-routes") {
		t.Fatalf("the missing stamp is a planned change: %+v", p)
	}
	live[DeriveStampKey("ingress-routes")] = want.Config[DeriveStampKey("ingress-routes")]
	if p, err = planInstance(srv(), want, NewPlanOptions(true)); err != nil || p.Action != ActionNone || len(p.Changes) != 0 {
		t.Errorf("a second plan: %+v, %v", p, err)
	}
}

func TestDroppingDeriveRemovesTheStampAndNothingElse(t *testing.T) {
	rs := derivedStack()
	rs[1].Derive = "" // the file stays, it no longer derives
	for i := range rs {
		rs[i].Image = ""
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	var want Resource
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindInstance {
				want = r
			}
		}
	}
	live := map[string]string{
		DeriveStampKey("ingress-routes"): `{"path":"/etc/caddy/apps.caddy","render":"caddy","restart":true}`,
		"user.tink.role":                 "ingress", "user.tink.derive": "x", "limits.cpu": "2", // not under the prefix: left alone
	}
	in := liveWhoami(live)
	in.Name = "ingress"
	p, err := planInstance(&stampServer{inst: in}, want, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Changes, "|"); got != `config.user.tink.derive.ingress-routes: "{\"path\":\"/etc/caddy/apps.caddy\",\"render\":\"caddy\",\"restart\":true}" -> (unset)` {
		t.Errorf("changes = %s", got)
	}
}

func TestAStackFileThatDeclaresADeriveStampKeyDoesNotLoad(t *testing.T) {
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, "kind: instance\nname: ingress\nimage: docker-oci:x\nconfig:\n  user.tink.derive.routes: '{}'\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "which tink owns") || !strings.Contains(err.Error(), "derive:") {
		t.Errorf("err = %v", err)
	}
}

func TestExportDoesNotWriteADerivedFileAndSaysWhy(t *testing.T) {
	inst := kumaInstance()
	inst.Config[DeriveStampKey("routes")] = `{"path":"/etc/caddy/apps.caddy","render":"caddy","restart":false}`
	s := &exportServer{inst: inst, volCfg: map[string]string{}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.YAML, "user.tink.derive") || strings.Contains(res.YAML, "kind: file") {
		t.Errorf("neither the key nor a file is written:\n%s", res.YAML)
	}
	if n := strings.Join(res.Notes, "\n"); !strings.Contains(n, DeriveStampKey("routes")) || !strings.Contains(n, "removes the stamp") {
		t.Errorf("a note must name the stamp: %q", n)
	}
	if res.Verified {
		t.Error("applying this stack would remove the stamp, so it is not verified")
	}
}
