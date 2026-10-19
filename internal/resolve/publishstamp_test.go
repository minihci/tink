package resolve

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

func pub(name, instance, offer string, allow ...string) Resource {
	return Resource{Kind: KindPublish, Name: name, Project: "canary", Instance: instance, Offer: offer, Allow: allow}
}

func stampedStack() []Resource {
	return []Resource{
		{Kind: KindInstance, Name: "whoami", Project: "canary", Image: "docker-oci:traefik/whoami:v1.12.0", Config: map[string]string{
			"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http"}},
		pub("whoami", "whoami", "web", "family"),
		pub("whoami-admin", "whoami", "web", "admins", "family"),
	}
}

func instanceOf(t *testing.T, rs []Resource, name string) Resource {
	t.Helper()
	for _, r := range rs {
		if r.Kind == KindInstance && r.Name == name {
			return r
		}
	}
	t.Fatalf("no instance %q", name)
	return Resource{}
}

func TestAPublishStampIsCompactJSONWithSortedKeysAndGroupsInDeclaredOrder(t *testing.T) {
	cases := []struct {
		p    Resource
		want string
	}{
		{pub("whoami", "whoami", "web", "family"), `{"allow":["family"],"offer":"web"}`},
		// the groups keep the order they were declared in: it is the order the sign-on rule lists them
		{pub("x", "x", "api", "kids", "admins", "family"), `{"allow":["kids","admins","family"],"offer":"api"}`},
		// not HTML-escaped: a group is stamped as it is written
		{pub("x", "x", "web", "a&b"), `{"allow":["a&b"],"offer":"web"}`},
	}
	for _, c := range cases {
		got, err := EncodePublishStamp(c.p)
		if err != nil || got != c.want {
			t.Errorf("stamp = %q, %v; want %q", got, err, c.want)
		}
	}
	if k := PublishStampKey("whoami-admin"); k != "user.tink.publish.whoami-admin" {
		t.Errorf("key = %q", k)
	}
}

func TestAPublishStampReadsBackIntoTheSamePublish(t *testing.T) {
	want := pub("whoami-admin", "whoami", "web", "admins", "family")
	v, err := EncodePublishStamp(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodePublishStamp("canary", "whoami", PublishStampKey("whoami-admin"), v)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back %+v, want %+v", got, want)
	}
	// whitespace between tokens is accepted: a person may have edited it
	if _, err := DecodePublishStamp("canary", "whoami", PublishStampKey("w"), ` { "allow" : [ "family" ] , "offer" : "web" } `); err != nil {
		t.Errorf("whitespace: %v", err)
	}
}

func TestAPublishStampIsReadStrictly(t *testing.T) {
	good := `{"allow":["family"],"offer":"web"}`
	cases := []struct {
		name, key, value, want string
	}{
		{"not JSON", "user.tink.publish.w", `family`, "not a JSON object"},
		{"not an object", "user.tink.publish.w", `["family"]`, "not a JSON object"},
		{"null", "user.tink.publish.w", `null`, "not a JSON object"},
		{"empty", "user.tink.publish.w", ``, "not a JSON object"},
		{"truncated", "user.tink.publish.w", `{"allow":["family"],"offer":`, "not valid JSON"},
		{"something after it", "user.tink.publish.w", good + ` {}`, "more after the JSON object"},
		{"a field that is not one", "user.tink.publish.w", `{"allow":["family"],"offer":"web","instance":"x"}`, `"instance" is not a field`},
		{"names are matched exactly", "user.tink.publish.w", `{"Allow":["family"],"offer":"web"}`, `"Allow" is not a field`},
		{"a field given twice", "user.tink.publish.w", `{"allow":["family"],"offer":"web","offer":"api"}`, `"offer" is given twice`},
		{"no allow", "user.tink.publish.w", `{"offer":"web"}`, `no "allow"`},
		{"no offer", "user.tink.publish.w", `{"allow":["family"]}`, `no "offer"`},
		{"offer is not a string", "user.tink.publish.w", `{"allow":["family"],"offer":7}`, "offer is not a string"},
		{"allow is not a list", "user.tink.publish.w", `{"allow":"family","offer":"web"}`, "allow is not a list of group names"},
		{"allow lists a number", "user.tink.publish.w", `{"allow":[1],"offer":"web"}`, "allow is not a list of group names"},
		// from here on the shape is right and the PUBLISH RULES (publish.go) are what refuse it
		{"nobody may open it", "user.tink.publish.w", `{"allow":[],"offer":"web"}`, "needs allow"},
		{"allow is null", "user.tink.publish.w", `{"allow":null,"offer":"web"}`, "needs allow"},
		{"a typed subject", "user.tink.publish.w", `{"allow":["group:family"],"offer":"web"}`, "plain group names"},
		{"a group twice", "user.tink.publish.w", `{"allow":["family","family"],"offer":"web"}`, "twice"},
		{"a quote in a group", "user.tink.publish.w", `{"allow":["fam'ily"],"offer":"web"}`, "corrupt the sign-on rule"},
		{"an offer name that is not one", "user.tink.publish.w", `{"allow":["family"],"offer":"Web"}`, "is not an offer name"},
		{"an empty offer", "user.tink.publish.w", `{"allow":["family"],"offer":""}`, "needs offer"},
		{"a label that is not a DNS label", "user.tink.publish.Who.Am.I", good, "host label"},
		{"no label", "user.tink.publish.", good, "host label"},
		{"not a stamp key", "user.tink.offer.web.port", good, "is not a publish stamp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := DecodePublishStamp("canary", "whoami", c.key, c.value)
			if err == nil {
				t.Fatalf("accepted %q as %+v", c.value, p)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error = %q, want it to say %q", err, c.want)
			}
		})
	}
}

// ValidatePublish is the load-time check itself, exported: the stamp reader must hold a publish to exactly the rules a document is held to.
func TestValidatePublishIsTheRuleADocumentIsHeldTo(t *testing.T) {
	for _, p := range []Resource{
		pub("Bad_Label", "x", "web", "family"),
		pub("x", "", "web", "family"),
		pub("x", "x", "web"),
		pub("x", "x", "web", "group:family"),
	} {
		doc, stamp := Validate(p), ValidatePublish(p)
		if doc == nil || stamp == nil || doc.Error() != stamp.Error() {
			t.Errorf("%+v: Validate says %v, ValidatePublish says %v", p, doc, stamp)
		}
	}
	if err := ValidatePublish(Resource{Kind: KindInstance, Name: "a"}); err != nil {
		t.Errorf("another kind is not a publish's business: %v", err)
	}
}

func TestPlanStampsEachPublishOntoItsInstanceAndLeavesThePublishesOutOfTheGraph(t *testing.T) {
	rs := stampedStack()
	orig := rs[0].Config
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	var all []Resource
	for _, l := range levels {
		all = append(all, l...)
	}
	for _, r := range all {
		if r.Kind == KindPublish {
			t.Errorf("publish/%s is in the plan graph", r.Name)
		}
	}
	inst := instanceOf(t, all, "whoami")
	wantConfig := map[string]string{
		"user.tink.offer.web.port":       "80",
		"user.tink.offer.web.proto":      "http",
		"user.tink.publish.whoami":       `{"allow":["family"],"offer":"web"}`,
		"user.tink.publish.whoami-admin": `{"allow":["admins","family"],"offer":"web"}`,
	}
	if !reflect.DeepEqual(inst.Config, wantConfig) {
		t.Errorf("desired config = %v\nwant %v", inst.Config, wantConfig)
	}
	// the map the caller declared is not written into: the instance gets a copy
	if len(orig) != 2 {
		t.Errorf("the declared config map was changed in place: %v", orig)
	}
	// planning twice stamps the same keys with the same values
	again, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	if got := instanceOf(t, again[0], "whoami").Config; !reflect.DeepEqual(got, wantConfig) {
		t.Errorf("a second run = %v", got)
	}
}

// With several instances in the stack, each publish lands on the instance it names and on no other.
func TestAPublishIsStampedOnItsOwnInstanceAndOnNoOther(t *testing.T) {
	rs := []Resource{
		{Kind: KindInstance, Name: "a", Project: "canary", Image: "docker-oci:x"},
		{Kind: KindInstance, Name: "b", Project: "canary", Image: "docker-oci:x"},
		{Kind: KindInstance, Name: "c", Project: "other", Image: "docker-oci:x"},
		pub("bee", "b", "web", "family"),
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	var all []Resource
	for _, l := range levels {
		all = append(all, l...)
	}
	for _, name := range []string{"a", "c"} {
		if cfg := instanceOf(t, all, name).Config; len(cfg) != 0 {
			t.Errorf("instance %s carries %v, but nothing was published from it", name, cfg)
		}
	}
	if got := instanceOf(t, all, "b").Config; !reflect.DeepEqual(got, map[string]string{"user.tink.publish.bee": `{"allow":["family"],"offer":"web"}`}) {
		t.Errorf("instance b = %v", got)
	}
}

func TestAStackWithNoPublishStampsNothing(t *testing.T) {
	rs := []Resource{{Kind: KindInstance, Name: "a", Image: "docker-oci:x", Config: map[string]string{"k": "v"}}}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	if got := levels[0][0].Config; !reflect.DeepEqual(got, map[string]string{"k": "v"}) {
		t.Errorf("config = %v", got)
	}
}

func TestAPublishStampsOnlyThePlaceItsInstanceIsDeclaredIn(t *testing.T) {
	cases := []struct {
		name  string
		rs    []Resource
		wants []string
	}{
		{"the instance is not in the stack",
			[]Resource{pub("web", "ghost", "web", "family")},
			[]string{`publish "web"`, `instance "ghost" (project canary) is not in this stack`, "same stack and project"}},
		{"the instance is in another project",
			[]Resource{
				{Kind: KindInstance, Name: "whoami", Project: "other", Image: "docker-oci:x"},
				pub("web", "whoami", "web", "family")},
			[]string{`instance "whoami" (project canary) is not in this stack`, "declared in project other"}},
		{"the publish names no project and the instance is in one",
			[]Resource{
				{Kind: KindInstance, Name: "whoami", Project: "canary", Image: "docker-oci:x"},
				{Kind: KindPublish, Name: "web", Instance: "whoami", Offer: "web", Allow: []string{"family"}}},
			[]string{`instance "whoami" (project default) is not in this stack`, "declared in project canary"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Levels(c.rs)
			if err == nil {
				t.Fatal("a publish whose instance is not beside it was accepted")
			}
			for _, w := range c.wants {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error = %q, want it to say %q", err, w)
				}
			}
		})
	}
}

// The stack's own default project reaches both the publish and its instance (stackproject.go), and then they meet.
func TestThePublishAndItsInstanceMeetInTheStacksDefaultProject(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "s", Project: "canary"},
		{Kind: KindInstance, Name: "whoami", Image: "docker-oci:x"},
		{Kind: KindPublish, Name: "web", Instance: "whoami", Offer: "web", Allow: []string{"family"}},
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := instanceOf(t, levels[0], "whoami").Config["user.tink.publish.web"]; !ok {
		t.Errorf("the stamp is missing: %v", instanceOf(t, levels[0], "whoami").Config)
	}
	// neither names a project and the stack names none: both are in the server's default project
	rs = []Resource{{Kind: KindInstance, Name: "whoami", Image: "docker-oci:x"}, {Kind: KindPublish, Name: "web", Instance: "whoami", Offer: "web", Allow: []string{"family"}}}
	if _, err := Levels(rs); err != nil {
		t.Errorf("both in the default project: %v", err)
	}
}

func TestAnInstanceMayNotDeclareAKeyUnderTheOwnedPrefix(t *testing.T) {
	r := Resource{Kind: KindInstance, Name: "whoami", Image: "docker-oci:x", Config: map[string]string{"user.tink.publish.web": `{"allow":["family"],"offer":"web"}`}}
	err := Validate(r)
	if err == nil || !strings.Contains(err.Error(), "which tink owns") || !strings.Contains(err.Error(), "user.tink.publish.web") {
		t.Errorf("err = %v", err)
	}
	// anything else under user.tink. is the person's to declare
	r.Config = map[string]string{"user.tink.offer.web.port": "80", "user.tink.publisher": "x", "user.tink.publish": "x"}
	if err := Validate(r); err != nil {
		t.Errorf("err = %v", err)
	}
}

// ---- plan and apply: what converges the stamps, and the one place a key is taken back ----

type doneOp struct{ incus.Operation }

func (doneOp) Wait() error { return nil }

// stampServer holds one instance and records the update written to it.
type stampServer struct {
	incus.InstanceServer
	inst    *api.Instance
	updated *api.InstancePut
}

func (s *stampServer) UseProject(string) incus.InstanceServer { return s }

func (s *stampServer) GetInstance(name string) (*api.Instance, string, error) {
	if s.inst == nil || s.inst.Name != name {
		return nil, "", api.StatusErrorf(http.StatusNotFound, "not found")
	}
	c := *s.inst
	c.Config = map[string]string{}
	for k, v := range s.inst.Config {
		c.Config[k] = v
	}
	return &c, "etag", nil
}

func (s *stampServer) UpdateInstance(_ string, put api.InstancePut, _ string) (incus.Operation, error) {
	s.updated = &put
	return doneOp{}, nil
}

func liveWhoami(cfg map[string]string) *api.Instance {
	return &api.Instance{Name: "whoami", Type: "container", InstancePut: api.InstancePut{Config: cfg}}
}

func desiredWhoami(t *testing.T, rs []Resource) Resource {
	t.Helper()
	for i := range rs { // no image, so plan has no registry to ask
		rs[i].Image = ""
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	return instanceOf(t, levels[0], "whoami")
}

func TestPlanConvergesAMissingAndAChangedStampLikeAnyOtherConfigKey(t *testing.T) {
	r := desiredWhoami(t, stampedStack())
	live := map[string]string{
		"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http",
		"user.tink.publish.whoami": `{"allow":["old"],"offer":"web"}`, // changed
		// whoami-admin is missing
	}
	p, err := planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(p.Changes, "\n")
	for _, w := range []string{
		`config.user.tink.publish.whoami: "{\"allow\":[\"old\"],\"offer\":\"web\"}" -> "{\"allow\":[\"family\"],\"offer\":\"web\"}"`,
		`config.user.tink.publish.whoami-admin: "" -> "{\"allow\":[\"admins\",\"family\"],\"offer\":\"web\"}"`,
	} {
		if !strings.Contains(got, w) {
			t.Errorf("changes lack %s:\n%s", w, got)
		}
	}
	if p.Action != ActionUpdate {
		t.Errorf("action = %v, want update", p.Action)
	}
	// converged: the same stamps live, nothing to do
	live["user.tink.publish.whoami"] = `{"allow":["family"],"offer":"web"}`
	live["user.tink.publish.whoami-admin"] = `{"allow":["admins","family"],"offer":"web"}`
	p, err = planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true))
	if err != nil || p.Action != ActionNone || len(p.Changes) != 0 {
		t.Errorf("a converged instance: %+v, %v", p, err)
	}
}

func TestPlanRemovesAStaleStampAndNothingElseUndeclared(t *testing.T) {
	r := desiredWhoami(t, stampedStack())
	live := map[string]string{
		"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http",
		"user.tink.publish.whoami":       `{"allow":["family"],"offer":"web"}`,
		"user.tink.publish.whoami-admin": `{"allow":["admins","family"],"offer":"web"}`,
		"user.tink.publish.retired":      `{"allow":["kids"],"offer":"web"}`, // its publish was deleted from the stack
		// keys the stack does not declare, none of them under the owned prefix: left alone, as ever
		"user.tink.helper.status": "x", "user.tink.role": "app", "user.tink.publisher": "x", "user.tink.publish": "x",
		"user.example.domain": "a.example", "limits.cpu": "2", "boot.autostart": "true",
	}
	p, err := planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{`config.user.tink.publish.retired: "{\"allow\":[\"kids\"],\"offer\":\"web\"}" -> (unset)`}; !reflect.DeepEqual(p.Changes, want) {
		t.Errorf("changes = %q\nwant      %q", p.Changes, want)
	}
	if p.Action != ActionUpdate {
		t.Errorf("a removal is a planned change: action = %v", p.Action)
	}
}

func TestAStackThatNoLongerPublishesAnythingRemovesEveryStampOnItsInstance(t *testing.T) {
	rs := []Resource{{Kind: KindInstance, Name: "whoami", Project: "canary", Config: map[string]string{"user.tink.offer.web.port": "80"}}}
	r := desiredWhoami(t, rs)
	live := map[string]string{"user.tink.offer.web.port": "80", "user.tink.publish.a": "{}", "user.tink.publish.b": "{}", "user.tink.role": "app"}
	p, err := planInstance(&stampServer{inst: liveWhoami(live)}, r, NewPlanOptions(true))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Changes, "|"); got != `config.user.tink.publish.a: "{}" -> (unset)|config.user.tink.publish.b: "{}" -> (unset)` {
		t.Errorf("changes = %s", got)
	}
}

func TestAStampIsNeverAnExtraNorAStaleOneListedAsOne(t *testing.T) {
	declared := Resource{Config: map[string]string{"user.tink.publish.kept": "{}"}}
	live := Resource{Config: map[string]string{"user.tink.publish.kept": "{}", "user.tink.publish.gone": "{}", "limits.cpu": "2"}}
	if got := undeclaredExtras(live, declared).Config; !reflect.DeepEqual(got, []string{"limits.cpu"}) {
		t.Errorf("extras = %v: a stale stamp is a planned removal, not an extra", got)
	}
}

func TestUpdatingTheInstanceSetsTheStampsAndTakesBackOnlyTheStaleOnes(t *testing.T) {
	r := desiredWhoami(t, stampedStack())
	live := map[string]string{
		"user.tink.offer.web.port":  "80",
		"user.tink.publish.whoami":  `{"allow":["old"],"offer":"web"}`,
		"user.tink.publish.retired": `{"allow":["kids"],"offer":"web"}`,
		"user.tink.role":            "app",
		"user.tink.publisher":       "x",
		"user.example.domain":       "a.example",
		"limits.cpu":                "2",
		"volatile.base_image":       "abc",
	}
	s := &stampServer{inst: liveWhoami(live)}
	if err := updateInstance(s, r); err != nil {
		t.Fatal(err)
	}
	if s.updated == nil {
		t.Fatal("nothing was written")
	}
	want := map[string]string{
		"user.tink.offer.web.port": "80", "user.tink.offer.web.proto": "http",
		"user.tink.publish.whoami":       `{"allow":["family"],"offer":"web"}`,
		"user.tink.publish.whoami-admin": `{"allow":["admins","family"],"offer":"web"}`,
		"user.tink.role":                 "app", "user.tink.publisher": "x", "user.example.domain": "a.example", "limits.cpu": "2", "volatile.base_image": "abc",
	}
	if !reflect.DeepEqual(map[string]string(s.updated.Config), want) {
		t.Errorf("written config = %v\nwant %v", s.updated.Config, want)
	}
}

// ---- export: a stack written from the live server must declare the grants it holds, or applying it would take them away ----

func TestExportWritesTheStampsBackAsPublishDocumentsAndVerifies(t *testing.T) {
	inst := kumaInstance()
	inst.Config["user.tink.publish.kuma"] = `{"allow":["family","admins"],"offer":"web"}`
	inst.Config["user.tink.publish.kuma-admin"] = `{"allow":["admins"],"offer":"web"}`
	s := &exportServer{inst: inst, volCfg: map[string]string{}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.YAML, "user.tink.publish.") {
		t.Errorf("the stamp keys must not be written as config (a stack that declares one is refused):\n%s", res.YAML)
	}
	for _, want := range []string{
		"kind: publish\nname: kuma\nproject: tink-play\ninstance: kuma-play\noffer: web\nallow: [family, admins]\n",
		"kind: publish\nname: kuma-admin\nproject: tink-play\ninstance: kuma-play\noffer: web\nallow: [admins]\n",
	} {
		if !strings.Contains(res.YAML, want) {
			t.Errorf("export lacks %q:\n%s", want, res.YAML)
		}
	}
	if !res.Verified {
		t.Errorf("the export derives the stamps the server holds, so it plans as no changes: %+v", res.Plans)
	}
	// and it loads: the publishes come back as publishes, and plan derives the same stamps
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, res.YAML); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, res.YAML)
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range levels {
		for _, r := range l {
			if r.Kind == KindInstance && r.Config["user.tink.publish.kuma"] != `{"allow":["family","admins"],"offer":"web"}` {
				t.Errorf("the exported stack does not derive the stamp: %v", r.Config)
			}
		}
	}
}

func TestExportDoesNotGuessAtAStampThatIsNotAPublish(t *testing.T) {
	inst := kumaInstance()
	inst.Config["user.tink.publish.kuma"] = `{"allow":"family","offer":"web"}`
	s := &exportServer{inst: inst, volCfg: map[string]string{}}
	res, err := Export(s, ExportOptions{Project: "tink-play", Instances: []string{"kuma-play"}, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.YAML, "kind: publish") || strings.Contains(res.YAML, "user.tink.publish") {
		t.Errorf("nothing is written for a stamp that cannot be read:\n%s", res.YAML)
	}
	if n := strings.Join(res.Notes, "\n"); !strings.Contains(n, "user.tink.publish.kuma") || !strings.Contains(n, "would remove it") {
		t.Errorf("a note must name the stamp: %q", n)
	}
	if res.Verified {
		t.Error("applying this stack would remove the stamp, so it is not verified")
	}
}

func TestAStackFileThatDeclaresAStampKeyDoesNotLoad(t *testing.T) {
	path := t.TempDir() + "/tink.yaml"
	if err := writeFile(path, "kind: instance\nname: whoami\nimage: docker-oci:x\nconfig:\n  user.tink.publish.web: '{\"allow\":[\"family\"],\"offer\":\"web\"}'\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "which tink owns") {
		t.Errorf("err = %v", err)
	}
}
