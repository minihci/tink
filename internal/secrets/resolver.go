package secrets

import (
	"sync"

	"filippo.io/age"
)

// Resolver is what the rest of tink uses to turn a secret name into a value: the store, the
// identity that decrypts it, and the redactor every decrypted value is registered with.
//
// The identity is loaded on first use, not up front, so a stack that references no secrets never
// needs one. If it cannot be loaded, Has still works (whether a secret is set is not secret) and
// Get explains why it cannot decrypt, which lets `plan` carry on and report instead of failing.
type Resolver struct {
	store        *Store
	identityPath string
	red          *Redactor

	once   sync.Once
	ids    []age.Identity
	idsErr error
}

// NewResolver builds a resolver. identityPath may be empty (see IdentityPath); red may be nil.
func NewResolver(store *Store, identityPath string, red *Redactor) *Resolver {
	return &Resolver{store: store, identityPath: IdentityPath(identityPath), red: red}
}

// Exists reports whether the store file is there at all.
func (r *Resolver) Exists() bool { return r.store.Exists() }

// Where is the store's path, for messages.
func (r *Resolver) Where() string { return r.store.Path() }

// Has reports whether the secret is set in the store.
func (r *Resolver) Has(name string) bool { return r.store.Has(name) }

// Get decrypts the secret and registers the value with the redactor before returning it.
func (r *Resolver) Get(name string) (string, error) {
	r.once.Do(func() { r.ids, r.idsErr = LoadIdentity(r.identityPath) })
	if r.idsErr != nil {
		return "", r.idsErr
	}
	v, err := r.store.Get(name, r.ids)
	if err != nil {
		return "", err
	}
	if r.red != nil {
		r.red.Add(v)
	}
	return v, nil
}
