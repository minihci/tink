package resolve

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	yaml "go.yaml.in/yaml/v4"
)

// yamlResource is the on-disk shape: multi-document YAML files
// (`---`-separated), one resource per document, discriminated by `kind`.
// Deliberately close to incus-apply's own YAML shape rather than a new
// invention -- familiarity was worth more here than any improvement a
// different shape might buy.
type yamlResource struct {
	Kind      string                       `yaml:"kind"`
	Name      string                       `yaml:"name"`
	Project   string                       `yaml:"project"`
	DependsOn []string                     `yaml:"depends_on"`
	Image     string                       `yaml:"image"`
	Profiles  []string                     `yaml:"profiles"`
	VM        bool                         `yaml:"vm"`
	Pool      string                       `yaml:"pool"`
	Config    map[string]string            `yaml:"config"`
	Devices   map[string]map[string]string `yaml:"devices"`

	// File-only.
	Instance   string `yaml:"instance"`
	Path       string `yaml:"path"`
	Content    string `yaml:"content"`
	SourcePath string `yaml:"source_path"` // read at load time, relative to this YAML file's own directory

	// Shared between File and Instance -- see Resource.Restart's own doc
	// comment.
	Restart bool `yaml:"restart"`

	// Incus-only. Both are argv for the incus binary, e.g. `command: [image,
	// import, /path/to.qcow2, --alias, haos-x86-64]` -- never a shell
	// string, so there's no quoting/escaping question and no way to run
	// anything but the incus CLI itself.
	Check   []string `yaml:"check"`
	Command []string `yaml:"command"`

	// Image-only. Source is read at load time like SourcePath above --
	// relative to this YAML file's own directory. Architecture defaults
	// to "x86_64" when left empty. Properties is its own field (not
	// Config above) since an image has no "config" concept in Incus at
	// all -- see Resource.Properties' own doc comment.
	Alias        string            `yaml:"alias"`
	Source       string            `yaml:"source"`
	Architecture string            `yaml:"architecture"`
	Properties   map[string]string `yaml:"properties"`
}

// DefaultFile is what tink plan / tink plan apply read when given no
// FILE arguments at all -- "the stack described by this directory,"
// the same role docker-compose.yml or kustomization.yaml play for their
// own tools. Earned that position by being the format two real stacks
// were hand-authored in directly, with no translation step ever
// involved (see docs/resolver-architecture.md's 2026-09-18 update).
const DefaultFile = "tink.yaml"

// LoadFiles loads and concatenates every resource across paths,
// defaulting to []string{DefaultFile} when paths is empty.
func LoadFiles(paths []string) ([]Resource, error) {
	if len(paths) == 0 {
		paths = []string{DefaultFile}
	}
	var resources []Resource
	for _, path := range paths {
		rs, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		resources = append(resources, rs...)
	}
	return resources, nil
}

// LoadFile parses one multi-document YAML file into a list of Resources.
// A file resource's source_path is read now, relative to this YAML
// file's own directory -- matching Terraform's own ${path.module}
// convention for the same problem.
func LoadFile(path string) ([]Resource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dir := filepath.Dir(path)
	var resources []Resource
	dec := yaml.NewDecoder(f)
	for {
		var doc yamlResource
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if doc.Kind == "" {
			continue // blank document between "---" separators
		}
		r, err := doc.toResource(dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if err := Validate(r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		resources = append(resources, r)
	}
	return resources, nil
}

func (d yamlResource) toResource(dir string) (Resource, error) {
	if d.Name == "" {
		return Resource{}, fmt.Errorf("kind %q: name is required", d.Kind)
	}
	kind := Kind(d.Kind)
	if _, ok := kindPriority[kind]; !ok {
		return Resource{}, fmt.Errorf("resource %q: unknown kind %q", d.Name, d.Kind)
	}

	content := d.Content
	if d.SourcePath != "" {
		if d.Content != "" {
			return Resource{}, fmt.Errorf("resource %q: content and source_path are mutually exclusive", d.Name)
		}
		data, err := os.ReadFile(filepath.Join(dir, d.SourcePath))
		if err != nil {
			return Resource{}, fmt.Errorf("resource %q: reading source_path: %w", d.Name, err)
		}
		content = string(data)
	}

	if kind == KindIncus && (len(d.Check) == 0 || len(d.Command) == 0) {
		return Resource{}, fmt.Errorf("resource %q: kind incus requires both check and command", d.Name)
	}

	source := d.Source
	architecture := d.Architecture
	if kind == KindImage {
		if d.Alias == "" || d.Source == "" {
			return Resource{}, fmt.Errorf("resource %q: kind image requires both alias and source", d.Name)
		}
		source = filepath.Join(dir, d.Source)
		if architecture == "" {
			architecture = "x86_64"
		}
	}

	return Resource{
		Kind:         kind,
		Name:         d.Name,
		Project:      d.Project,
		DependsOn:    d.DependsOn,
		Image:        d.Image,
		Profiles:     d.Profiles,
		VM:           d.VM,
		Pool:         d.Pool,
		Config:       d.Config,
		Devices:      d.Devices,
		Instance:     d.Instance,
		Path:         d.Path,
		Content:      content,
		Restart:      d.Restart,
		Check:        d.Check,
		Command:      d.Command,
		Alias:        d.Alias,
		Source:       source,
		Architecture: architecture,
		Properties:   d.Properties,
	}, nil
}
