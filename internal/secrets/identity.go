package secrets

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// IdentityPath is where the age identity (the private key) is looked for: an explicit path (a
// flag), else $TINK_AGE_IDENTITY, else ~/.config/tink/identity.txt. It never lives in the repo.
func IdentityPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("TINK_AGE_IDENTITY"); p != "" {
		return p
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "tink", "identity.txt")
	}
	return "identity.txt"
}

// ErrNoIdentity means no identity file exists at the path looked at. Callers that can carry on
// without secrets (plan on a machine that is not trusted with the keys) test for it.
var ErrNoIdentity = errors.New("no age identity")

// LoadIdentity reads the identity file at path. It refuses a file that group or others can read:
// the whole point of the store is that this file is the one thing guarding every secret.
func LoadIdentity(path string) ([]age.Identity, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w at %s (run `tink secret keygen` to make one)", ErrNoIdentity, path)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("identity file %s is readable by group or others (mode %04o): `chmod 600 %s`", path, info.Mode().Perm(), path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ids, nil
}

// GenerateIdentity makes a new X25519 identity at path (mode 0600) and returns its public key. It
// refuses to overwrite: replacing an identity would lock you out of everything encrypted to it.
func GenerateIdentity(path string) (publicKey string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("%s already exists; refusing to overwrite an identity", path)
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	pub := id.Recipient().String()
	if _, err := fmt.Fprintf(f, "# created: %s\n# public key: %s\n%s\n", time.Now().UTC().Format(time.RFC3339), pub, id.String()); err != nil {
		return "", err
	}
	return pub, nil
}

// PublicKeys returns the public keys of ids, for showing which recipient an identity corresponds to.
func PublicKeys(ids []age.Identity) []string {
	var out []string
	for _, id := range ids {
		if x, ok := id.(*age.X25519Identity); ok {
			out = append(out, x.Recipient().String())
		}
	}
	return out
}

// Charsets a generated secret can use.
const (
	CharsetAlnum = "alnum"
	CharsetHex   = "hex"
)

const (
	alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	hexch = "0123456789abcdef"
)

// MinGeneratedLength is the shortest generated secret: a short random value is a weak one.
const MinGeneratedLength = 16

// Generate returns a random secret of length characters from charset (default alnum), using
// crypto/rand and no modulo bias.
func Generate(length int, charset string) (string, error) {
	var alphabet string
	switch charset {
	case "", CharsetAlnum:
		alphabet = alnum
	case CharsetHex:
		alphabet = hexch
	default:
		return "", fmt.Errorf("unknown charset %q (alnum or hex)", charset)
	}
	if length == 0 {
		length = 32
	}
	if length < MinGeneratedLength {
		return "", fmt.Errorf("generated length %d is too short (minimum %d)", length, MinGeneratedLength)
	}
	var b strings.Builder
	max := big.NewInt(int64(len(alphabet)))
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}
