// Package secrets is tink's secret store: values encrypted with age, committed beside the
// stack they belong to (docs/secrets-design.md).
//
// The store is a YAML file, secrets.yaml, safe to commit:
//
//	recipients:
//	  - age1...            # public keys: who can decrypt
//	secrets:
//	  immich-db-password: <base64 of an age ciphertext>
//
// Every value is encrypted on its own, to every recipient, so a changed secret is one changed
// line in git and each can be decrypted independently. The matching private key (the identity)
// is never in the repo; see LoadIdentity.
//
// The age ciphertext is plain age: `base64 -d | age -d -i identity.txt` reads a value with no
// tink involved, so the store has an escape hatch.
package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"filippo.io/age"
	yaml "go.yaml.in/yaml/v4"
)

// DefaultFile is the store's name, looked for beside the stack's first YAML file.
const DefaultFile = "secrets.yaml"

// MinLength is the shortest value the store accepts. Redaction replaces every occurrence of a
// secret in tink's output, so a value like "abc" would mangle unrelated text.
const MinLength = 6

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidateName reports whether name can be used as a secret name: it appears in YAML
// (`${secret:NAME}`), in the store, and in messages, so it is kept boring.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("secret name %q must be 1-64 characters of lowercase letters, digits, '.', '_' or '-', starting with a letter or digit", name)
	}
	return nil
}

// Store is the decoded secrets file. The zero value is not usable; use Open.
type Store struct {
	path       string
	recipients []string          // age1... public keys, in file order
	values     map[string]string // name -> base64 of the age ciphertext
}

type fileFormat struct {
	Recipients []string          `yaml:"recipients"`
	Secrets    map[string]string `yaml:"secrets"`
}

// Open reads the store at path. A file that does not exist is an empty store, not an error: the
// first `tink secret set` creates it.
func Open(path string) (*Store, error) {
	s := &Store{path: path, values: map[string]string{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var f fileFormat
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, r := range f.Recipients {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return nil, fmt.Errorf("%s: recipient %q: %w", path, r, err)
		}
	}
	s.recipients = f.Recipients
	for name, v := range f.Secrets {
		if err := ValidateName(name); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if _, err := base64.StdEncoding.DecodeString(v); err != nil {
			return nil, fmt.Errorf("%s: secret %q is not base64 age ciphertext: %w", path, name, err)
		}
		s.values[name] = v
	}
	return s, nil
}

// Path is where the store lives.
func (s *Store) Path() string { return s.path }

// Recipients returns the public keys values are encrypted to.
func (s *Store) Recipients() []string { return append([]string(nil), s.recipients...) }

// Names returns every secret name, sorted.
func (s *Store) Names() []string {
	names := make([]string, 0, len(s.values))
	for n := range s.values {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Has reports whether name is in the store. It needs no identity: whether a secret is set is not
// itself secret.
func (s *Store) Has(name string) bool { _, ok := s.values[name]; return ok }

// Set encrypts value to every recipient and stores it under name. It needs no identity: encrypting
// takes only public keys.
func (s *Store) Set(name, value string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if len(value) < MinLength {
		return fmt.Errorf("secret %q is shorter than %d characters: tink redacts every occurrence of a secret from its output, and a short value would mangle unrelated text", name, MinLength)
	}
	if len(s.recipients) == 0 {
		return errors.New("the store has no recipients to encrypt to: run `tink secret keygen`, then `tink secret recipients add <key>`")
	}
	ct, err := encrypt(value, s.recipients)
	if err != nil {
		return err
	}
	s.values[name] = ct
	return nil
}

// Get decrypts name with ids.
func (s *Store) Get(name string, ids []age.Identity) (string, error) {
	ct, ok := s.values[name]
	if !ok {
		return "", fmt.Errorf("secret %q is not set", name)
	}
	if len(ids) == 0 {
		return "", errors.New("no age identity available to decrypt with")
	}
	return decrypt(ct, ids)
}

// Remove deletes name from the store (a no-op if it is not there).
func (s *Store) Remove(name string) { delete(s.values, name) }

// SetRecipients replaces the recipient list and re-encrypts every value to it, which needs ids to
// decrypt the existing values first. Nothing is changed if any value cannot be re-encrypted.
// Re-encrypting does not unshare a value from a removed recipient's copy of the repository:
// anything they could decrypt before, they still can from git history. Rotate the secrets too.
func (s *Store) SetRecipients(recipients []string, ids []age.Identity) error {
	for _, r := range recipients {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return fmt.Errorf("recipient %q: %w", r, err)
		}
	}
	next := make(map[string]string, len(s.values))
	for name, ct := range s.values {
		if len(ids) == 0 {
			return errors.New("re-encrypting the existing secrets needs an age identity that can decrypt them")
		}
		plain, err := decrypt(ct, ids)
		if err != nil {
			return fmt.Errorf("secret %q: %w", name, err)
		}
		if len(recipients) == 0 {
			return errors.New("a store with secrets needs at least one recipient")
		}
		if next[name], err = encrypt(plain, recipients); err != nil {
			return err
		}
	}
	s.recipients = append([]string(nil), recipients...)
	s.values = next
	return nil
}

// Save writes the store atomically, with names sorted so diffs stay small. The file holds only
// ciphertext and public keys, so it is written 0644 and meant to be committed.
func (s *Store) Save() error {
	f := fileFormat{Recipients: s.recipients, Secrets: s.values}
	body, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.WriteString("# tink secret store: age-encrypted values, safe to commit.\n")
	out.WriteString("# Edit with `tink secret ...`; read one without tink via `base64 -d | age -d -i <identity>`.\n")
	out.Write(body)

	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".secrets-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

func encrypt(value string, recipients []string) (string, error) {
	rs := make([]age.Recipient, 0, len(recipients))
	for _, r := range recipients {
		rec, err := age.ParseX25519Recipient(r)
		if err != nil {
			return "", fmt.Errorf("recipient %q: %w", r, err)
		}
		rs = append(rs, rec)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rs...)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(w, value); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

func decrypt(b64 string, ids []age.Identity) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", err
	}
	r, err := age.Decrypt(bytes.NewReader(raw), ids...)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt with the available identity (is its public key a recipient of this store?): %w", err)
	}
	plain, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
