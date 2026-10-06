package resolve

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// fakeSource is a SecretSource with a few secrets, one of which cannot be decrypted.
type fakeSource struct {
	exists bool
	set    map[string]string // name -> value
	broken map[string]bool   // set, but Get fails
	gets   map[string]int
}

func (f *fakeSource) Exists() bool  { return f.exists }
func (f *fakeSource) Where() string { return "secrets.yaml" }
func (f *fakeSource) Has(n string) bool {
	_, ok := f.set[n]
	return ok || f.broken[n]
}
func (f *fakeSource) Get(n string) (string, error) {
	if f.gets == nil {
		f.gets = map[string]int{}
	}
	f.gets[n]++
	if f.broken[n] {
		return "", errors.New("identity did not match any of the recipients")
	}
	v, ok := f.set[n]
	if !ok {
		return "", errors.New("not set")
	}
	return v, nil
}

func envInstance(name string, env map[string]string) Resource {
	cfg := map[string]string{"limits.memory": "1GiB"}
	for k, v := range env {
		cfg["environment."+k] = v
	}
	return Resource{Kind: KindInstance, Name: name, Config: cfg}
}

func TestExpandSecretsExpandsAndRecordsProvenance(t *testing.T) {
	src := &fakeSource{exists: true, set: map[string]string{"db-password": "pw-1234-abcd"}}
	in := []Resource{
		envInstance("db", map[string]string{"POSTGRES_PASSWORD": "${secret:db-password}", "TZ": "UTC"}),
		envInstance("plain", map[string]string{"TZ": "UTC"}),
	}
	out := ExpandSecrets(in, src)

	if got := out[0].Config["environment.POSTGRES_PASSWORD"]; got != "pw-1234-abcd" {
		t.Fatalf("not expanded: %q", got)
	}
	if out[0].Config["environment.TZ"] != "UTC" || out[0].Config["limits.memory"] != "1GiB" {
		t.Errorf("other keys must be untouched: %v", out[0].Config)
	}
	if !out[0].SecretKeys["environment.POSTGRES_PASSWORD"] || out[0].SecretKeys["environment.TZ"] {
		t.Errorf("provenance must name exactly the keys that held a reference: %v", out[0].SecretKeys)
	}
	if !out[0].SecretsExpanded || len(out[0].SecretProblems) != 0 {
		t.Errorf("expanded=%v problems=%v", out[0].SecretsExpanded, out[0].SecretProblems)
	}
	// the caller's resources must not change, and nothing may be shared with the copy
	if in[0].Config["environment.POSTGRES_PASSWORD"] != "${secret:db-password}" {
		t.Errorf("ExpandSecrets modified its input: %v", in[0].Config)
	}
	out[0].Config["environment.TZ"] = "mutated"
	if in[0].Config["environment.TZ"] != "UTC" {
		t.Error("the expanded resource shares its Config map with the input")
	}
	if out[1].SecretsExpanded || out[1].SecretKeys != nil {
		t.Errorf("a resource with no references must be left alone: %+v", out[1])
	}
}

func TestExpandSecretsBlocksTheWholeResourceAndSaysWhy(t *testing.T) {
	src := &fakeSource{
		exists: true,
		set:    map[string]string{"good-one": "value-of-good"},
		broken: map[string]bool{"locked-one": true},
	}
	r := envInstance("app", map[string]string{
		"A": "${secret:good-one}", "B": "${secret:missing-one}", "C": "${secret:locked-one}", "D": "${secret:missing-one}",
	})
	out := ExpandSecrets([]Resource{r}, src)[0]

	joined := strings.Join(out.SecretProblems, "\n")
	if !strings.Contains(joined, `"missing-one" is not set in secrets.yaml: run `+"`tink secret set missing-one`") {
		t.Errorf("an unset secret must say how to set it:\n%s", joined)
	}
	if !strings.Contains(joined, `"locked-one" is set in secrets.yaml but cannot be decrypted`) ||
		!strings.Contains(joined, "do not run `tink secret set`") {
		t.Errorf("a set-but-undecryptable secret must NOT suggest `secret set` (it would overwrite it):\n%s", joined)
	}
	if strings.Count(joined, "missing-one") != 2 || len(out.SecretProblems) != 2 {
		t.Errorf("each unresolved secret is reported once, got %d problems:\n%s", len(out.SecretProblems), joined)
	}
	// nothing partial: not even the resolvable key was expanded
	if out.Config["environment.A"] != "${secret:good-one}" {
		t.Errorf("a blocked resource must keep its Config as written, got %q", out.Config["environment.A"])
	}
}

func TestExpandSecretsDistinguishesNoStoreFromUnset(t *testing.T) {
	out := ExpandSecrets([]Resource{envInstance("a", map[string]string{"X": "${secret:some-secret}"})}, &fakeSource{exists: false})[0]
	if !strings.Contains(strings.Join(out.SecretProblems, ""), "there is no secret store at secrets.yaml (wrong directory?") {
		t.Errorf("a missing store must not read as 'unset': %v", out.SecretProblems)
	}
	out = ExpandSecrets([]Resource{envInstance("a", map[string]string{"X": "${secret:some-secret}"})}, nil)[0]
	if len(out.SecretProblems) != 1 || !strings.Contains(out.SecretProblems[0], "no secret store was given") {
		t.Errorf("a nil source must block, not panic: %v", out.SecretProblems)
	}
}

func TestExpandSecretsEscapesAndNeverTouchesTheStoreForThem(t *testing.T) {
	src := &fakeSource{exists: true}
	out := ExpandSecrets([]Resource{envInstance("a", map[string]string{"TEMPLATE": "literal $${secret:not-a-ref}"})}, src)[0]
	if got := out.Config["environment.TEMPLATE"]; got != "literal ${secret:not-a-ref}" {
		t.Errorf("an escaped opener must become literal text, got %q", got)
	}
	if len(src.gets) != 0 || len(out.SecretProblems) != 0 || !out.SecretsExpanded {
		t.Errorf("an escape needs no secret: gets=%v problems=%v expanded=%v", src.gets, out.SecretProblems, out.SecretsExpanded)
	}
	// and the guard must not mistake that literal for an unexpanded reference
	if unexpandedSecretRef(out) {
		t.Error("an expanded resource holding a literal ${secret: must not be flagged")
	}
}

func TestExpandSecretsDecryptsEachSecretOncePerReferenceSite(t *testing.T) {
	src := &fakeSource{exists: true, set: map[string]string{"shared-one": "value-shared"}}
	ExpandSecrets([]Resource{envInstance("a", map[string]string{"X": "${secret:shared-one}"})}, src)
	if src.gets["shared-one"] != 1 {
		t.Errorf("decrypted %d times; once is enough", src.gets["shared-one"])
	}
}

func TestNeedsSecrets(t *testing.T) {
	if NeedsSecrets([]Resource{envInstance("a", map[string]string{"TZ": "UTC"})}) {
		t.Error("a stack without references must not need a store or an identity")
	}
	if !NeedsSecrets([]Resource{envInstance("a", map[string]string{"P": "x${secret:y-secret}"})}) {
		t.Error("a reference means secrets are needed")
	}
	if !NeedsSecrets([]Resource{envInstance("a", map[string]string{"P": "$${secret:y-secret}"})}) {
		t.Error("an escape still needs expansion (to unescape), though not the store: NeedsSecrets must say so")
	}
}

func TestValidateSecretRefsIsAnAllowlist(t *testing.T) {
	ref := "${secret:db-password}"
	ok := []Resource{
		{Kind: KindInstance, Name: "i", Config: map[string]string{"environment.PW": ref}},
		{Kind: KindInstance, Name: "i", Config: map[string]string{"environment.PW": "postgres://u:" + ref + "@h/db"}},
	}
	for _, r := range ok {
		if err := Validate(r); err != nil {
			t.Errorf("%v should be allowed: %v", r.Config, err)
		}
	}
	bad := map[string]Resource{
		"image":                 {Kind: KindInstance, Name: "i", Image: "x:" + ref},
		"the name":              {Kind: KindInstance, Name: "i-" + ref},
		"a non-environment key": {Kind: KindInstance, Name: "i", Config: map[string]string{"limits.memory": ref}},
		"oci.entrypoint (argv)": {Kind: KindInstance, Name: "i", Config: map[string]string{"oci.entrypoint": "run " + ref}},
		"user.* (devlxd)":       {Kind: KindInstance, Name: "i", Config: map[string]string{"user.pw": ref}},
		"cloud-init":            {Kind: KindInstance, Name: "i", Config: map[string]string{"cloud-init.user-data": ref}},
		"a profile's config":    {Kind: KindProfile, Name: "p", Config: map[string]string{"environment.PW": ref}},
		"a project's config":    {Kind: KindProject, Name: "p", Config: map[string]string{"environment.PW": ref}},
		"a device":              {Kind: KindInstance, Name: "i", Devices: map[string]map[string]string{"d": {"type": "disk", "source": ref}}},
		"file content":          {Kind: KindFile, Name: "f", Instance: "i", Path: "/x", Content: "pw=" + ref},
		"exec argv":             {Kind: KindExec, Name: "e", Instance: "i", Command: []string{"sh", "-c", "echo " + ref}, Check: []string{"true"}},
		"incus argv":            {Kind: KindIncus, Name: "x", Check: []string{"image", "list"}, Command: []string{"config", "set", ref}},
		"depends_on":            {Kind: KindInstance, Name: "i", DependsOn: []string{ref}},
		"an escaped opener too": {Kind: KindFile, Name: "f", Instance: "i", Path: "/x", Content: "$" + ref},
	}
	for name, r := range bad {
		err := Validate(r)
		if err == nil || !strings.Contains(err.Error(), "secret reference is not allowed") {
			t.Errorf("%s: a reference must be rejected, got %v", name, err)
		}
	}
	// syntax errors inside the one allowed place are caught at load too
	for _, v := range []string{"${secret:unterminated", "${secret:Bad Name}"} {
		if err := Validate(Resource{Kind: KindInstance, Name: "i", Config: map[string]string{"environment.X": v}}); err == nil {
			t.Errorf("%q: a malformed reference must be rejected at load", v)
		}
	}
}

func TestLoadFileRejectsAReferenceInFileContentFromSourcePath(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := dir + "/" + name
		if err := osWriteFile(p, body); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("pw.conf", "password=${secret:db-password}\n")
	p := write("tink.yaml", "kind: instance\nname: i\n---\nkind: file\nname: f\ninstance: i\npath: /etc/x\nsource_path: pw.conf\n")
	if _, err := LoadFile(p); err == nil || !strings.Contains(err.Error(), "secret reference is not allowed") {
		t.Errorf("a reference arriving through source_path must be rejected like inline content, got %v", err)
	}
}

func TestPlanRefusesAnUnexpandedReferenceAndABlockedSecret(t *testing.T) {
	// no server needed: these are decided before anything is read from Incus
	r := envInstance("db", map[string]string{"PW": "${secret:db-password}"})
	if p, err := planOne(nil, r, PlanOptions{}); err != nil || p.Action != ActionBlocked || !strings.Contains(strings.Join(p.Blocked, ""), "never expanded") {
		t.Errorf("an unexpanded reference must BLOCK rather than be pushed as the literal text: %+v, %v", p, err)
	}
	blocked := ExpandSecrets([]Resource{r}, &fakeSource{exists: true})[0]
	p, err := planOne(nil, blocked, PlanOptions{})
	if err != nil || p.Action != ActionBlocked || !strings.Contains(strings.Join(p.Blocked, ""), "is not set") {
		t.Errorf("an unresolved secret must BLOCK with the reason: %+v, %v", p, err)
	}
}

func TestExpandedSecretIsNeverPrintedByTheDiff(t *testing.T) {
	const value = "pw-1234-abcd-5678"
	r := ExpandSecrets([]Resource{envInstance("db", map[string]string{"INNOCENTLY_NAMED": "${secret:db-password}"})},
		&fakeSource{exists: true, set: map[string]string{"db-password": value}})[0]
	// a key with a harmless name: only provenance can hide it
	changes := diffConfig(map[string]string{"environment.INNOCENTLY_NAMED": "old-plain-1111"}, r.Config, r.SecretKeys)
	if joined := strings.Join(changes, "\n"); strings.Contains(joined, value) || strings.Contains(joined, "old-plain") {
		t.Errorf("the diff printed a secret or the value it replaces:\n%s", joined)
	}
}

func osWriteFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }
