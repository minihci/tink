package ingress

// The config keys an instance sets to register with the ingress. Everything tink reads or writes on an object's metadata lives under
// user.tink.*, so one filter finds all of it; these are in that namespace.
const (
	KeyEnabled = "user.tink.ingress.enabled"
	KeyDomain  = "user.tink.ingress.domain"
	KeyPort    = "user.tink.ingress.port"
)

// The names these keys had before the rule was applied to them. They are still read, because instances in other repositories, on hosts that are
// in use, register with them, and a reconciler that stopped seeing them would delete their routes. A key under the new name wins; a notice
// asks for the rename. Nothing may add a key outside user.tink.*: cmd/tink's TestTinkKeysAreUnderTheTinkNamespace holds the line, and this
// is its one exception.
const (
	legacyEnabled = "user.ingress.enabled"
	legacyDomain  = "user.ingress.domain"
	legacyPort    = "user.ingress.port"
)

// setting reads an ingress key, preferring its current name. fromLegacy says the value came from the old name.
func setting(cfg map[string]string, key, legacy string) (value string, fromLegacy bool) {
	if v, ok := cfg[key]; ok {
		return v, false
	}
	v, ok := cfg[legacy]
	return v, ok
}
