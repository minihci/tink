// Package ownership is what tink means by "this object belongs to that stack": one config key, the name that is stored in it, and the answers a
// reader can get from an object it can see. It imports nothing of tink's, so the resolver, the volume backup code and the daemon helper can all use
// it without any of them importing another.
//
// The design is issue #133 and the notes in docs/ownership.md. In short: every object tink CREATES for a named stack carries `user.tink.stack:
// <stack name>` from the creation request on; an object that was there before is not touched until a person adopts it (`tink plan apply --adopt`);
// and the stamp is a record of who made or took the object, not a lock: nothing here, or in phase 1, decides what may be removed.
//
// The key lives in the object's own `config`, except for an image, where Incus keeps a client's keys in `properties`; the key and the value are the
// same.
package ownership

// Key is the key a stamp is written under, on every kind of object that carries one: project, profile, storage volume, instance, network and network
// ACL (their `config`) and image (its `properties`). It is inside the user.tink.* namespace that every key tink writes on an object lives in.
const Key = "user.tink.stack"

// State is how an object that exists answers "whose is this?" for one stack.
type State int

const (
	// Unknown means the question has no answer: the stack has no name, the object does not exist, or its kind carries no stamp.
	Unknown State = iota
	// Unstamped means the object exists and carries no stamp: it was made before stamps, or by hand, or by another tool.
	Unstamped
	// Here means the object is stamped with this stack's name.
	Here
	// Elsewhere means the object is stamped with the name of another stack.
	Elsewhere
)

func (s State) String() string {
	switch s {
	case Unstamped:
		return "unstamped"
	case Here:
		return "stamped here"
	case Elsewhere:
		return "stamped elsewhere"
	}
	return "unknown"
}

// Owner is the stack name an object's config (or an image's properties) carries, or "" when it carries none.
func Owner(config map[string]string) string {
	return config[Key]
}

// Classify says whose an existing object is, from its own config as it was read (its local config, never an expanded one: an instance's profiles
// would otherwise answer for it), and the name of the stack asking. A stack with no name has no answer: it stamps and adopts nothing. The second
// result is the owner the object names, or "".
func Classify(config map[string]string, stack string) (State, string) {
	if stack == "" {
		return Unknown, ""
	}
	switch owner := Owner(config); {
	case owner == "":
		return Unstamped, ""
	case owner == stack:
		return Here, owner
	default:
		return Elsewhere, owner
	}
}

// With returns a copy of config that carries the stamp for stack, for a creation request. The map it is given is not changed (a resource's config is
// shared by everything that plans and applies it). A stack with no name adds nothing, and config is returned as it is.
func With(config map[string]string, stack string) map[string]string {
	if stack == "" {
		return config
	}
	out := make(map[string]string, len(config)+1)
	for k, v := range config {
		out[k] = v
	}
	out[Key] = stack
	return out
}
