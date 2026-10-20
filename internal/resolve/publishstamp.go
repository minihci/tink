package resolve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/minihci/tink/internal/run"
	"github.com/minihci/tink/internal/secrets"
)

// A `kind: publish` is a declaration that stays out of the plan graph (publish.go). It is also STAMPED onto the instance it governs, so that whatever acts
// for the site later (the daemon helper) can read the declarations from the SERVER and does not need the stack files:
//
//	user.tink.publish.<name>: {"allow":["family"],"offer":"web"}
//
// <name> is the publish's host label. The value is compact JSON with its keys sorted and the groups in the order the publish declares them, so the same
// stack always stamps the same bytes (a re-plan reads "no changes"). It names no instance (the key is on it) and holds no secret: a publish holds group
// names only.
//
// The stamps are DERIVED, not declared: derivePublishStamps (below) adds them to the desired config of the instance in the same stack and project, and the
// ordinary instance plan and apply converge them (create, update), with no apply code of their own. Only a publish in the same stack and project as its
// instance can be stamped, because a grant sits next to the thing it governs and the server holds it there.
//
// The prefix is OWNED by tink, and that is the one place tink takes a config key back. Everywhere else a key that is no longer declared is left on the
// instance (docs/plan-extras.md; removing them is proposed in #138), but a stamp that stays after its publish is deleted would keep the name rendered: it would go on being
// routed and let in. So when planning an instance that the stack declares, a live key under the prefix that the stack no longer derives is reported as a
// removal and removed on apply (run.StaleOwnedKeys). Only that prefix, and only on instances the stack declares: any other key, tink's or not, is untouched.
const PublishStampPrefix = "user.tink.publish."

// ownedConfigPrefixes are the config key prefixes tink owns on a stack's instances (see PublishStampPrefix).
var ownedConfigPrefixes = []string{PublishStampPrefix}

// publishStamp is the stamped value. Its fields are in key order, which is the order the JSON is written in.
type publishStamp struct {
	Allow []string `json:"allow"`
	Offer string   `json:"offer"`
}

// PublishStampKey is the config key a publish with this host label is stamped under.
func PublishStampKey(label string) string { return PublishStampPrefix + label }

// ValidatePublish applies the rules a `kind: publish` document is held to at load time to a publish that did not come from a file, such as one read back from
// a stamp. It is validatePublish, exported, so the two cannot drift apart. A resource of another kind passes.
func ValidatePublish(r Resource) error { return validatePublish(r) }

// EncodePublishStamp is the value stamped for a publish: {"allow":[...],"offer":"..."}, compact, keys sorted, groups in declared order.
func EncodePublishStamp(p Resource) (string, error) {
	allow := p.Allow
	if allow == nil {
		allow = []string{} // never "null"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a group name is stamped as it is written, not as &
	if err := enc.Encode(publishStamp{Allow: allow, Offer: p.Offer}); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// DecodePublishStamp reads one stamp back into the publish it was made from. key is the whole config key (it must start with PublishStampPrefix), value is
// what is stored under it, and project and instance say where it was found. It is STRICT: the value must be one JSON object with exactly the keys allow and
// offer, each once, nothing after it, and the result must pass ValidatePublish. Anything else is an error that says what, and never a publish that is only
// nearly what was stamped: a name that is half-read would be half-rendered.
func DecodePublishStamp(project, instance, key, value string) (Resource, error) {
	label, ok := strings.CutPrefix(key, PublishStampPrefix)
	if !ok {
		return Resource{}, fmt.Errorf("%s is not a publish stamp (a stamp is %s<name>)", key, PublishStampPrefix)
	}
	raw, err := strictObject(value)
	if err != nil {
		return Resource{}, fmt.Errorf("%s: %w", key, err)
	}
	for k := range raw {
		if k != "allow" && k != "offer" {
			return Resource{}, fmt.Errorf("%s: %q is not a field of a publish stamp (known: allow, offer)", key, k)
		}
	}
	for _, k := range []string{"allow", "offer"} {
		if _, ok := raw[k]; !ok {
			return Resource{}, fmt.Errorf("%s: the stamp has no %q", key, k)
		}
	}
	p := Resource{Kind: KindPublish, Name: label, Project: project, Instance: instance}
	if err := json.Unmarshal(raw["offer"], &p.Offer); err != nil {
		return Resource{}, fmt.Errorf("%s: offer is not a string: %w", key, err)
	}
	if err := json.Unmarshal(raw["allow"], &p.Allow); err != nil {
		return Resource{}, fmt.Errorf("%s: allow is not a list of group names: %w", key, err)
	}
	if err := ValidatePublish(p); err != nil {
		return Resource{}, fmt.Errorf("%s: %w", key, err)
	}
	return p, nil
}

// strictObject splits a JSON object into its fields. Unlike json.Unmarshal into a struct it matches names exactly (no case folding), refuses a repeated name
// and refuses anything after the object.
func strictObject(s string) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(strings.NewReader(s))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("the value is not a JSON object")
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("the value is not valid JSON: %w", err)
		}
		name, _ := tok.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("the value is not valid JSON: %w", err)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("%q is given twice", name)
		}
		out[name] = v
	}
	if _, err := dec.Token(); err != nil { // the closing brace
		return nil, fmt.Errorf("the value is not valid JSON: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("there is more after the JSON object")
	}
	return out, nil
}

// validateOwnedConfig rejects an instance that declares a key under a prefix tink owns. Such a key would be overwritten by the derived stamp or removed as
// stale, so declaring it could only mislead.
func validateOwnedConfig(r Resource) error {
	if r.Kind != KindInstance {
		return nil
	}
	keys := make([]string, 0)
	for k := range r.Config {
		for _, p := range ownedConfigPrefixes {
			if strings.HasPrefix(k, p) {
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	return fmt.Errorf("resource %q: config %s is under %s, which tink owns: it writes those keys from the kind: publish documents of the stack and removes the ones no publish derives, so declare the publish and not the key", r.Name, keys[0], PublishStampPrefix)
}

// derivePublishStamps adds, to the desired config of every instance a publish names, the stamp of each of its publishes. It changes the caller's resources in
// place, like the other passes over them (applyStackProject, inheritProject), by giving the instance a COPY of its config map that carries the stamps, and
// is safe to run twice: a second run stamps the same keys with the same values.
//
// A publish whose instance is not in the same stack and project is an error. Levels, which calls this, is what `plan` and `plan apply` run first, so it is a
// plan error and not a load error: `tink join FILE...` reads publishes and instances across stacks and is not held to this.
func derivePublishStamps(resources []Resource) error {
	type loc struct{ project, name string }
	at := func(project string) string {
		if project == "" {
			return "default"
		}
		return project
	}
	instances := map[loc]int{}
	otherProjects := map[string][]string{} // instance name -> the projects it is declared in, for the message
	for i, r := range resources {
		if r.Kind == KindInstance {
			instances[loc{at(r.Project), r.Name}] = i
			otherProjects[r.Name] = append(otherProjects[r.Name], at(r.Project))
		}
	}
	stamps := map[int]map[string]string{}
	var order []int
	for _, p := range resources {
		if p.Kind != KindPublish {
			continue
		}
		i, ok := instances[loc{at(p.Project), p.Instance}]
		if !ok {
			hint := ""
			if ps := otherProjects[p.Instance]; len(ps) > 0 {
				sort.Strings(ps)
				hint = fmt.Sprintf(" (an instance of that name is declared in project %s)", strings.Join(ps, ", "))
			}
			return fmt.Errorf("publish %q: instance %q (project %s) is not in this stack%s: a publish is stamped onto the instance it governs, so the two must be declared in the same stack and project", p.Name, p.Instance, at(p.Project), hint)
		}
		v, err := EncodePublishStamp(p)
		if err != nil {
			return fmt.Errorf("publish %q: %w", p.Name, err)
		}
		if stamps[i] == nil {
			stamps[i] = map[string]string{}
			order = append(order, i)
		}
		stamps[i][PublishStampKey(p.Name)] = v
	}
	for _, i := range order {
		cfg := make(map[string]string, len(resources[i].Config)+len(stamps[i]))
		for k, v := range resources[i].Config {
			cfg[k] = v
		}
		for k, v := range stamps[i] {
			cfg[k] = v
		}
		resources[i].Config = cfg
	}
	return nil
}

// staleOwnedChanges are the plan lines for the owned keys that are live on an instance and that the stack no longer derives: one removal per key, sorted.
func staleOwnedChanges(live, desired map[string]string) []string {
	var changes []string
	for _, k := range run.StaleOwnedKeys(live, desired, ownedConfigPrefixes) {
		if secrets.SensitiveKey(k) { // a stamp holds group names only, but a key under the prefix is whatever is on the instance: print no value tink would not print elsewhere
			changes = append(changes, fmt.Sprintf("config.%s: %s -> (unset)", k, secrets.HiddenValue))
			continue
		}
		changes = append(changes, fmt.Sprintf("config.%s: %q -> (unset)", k, live[k]))
	}
	return changes
}
