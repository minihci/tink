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
	Pool      string                       `yaml:"pool"`
	Config    map[string]string            `yaml:"config"`
	Devices   map[string]map[string]string `yaml:"devices"`

	// File-only.
	Instance   string `yaml:"instance"`
	Path       string `yaml:"path"`
	Content    string `yaml:"content"`
	SourcePath string `yaml:"source_path"` // read at load time, relative to this YAML file's own directory
	Restart    bool   `yaml:"restart"`
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

	return Resource{
		Kind:      kind,
		Name:      d.Name,
		Project:   d.Project,
		DependsOn: d.DependsOn,
		Image:     d.Image,
		Profiles:  d.Profiles,
		Pool:      d.Pool,
		Config:    d.Config,
		Devices:   d.Devices,
		Instance:  d.Instance,
		Path:      d.Path,
		Content:   content,
		Restart:   d.Restart,
	}, nil
}
