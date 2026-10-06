package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

type keypair struct {
	id  *age.X25519Identity
	pub string
}

func newKey(t *testing.T) keypair {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return keypair{id, id.Recipient().String()}
}

func ids(ks ...keypair) []age.Identity {
	var out []age.Identity
	for _, k := range ks {
		out = append(out, k.id)
	}
	return out
}

func storeWith(t *testing.T, recipients ...keypair) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), DefaultFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recipients {
		s.recipients = append(s.recipients, r.pub)
	}
	return s
}

func TestSetGetRoundTripAndPersistence(t *testing.T) {
	k := newKey(t)
	s := storeWith(t, k)
	if err := s.Set("immich-db-password", "correct-horse-battery"); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(s.Path())
	if strings.Contains(string(raw), "correct-horse") {
		t.Fatalf("the store file contains the plaintext value:\n%s", raw)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("immich-db-password", ids(k))
	if err != nil || got != "correct-horse-battery" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if info, _ := os.Stat(s.Path()); info.Mode().Perm() != 0o644 {
		t.Errorf("store mode = %v; it holds only ciphertext and is meant to be committed", info.Mode().Perm())
	}
}

func TestEveryRecipientCanDecryptAndStrangersCannot(t *testing.T) {
	mac, tron, stranger := newKey(t), newKey(t), newKey(t)
	s := storeWith(t, mac, tron)
	if err := s.Set("x-secret", "a-long-enough-value"); err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]keypair{"mac": mac, "tron": tron} {
		if v, err := s.Get("x-secret", ids(k)); err != nil || v != "a-long-enough-value" {
			t.Errorf("%s: Get = %q, %v", name, v, err)
		}
	}
	if _, err := s.Get("x-secret", ids(stranger)); err == nil || !strings.Contains(err.Error(), "cannot decrypt") {
		t.Errorf("an identity that is not a recipient must fail with a clear error, got %v", err)
	}
	if _, err := s.Get("x-secret", nil); err == nil || !strings.Contains(err.Error(), "no age identity") {
		t.Errorf("no identity: %v", err)
	}
	if _, err := s.Get("absent", ids(mac)); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Errorf("absent secret: %v", err)
	}
}

func TestSetRefusesBadInput(t *testing.T) {
	k := newKey(t)
	s := storeWith(t, k)
	for name, tc := range map[string]struct{ name, value, want string }{
		"short value":     {"ok-name", "abc", "shorter than"},
		"bad name":        {"Has Spaces", "long-enough-value", "must be 1-64"},
		"name with slash": {"a/b", "long-enough-value", "must be 1-64"},
		"empty name":      {"", "long-enough-value", "must be 1-64"},
	} {
		if err := s.Set(tc.name, tc.value); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
	if err := storeWith(t).Set("ok-name", "long-enough-value"); err == nil || !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("setting with no recipients must say how to fix it, got %v", err)
	}
}

func TestSetRecipientsReencryptsAndRevokesFutureAccess(t *testing.T) {
	old, fresh := newKey(t), newKey(t)
	s := storeWith(t, old)
	for _, n := range []string{"a-one", "b-two"} {
		if err := s.Set(n, "value-of-"+n); err != nil {
			t.Fatal(err)
		}
	}
	// add a recipient: the existing values become readable by both
	if err := s.SetRecipients([]string{old.pub, fresh.pub}, ids(old)); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Get("a-one", ids(fresh)); err != nil || v != "value-of-a-one" {
		t.Fatalf("new recipient cannot read after rekey: %q, %v", v, err)
	}
	// drop the old one: it can no longer read the rewritten values
	if err := s.SetRecipients([]string{fresh.pub}, ids(fresh)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("b-two", ids(old)); err == nil {
		t.Error("a removed recipient can still decrypt the re-encrypted value")
	}
	if v, err := s.Get("b-two", ids(fresh)); err != nil || v != "value-of-b-two" {
		t.Errorf("remaining recipient: %q, %v", v, err)
	}
	// without an identity that can decrypt, nothing may change
	before := s.values["a-one"]
	if err := s.SetRecipients([]string{old.pub}, ids(newKey(t))); err == nil {
		t.Fatal("rekey with an identity that cannot decrypt must fail")
	}
	if s.values["a-one"] != before || len(s.recipients) != 1 || s.recipients[0] != fresh.pub {
		t.Error("a failed rekey must leave the store unchanged")
	}
	if err := s.SetRecipients([]string{"not-a-key"}, ids(fresh)); err == nil {
		t.Error("an invalid recipient must be rejected")
	}
}

func TestOpenMissingFileIsEmptyAndMalformedIsAnError(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "nope.yaml"))
	if err != nil || len(s.Names()) != 0 {
		t.Fatalf("a missing store should be empty, got %v, %v", s, err)
	}
	for name, body := range map[string]string{
		"bad yaml":      "recipients: [",
		"bad recipient": "recipients: [age1nope]\n",
		"bad name":      "recipients: []\nsecrets:\n  Bad Name: aGk=\n",
		"not base64":    "recipients: []\nsecrets:\n  good-name: '!!!'\n",
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
		os.WriteFile(p, []byte(body), 0o644)
		if _, err := Open(p); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestSaveIsDeterministicAndSorted(t *testing.T) {
	k := newKey(t)
	s := storeWith(t, k)
	for _, n := range []string{"zeta-secret", "alpha-secret", "mid-secret"} {
		s.Set(n, "value-"+n)
	}
	s.Save()
	first, _ := os.ReadFile(s.Path())
	s2, _ := Open(s.Path())
	s2.Save()
	second, _ := os.ReadFile(s.Path())
	if !bytes.Equal(first, second) {
		t.Error("opening and saving without changes must not change the file (it would churn git)")
	}
	body := string(first)
	if !(strings.Index(body, "alpha-secret") < strings.Index(body, "mid-secret") && strings.Index(body, "mid-secret") < strings.Index(body, "zeta-secret")) {
		t.Errorf("secrets are not sorted by name:\n%s", body)
	}
	if got := s2.Names(); strings.Join(got, ",") != "alpha-secret,mid-secret,zeta-secret" {
		t.Errorf("Names = %v", got)
	}
}

func TestIdentityLifecycleAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "identity.txt")
	if _, err := LoadIdentity(path); !errors.Is(err, ErrNoIdentity) {
		t.Fatalf("a missing identity must be ErrNoIdentity so callers can carry on without secrets, got %v", err)
	}
	pub, err := GenerateIdentity(path)
	if err != nil || !strings.HasPrefix(pub, "age1") {
		t.Fatalf("GenerateIdentity = %q, %v", pub, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("identity mode = %v, want 0600", info.Mode().Perm())
	}
	got, err := LoadIdentity(path)
	if err != nil || len(PublicKeys(got)) != 1 || PublicKeys(got)[0] != pub {
		t.Fatalf("LoadIdentity = %v, %v (want public key %s)", PublicKeys(got), err, pub)
	}
	if _, err := GenerateIdentity(path); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("an existing identity must never be overwritten: %v", err)
	}
	os.Chmod(path, 0o640)
	if _, err := LoadIdentity(path); err == nil || !strings.Contains(err.Error(), "readable by group or others") {
		t.Errorf("a group-readable identity must be refused: %v", err)
	}
}

func TestIdentityPathPrecedence(t *testing.T) {
	t.Setenv("TINK_AGE_IDENTITY", "/from/env")
	if got := IdentityPath("/from/flag"); got != "/from/flag" {
		t.Errorf("the explicit path wins, got %q", got)
	}
	if got := IdentityPath(""); got != "/from/env" {
		t.Errorf("the environment comes next, got %q", got)
	}
	t.Setenv("TINK_AGE_IDENTITY", "")
	if got := IdentityPath(""); !strings.HasSuffix(got, filepath.Join("tink", "identity.txt")) {
		t.Errorf("the default is under the user config dir, got %q", got)
	}
}

func TestGenerate(t *testing.T) {
	for _, tc := range []struct {
		length  int
		charset string
		ok      func(r rune) bool
	}{
		{0, "", func(r rune) bool {
			return r < 128 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		}},
		{40, CharsetAlnum, func(r rune) bool {
			return r < 128 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		}},
		{24, CharsetHex, func(r rune) bool { return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' }},
	} {
		v, err := Generate(tc.length, tc.charset)
		want := tc.length
		if want == 0 {
			want = 32
		}
		if err != nil || len(v) != want {
			t.Fatalf("Generate(%d,%q) = %q, %v", tc.length, tc.charset, v, err)
		}
		for _, r := range v {
			if !tc.ok(r) {
				t.Errorf("Generate(%d,%q) produced %q outside its charset", tc.length, tc.charset, string(r))
			}
		}
	}
	a, _ := Generate(32, "")
	b, _ := Generate(32, "")
	if a == b {
		t.Error("two generated secrets are identical")
	}
	if _, err := Generate(8, ""); err == nil {
		t.Error("a short generated secret must be refused")
	}
	if _, err := Generate(32, "emoji"); err == nil {
		t.Error("an unknown charset must be refused")
	}
}

func TestRedactor(t *testing.T) {
	r := NewRedactor()
	secret := "hunter2-hunter2"
	r.Add(secret)
	r.Add("abc") // too short: must not be registered and must not mangle text
	pem := "-----BEGIN KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n-----END KEY-----"
	r.Add(pem)

	for name, in := range map[string]string{
		"raw":             "password=" + secret + " end",
		"base64 std":      base64.StdEncoding.EncodeToString([]byte(secret)),
		"base64 raw":      base64.RawStdEncoding.EncodeToString([]byte(secret)),
		"base64 url":      base64.URLEncoding.EncodeToString([]byte(secret)),
		"query escaped":   "?p=" + url.QueryEscape(secret),
		"%q formatted":    `config.environment.X: "old" -> "` + secret + `"`,
		"a line of a PEM": "leak: MIIEvQIBADANBgkqhkiG9w0BAQEFAASC",
		"the whole PEM":   pem,
		"repeated":        secret + secret,
	} {
		if got := r.Redact(in); strings.Contains(got, "hunter2") || strings.Contains(got, "MIIEvQ") {
			t.Errorf("%s: %q still contains the secret: %q", name, in, got)
		}
	}
	if got := r.Redact("the abc of things"); got != "the abc of things" {
		t.Errorf("a value below MinLength must not be redacted, got %q", got)
	}
	if got := r.Redact("nothing to see"); got != "nothing to see" {
		t.Errorf("unrelated text changed: %q", got)
	}
	if got := NewRedactor().Redact("anything"); got != "anything" {
		t.Errorf("an empty redactor must pass text through, got %q", got)
	}
}

func TestRedactorPrefersLongestMatch(t *testing.T) {
	r := NewRedactor()
	r.Add("password")
	r.Add("password-with-suffix")
	if got := r.Redact("x password-with-suffix y"); got != "x *** y" {
		t.Errorf("got %q: a secret that contains a shorter one must be replaced whole, not left with its tail", got)
	}
}

func TestRedactingWriterHandlesSplitWritesAndFlush(t *testing.T) {
	r := NewRedactor()
	r.Add("super-secret-value")
	var out bytes.Buffer
	w := r.Writer(&out)
	// a secret split across two writes must still be caught
	w.Write([]byte("key=super-sec"))
	w.Write([]byte("ret-value and more\nsecond line without newline super-secret-value"))
	if strings.Contains(out.String(), "super-sec") {
		t.Fatalf("leaked through a split write: %q", out.String())
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "key=*** and more\nsecond line without newline ***" {
		t.Errorf("got %q", got)
	}
}

func TestResolver(t *testing.T) {
	k := newKey(t)
	s := storeWith(t, k)
	s.Set("db-password", "s3cret-value-123")
	idPath := filepath.Join(t.TempDir(), "id.txt")
	os.WriteFile(idPath, []byte(k.id.String()+"\n"), 0o600)

	red := NewRedactor()
	res := NewResolver(s, idPath, red)
	if !res.Has("db-password") || res.Has("nope") {
		t.Error("Has is wrong")
	}
	if v, err := res.Get("db-password"); err != nil || v != "s3cret-value-123" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if got := red.Redact("leak s3cret-value-123"); strings.Contains(got, "s3cret") {
		t.Errorf("a decrypted value must be registered with the redactor, got %q", got)
	}

	// no identity: Has still works, Get explains
	noid := NewResolver(s, filepath.Join(t.TempDir(), "absent.txt"), nil)
	if !noid.Has("db-password") {
		t.Error("Has must not need an identity")
	}
	if _, err := noid.Get("db-password"); !errors.Is(err, ErrNoIdentity) {
		t.Errorf("Get without an identity must be ErrNoIdentity, got %v", err)
	}
}

func TestNameIsBoundToTheCiphertext(t *testing.T) {
	k := newKey(t)
	s := storeWith(t, k)
	s.Set("db-password", "the-database-password")
	s.Set("api-token", "the-api-token-value")
	// a bad merge or a hand edit swaps the two lines
	s.values["db-password"], s.values["api-token"] = s.values["api-token"], s.values["db-password"]
	for _, n := range []string{"db-password", "api-token"} {
		if v, err := s.Get(n, ids(k)); err == nil || !strings.Contains(err.Error(), "swapped or edited by hand") {
			t.Errorf("%s: swapped ciphertext was accepted (%q, %v): two secrets would be silently exchanged", n, v, err)
		}
	}
	// and rekeying a store whose lines were swapped must not launder the swap into a valid-looking store
	if err := s.SetRecipients(s.Recipients(), ids(k)); err == nil {
		t.Error("rekey of a store with a swapped value must fail, not re-encrypt it under the wrong name")
	}
}

func TestStoreExistsReflectsTheFile(t *testing.T) {
	dir := t.TempDir()
	missing, _ := Open(filepath.Join(dir, "nope.yaml"))
	if missing.Exists() {
		t.Error("a store that is not on disk must say so (a wrong directory is not 'every secret unset')")
	}
	k := newKey(t)
	s := storeWith(t, k)
	s.Set("a-secret", "a-secret-value")
	s.Save()
	reopened, _ := Open(s.Path())
	if !reopened.Exists() {
		t.Error("an existing store must report that it exists")
	}
}

// The store's promise: a value can be read with the plain age tool, no tink involved.
func TestPlainAgeCLICanReadAValue(t *testing.T) {
	if _, err := exec.LookPath("age"); err != nil {
		t.Skip("the age CLI is not installed")
	}
	dir := t.TempDir()
	idPath := filepath.Join(dir, "id.txt")
	pub, err := GenerateIdentity(idPath)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := Open(filepath.Join(dir, DefaultFile))
	s.recipients = []string{pub}
	s.Set("x-secret", "interop-check-value")
	raw, _ := base64.StdEncoding.DecodeString(s.values["x-secret"])
	cmd := exec.Command("age", "-d", "-i", idPath)
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("age -d failed: %v", err)
	}
	if got := strings.TrimPrefix(string(out), "tink:1:x-secret\n"); got != "interop-check-value" {
		t.Errorf("age -d output = %q", out)
	}
}

func TestRedactorCoversHexAndJSONEncodings(t *testing.T) {
	r := NewRedactor()
	secret := `pa<ss>&wörd-1234`
	r.Add(secret)
	for name, in := range map[string]string{
		"hex":       hex.EncodeToString([]byte(secret)),
		"HEX":       strings.ToUpper(hex.EncodeToString([]byte(secret))),
		"json body": `{"password":"pa<ss>&wörd-1234"}`,
	} {
		if got := r.Redact("x " + in + " y"); strings.Contains(got, in) {
			t.Errorf("%s form of the secret survived: %q", name, got)
		}
	}
}
