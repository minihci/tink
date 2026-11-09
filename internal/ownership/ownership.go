// Package ownership is what tink means by "this object belongs to that stack": one config key, the name that is stored in it, and the answers a
// reader can get from an object it can see. It imports nothing of tink's, so the resolver, the volume backup code and the daemon helper can all use
// it without any of them importing another.
//
// The design is issue #133 and the notes in docs/ownership.md. In short: every object tink CREATES for a named stack carries `user.tink.intent.stack:
// <stack name>` from the creation request on; an object that was there before is not touched until a person adopts it (`tink plan apply --adopt`);
// and the stamp is a record of who made or took the object, not a lock: nothing here, or in phase 1, decides what may be removed.
//
// The key lives in the object's own `config`, except for an image, where Incus keeps a client's keys in `properties`; the key and the value are the
// same.
//
// The package is also the one place that says what class a key under user.tink. is (ClassOf; the issue behind it is #306). There are three, and the
// class is the prefix, so no consumer keeps a table of its own:
//
//   - A fact is written by a person in `config:` and declares something about the object: its role, a leg, an offer, the dns zone. No prefix; the short
//     list is below, and it is the only one.
//   - Intent is written by tink from the declaration, by `plan apply`, under IntentPrefix: what the stack says about the object (which stack it belongs
//     to, what it publishes, which files are derived onto it, its copy policy).
//   - An outcome is written by whatever ran an operation, under OutcomePrefix: what happened (the last copy, a failure, the hash of the triggers an exec
//     saw, the command `tink run` was given, the helper's marker and status).
//
// Every consumer asks the one question: extras report a fact that is on an object and not declared, and never intent or an outcome; `tink export` keeps
// facts and drops the other two; validation refuses intent and outcome in a declaration; a copy of a volume keeps the stamps of what the object IS and
// drops what happened to the one it was copied from; and tink takes back an intent key that nothing derives any more. A new key picks its class by its
// name (cmd/tink's classification test fails for one that has none).
package ownership

import "strings"

// IntentPrefix and OutcomePrefix are what the class of a key is read from.
const (
	IntentPrefix  = "user.tink.intent."
	OutcomePrefix = "user.tink.outcome."
)

// Key is the key a stamp is written under, on every kind of object that carries one: project, profile, storage volume, instance, network and network
// ACL (their `config`) and image (its `properties`). It is intent: which stack the object was made for.
const Key = IntentPrefix + "stack"

// ResourceKey and AppliedKey are two more intent keys, named here so that everything that has to treat them alike (a copy of a volume keeps the first
// and drops the second; a plan never takes either back) can. Writing them is the work of their own issues: nothing writes them yet.
const (
	ResourceKey = IntentPrefix + "resource"
	AppliedKey  = IntentPrefix + "applied"
)

// The facts: the keys a person writes under user.tink. in `config:`. It is the whole list. role and dns.zone are one key each; leg. and offer. own every
// key under them (a leg per NIC; an offer's port, protocol and listen address).
const (
	RoleKey     = "user.tink.role"
	LegPrefix   = "user.tink.leg."
	OfferPrefix = "user.tink.offer."
	DNSZoneKey  = "user.tink.dns.zone"
)

// Class is what a key under user.tink. is.
type Class int

const (
	// Unclassified is every other key: Incus's own, a person's own (`user.note`), and one under user.tink. that is none of the three classes. tink does
	// not judge it.
	Unclassified Class = iota
	// Fact is a key a person declares.
	Fact
	// Intent is a key tink writes from the declaration.
	Intent
	// Outcome is a key written by what ran an operation.
	Outcome
)

func (c Class) String() string {
	switch c {
	case Fact:
		return "fact"
	case Intent:
		return "intent"
	case Outcome:
		return "outcome"
	}
	return "unclassified"
}

// ClassOf says what class a config key (or an image property) is. It is a prefix test and nothing else.
func ClassOf(key string) Class {
	switch {
	case strings.HasPrefix(key, IntentPrefix):
		return Intent
	case strings.HasPrefix(key, OutcomePrefix):
		return Outcome
	case key == RoleKey, key == DNSZoneKey, strings.HasPrefix(key, LegPrefix), strings.HasPrefix(key, OfferPrefix):
		return Fact
	}
	return Unclassified
}

// WrittenByTink reports that a key is intent or an outcome: tink's, and never a person's declaration.
func WrittenByTink(key string) bool {
	c := ClassOf(key)
	return c == Intent || c == Outcome
}

// IsStamp reports that a key is one of the intent keys that are NOT derived into an object's desired config: the owner stamp is written once, in the
// creation request or by adoption, and the other two by passes of their own. A plan never takes one back for being absent from the declaration.
func IsStamp(key string) bool {
	return key == Key || key == ResourceKey || key == AppliedKey
}

// DroppedOnCopy reports that a key must not travel to a volume tink copies (a restore point, a restored volume, a verification's scratch volume): what
// happened to the volume it was copied from (an outcome), the copy policy that says what is to happen to that volume, and the record of what was last
// applied to it. The owner and resource stamps are kept, so a copy reads as a copy of that object. policyKey is the copy policy's key, which the package
// that owns it passes in.
func DroppedOnCopy(key, policyKey string) bool {
	return ClassOf(key) == Outcome || key == policyKey || key == AppliedKey
}

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
