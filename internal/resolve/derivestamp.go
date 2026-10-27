package resolve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// A `kind: file` with `derive:` (derivedfile.go) is also STAMPED onto the instance it is pushed to, so that whatever keeps the site converged later (the daemon,
// #271) can read what to keep from the SERVER and does not need the stack files:
//
//	user.tink.derive.<file name>: {"path":"/etc/caddy/apps.caddy","render":"caddy","restart":false}
//
// <file name> is the name of the kind: file document, which is unique in its stack. The value is compact JSON with its keys sorted, so the same stack always
// stamps the same bytes (a re-plan reads "no changes"). It names no instance (the key is on it) and holds no content and no secret: what is between the
// markers is the join's rendering, and what is outside them is the person's, so neither is the stamp's to carry.
//
// It carries no guard either. `adopt_existing` (take over a file tink did not write) is never the daemon's, and `allow_empty` (replace records with none) is not
// a standing property of a declaration: whoever keeps the file converged refuses both, and a stack that wants either runs `plan apply` (#273, #271).
//
// The stamp is DERIVED, like a publish stamp (publishstamp.go, which says why the prefix is the one tink owns): deriveFileStamps adds it to the desired config
// of the instance the file names, in the same project, and the ordinary instance plan and apply converge it. A file whose instance is not declared in the stack has
// nothing to carry it and is left alone: it keeps working under `plan apply`, and internal/derive says in the plan that the daemon cannot keep it.
const DeriveStampPrefix = "user.tink.derive."

// deriveFileNamePattern is what a file's name may be for the key to be its name: it ends up as the last part of a config key.
var deriveFileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// deriveStamp is the stamped value. Its fields are in key order, which is the order the JSON is written in.
type deriveStamp struct {
	Path    string `json:"path"`
	Render  string `json:"render"`
	Restart bool   `json:"restart"`
}

// DerivedFile is what a stamp says about a file: where it is and what is written into it.
type DerivedFile struct {
	// Name is the file document's name, Project and Instance say where the stamp was found.
	Name, Project, Instance string
	Path                    string
	// Render is the value of `derive:` the file says.
	Render string
	// Restart is the file's `restart:`: whether the instance is restarted when the file changes.
	Restart bool
}

// DeriveStampKey is the config key a derived file with this name is stamped under.
func DeriveStampKey(name string) string { return DeriveStampPrefix + name }

// validDerive says whether s is one of the values of `derive:`.
func validDerive(s string) bool {
	switch s {
	case DeriveCaddy, DeriveSSO, DeriveDNS, DeriveMesh:
		return true
	}
	return false
}

func validateDeriveStamp(name, path, render string) error {
	if !deriveFileNamePattern.MatchString(name) {
		return fmt.Errorf("the file's name %q cannot be the last part of a config key: letters, digits, '.', '_' and '-', starting with a letter or digit, at most 128", name)
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\x00\r\n") {
		return fmt.Errorf("path %q is not an absolute path inside the instance", path)
	}
	if !validDerive(render) {
		return fmt.Errorf("render %q is not one of %s, %s, %s, %s", render, DeriveCaddy, DeriveSSO, DeriveDNS, DeriveMesh)
	}
	return nil
}

// EncodeDeriveStamp is the value stamped for a derived file: {"path":"...","render":"...","restart":false}, compact, keys sorted.
func EncodeDeriveStamp(f Resource) (string, error) {
	if err := validateDeriveStamp(f.Name, f.Path, f.Derive); err != nil {
		return "", err
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // a path is stamped as it is written, not as &
	if err := enc.Encode(deriveStamp{Path: f.Path, Render: f.Derive, Restart: f.Restart}); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// DecodeDeriveStamp reads one stamp back. key is the whole config key (it must start with DeriveStampPrefix), value is what is stored under it, and project and
// instance say where it was found. It is STRICT, as DecodePublishStamp is: the value must be one JSON object with exactly the keys path, render and restart,
// each once, nothing after it, and what they hold must be valid. Anything else is an error that says what, and never a file that is only nearly what was
// stamped: a region written into the wrong path, or by the wrong rendering, damages a file that is the person's.
func DecodeDeriveStamp(project, instance, key, value string) (DerivedFile, error) {
	name, ok := strings.CutPrefix(key, DeriveStampPrefix)
	if !ok {
		return DerivedFile{}, fmt.Errorf("%s is not a derived file's stamp (a stamp is %s<name>)", key, DeriveStampPrefix)
	}
	raw, err := strictObject(value)
	if err != nil {
		return DerivedFile{}, fmt.Errorf("%s: %w", key, err)
	}
	for k := range raw {
		if k != "path" && k != "render" && k != "restart" {
			return DerivedFile{}, fmt.Errorf("%s: %q is not a field of a derived file's stamp (known: path, render, restart)", key, k)
		}
	}
	for _, k := range []string{"path", "render", "restart"} {
		if _, ok := raw[k]; !ok {
			return DerivedFile{}, fmt.Errorf("%s: the stamp has no %q", key, k)
		}
	}
	f := DerivedFile{Name: name, Project: project, Instance: instance}
	if err := json.Unmarshal(raw["path"], &f.Path); err != nil {
		return DerivedFile{}, fmt.Errorf("%s: path is not a string: %w", key, err)
	}
	if err := json.Unmarshal(raw["render"], &f.Render); err != nil {
		return DerivedFile{}, fmt.Errorf("%s: render is not a string: %w", key, err)
	}
	if err := json.Unmarshal(raw["restart"], &f.Restart); err != nil {
		return DerivedFile{}, fmt.Errorf("%s: restart is not true or false: %w", key, err)
	}
	if err := validateDeriveStamp(f.Name, f.Path, f.Render); err != nil {
		return DerivedFile{}, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

// deriveFileStamps adds, to the desired config of every instance a derived file names, the stamp of each of its files. Like derivePublishStamps it changes the
// caller's resources in place, by giving the instance a COPY of its config, and is safe to run twice. A file whose instance is not declared in the same project
// is skipped, not an error: see the top of this file.
func deriveFileStamps(resources []Resource) error {
	type loc struct{ project, name string }
	instances := map[loc]int{}
	for i, r := range resources {
		if r.Kind == KindInstance {
			instances[loc{stampProject(r.Project), r.Name}] = i
		}
	}
	stamps := map[int]map[string]string{}
	var order []int
	for _, f := range resources {
		if f.Kind != KindFile || f.Derive == "" {
			continue
		}
		i, ok := instances[loc{stampProject(f.Project), f.Instance}]
		if !ok {
			continue
		}
		v, err := EncodeDeriveStamp(f)
		if err != nil {
			return fmt.Errorf("file %q: %w", f.Name, err)
		}
		if stamps[i] == nil {
			stamps[i] = map[string]string{}
			order = append(order, i)
		}
		stamps[i][DeriveStampKey(f.Name)] = v
	}
	addStamps(resources, stamps, order)
	return nil
}
