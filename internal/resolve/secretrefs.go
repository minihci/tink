package resolve

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/minihci/tink/internal/secrets"
)

// Secret references (docs/secrets-design.md): `${secret:NAME}` in the VALUE of an instance's
// `environment.*` config. That is the only place they are accepted. Everywhere else a reference is a
// load-time error, so a secret can never end up somewhere that prints or logs it (an argv, an
// instance name, a file mode-0644 in the guest) by quiet accident.

// SecretSource is what expansion needs from the secret store.
type SecretSource interface {
	// Exists reports whether there is a store at all (a missing one is a wrong directory, not
	// "everything is unset").
	Exists() bool
	// Where names the store, for messages.
	Where() string
	// Has reports whether a secret is set; it needs no identity.
	Has(name string) bool
	// Get decrypts a secret.
	Get(name string) (string, error)
}

// validateSecretRefs refuses a `${secret:` anywhere but an instance's environment.* values, and
// checks the reference syntax where one is allowed. It walks every string-ish field of the
// resource instead of listing the forbidden ones, so a field added later is covered by default.
func validateSecretRefs(r Resource) error {
	reject := func(field, value string) error {
		if secrets.HasRef(value) {
			return fmt.Errorf("resource %q: a secret reference is not allowed in %s: references are only accepted in an instance's environment.* values (anywhere else a secret could be printed, logged, or readable by the whole guest)", r.Name, field)
		}
		return nil
	}
	v := reflect.ValueOf(r)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name, f := t.Field(i).Name, v.Field(i)
		switch f.Kind() {
		case reflect.String:
			if err := reject(name, f.String()); err != nil {
				return err
			}
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				continue
			}
			for j := 0; j < f.Len(); j++ {
				if err := reject(name, f.Index(j).String()); err != nil {
					return err
				}
			}
		case reflect.Map:
			switch f.Type().Elem().Kind() {
			case reflect.String:
				for _, k := range f.MapKeys() {
					val := f.MapIndex(k).String()
					if name == "Config" && r.Kind == KindInstance && strings.HasPrefix(k.String(), "environment.") {
						if err := secrets.CheckTemplate(val); err != nil {
							return fmt.Errorf("resource %q: config %s: %w", r.Name, k.String(), err)
						}
						continue
					}
					if err := reject(fmt.Sprintf("%s[%s]", name, k.String()), val); err != nil {
						return err
					}
				}
			case reflect.Map: // Devices: name -> key -> value
				for _, dev := range f.MapKeys() {
					inner := f.MapIndex(dev)
					for _, k := range inner.MapKeys() {
						if err := reject(fmt.Sprintf("%s[%s].%s", name, dev.String(), k.String()), inner.MapIndex(k).String()); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// NeedsSecrets reports whether any resource holds a secret reference, so a stack with none never
// touches the store or an identity.
func NeedsSecrets(resources []Resource) bool {
	for _, r := range resources {
		for k, v := range r.Config {
			if r.Kind == KindInstance && strings.HasPrefix(k, "environment.") && secrets.HasRef(v) {
				return true
			}
		}
	}
	return false
}

// ExpandSecrets returns resources with every `${secret:NAME}` replaced by the secret's value, and
// every escaped `$${secret:` turned into the literal text. It never modifies its input: a resource
// that needs expanding gets a fresh Config map, so nothing is shared with the caller or with
// another copy.
//
// A resource whose references cannot all be resolved is left unexpanded and marked with
// SecretProblems, and planning reports it BLOCKED as a whole. Half an environment is worse than
// none: Postgres initialises once with whatever it first sees. "Unset" and "set but cannot be
// decrypted" are told apart, because the remedy for the first (`tink secret set`) would overwrite a
// good secret in the second.
//
// Each key that held a reference is recorded in SecretKeys, so no diff prints it.
func ExpandSecrets(resources []Resource, src SecretSource) []Resource {
	out := make([]Resource, len(resources))
	copy(out, resources)
	for i := range out {
		r := &out[i]
		var keys []string
		for k, v := range r.Config {
			if r.Kind == KindInstance && strings.HasPrefix(k, "environment.") && secrets.HasRef(v) {
				keys = append(keys, k)
			}
		}
		if len(keys) == 0 {
			continue
		}

		cfg := make(map[string]string, len(r.Config))
		for k, v := range r.Config {
			cfg[k] = v
		}
		hidden := map[string]bool{}
		for k := range r.SecretKeys {
			hidden[k] = true
		}
		var problems []string
		seen := map[string]bool{}
		for _, k := range keys {
			hidden[k] = true
			expanded, err := secrets.Expand(r.Config[k], func(name string) (string, error) {
				value, problem := resolveSecret(name, src)
				if problem == "" {
					return value, nil
				}
				if !seen[name] {
					seen[name] = true
					problems = append(problems, problem)
				}
				return "", fmt.Errorf("unresolved")
			})
			if err == nil {
				cfg[k] = expanded
			}
		}
		r.SecretKeys = hidden
		r.SecretsExpanded = true
		if len(problems) > 0 {
			r.SecretProblems = problems // Config stays as written: nothing partial is applied
			continue
		}
		r.Config = cfg
	}
	return out
}

// resolveSecret returns the secret's value, or else why it cannot be used.
func resolveSecret(name string, src SecretSource) (value, problem string) {
	if src == nil {
		return "", fmt.Sprintf("secret %q is referenced but no secret store was given", name)
	}
	if !src.Has(name) {
		if !src.Exists() {
			return "", fmt.Sprintf("secret %q is referenced but there is no secret store at %s (wrong directory? or run `tink secret keygen --add`, then `tink secret set %s`)", name, src.Where(), name)
		}
		return "", fmt.Sprintf("secret %q is not set in %s: run `tink secret set %s` (or add --generate)", name, src.Where(), name)
	}
	v, err := src.Get(name)
	if err != nil {
		return "", fmt.Sprintf("secret %q is set in %s but cannot be decrypted here: %v. It is not unset: do not run `tink secret set`, which would overwrite it", name, src.Where(), err)
	}
	return v, ""
}

// unexpandedSecretRef reports a resource that still holds an unexpanded reference, which means
// ExpandSecrets was never run on it. Applying it would push the literal text "${secret:...}" as
// a password, so planning refuses.
func unexpandedSecretRef(r Resource) bool {
	if r.SecretsExpanded {
		return false
	}
	for k, v := range r.Config {
		if r.Kind == KindInstance && strings.HasPrefix(k, "environment.") && secrets.HasRef(v) {
			return true
		}
	}
	return false
}
