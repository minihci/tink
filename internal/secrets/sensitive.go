package secrets

import (
	"regexp"
	"sort"
	"strings"
)

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// sensitiveWords are whole words in a config key (split on anything that is not a letter or digit,
// so DB_PASSWORD and db-password both qualify) that mark its value as one not to print.
var sensitiveWords = map[string]bool{
	"pass": true, "passwd": true, "password": true, "passphrase": true,
	"secret": true, "secrets": true, "token": true, "tokens": true,
	"key": true, "keys": true, "apikey": true, "credential": true, "credentials": true, "auth": true,
}

var sensitiveSuffixes = []string{"password", "passwd", "passphrase", "secret", "token", "apikey"}

// SensitiveKey reports whether a config key's NAME suggests its value is a secret
// (environment.POSTGRES_PASSWORD, environment.API_TOKEN, ...). It is defense in depth: tink prints
// no value for such a key, whether or not it came from a ${secret:} reference, because a password
// someone put in plainly is exactly what a diff must not echo. It is a guess about names, so it
// errs towards hiding; the cost of a false positive is "(value hidden)" in a diff.
func SensitiveKey(key string) bool {
	for _, w := range strings.Fields(nonAlnum.ReplaceAllString(strings.ToLower(key), " ")) {
		if sensitiveWords[w] {
			return true
		}
		for _, s := range sensitiveSuffixes {
			if strings.HasSuffix(w, s) {
				return true
			}
		}
	}
	return false
}

// HiddenValue is what is printed instead of a sensitive value.
const HiddenValue = "(value hidden)"

// MaskedConfig returns a copy of cfg that is safe to print: values of sensitive keys are replaced.
func MaskedConfig(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if SensitiveKey(k) {
			v = HiddenValue
		}
		out[k] = v
	}
	return out
}

// SortedKeys returns the keys of m in order, for stable printing.
func SortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
