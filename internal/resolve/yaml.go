package resolve

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/backupext"
)

// yamlResource is the on-disk shape: multi-document YAML files
// (`---`-separated), one resource per document, discriminated by `kind`.
// Deliberately close to incus-apply's own YAML shape rather than a new
// invention -- familiarity was worth more here than any improvement a
// different shape might buy.
type yamlResource struct {
	Kind      string   `yaml:"kind"`
	Name      string   `yaml:"name"`
	Project   string   `yaml:"project"`
	DependsOn []string `yaml:"depends_on"`
	Image     string   `yaml:"image"`
	Profiles  []string `yaml:"profiles"`
	VM        bool     `yaml:"vm"`
	Ephemeral bool     `yaml:"ephemeral"`
	Pool      string   `yaml:"pool"`

	// Backup-target-only -- see backuptarget.go.
	Location string                       `yaml:"location"`
	Engine   string                       `yaml:"engine"`
	Remote   string                       `yaml:"remote"`
	Config   map[string]string            `yaml:"config"`
	Devices  map[string]map[string]string `yaml:"devices"`

	// Backup-target-only, optional, and only with remote: the server's address and certificate fingerprint -- see Resource.Address.
	Address     string `yaml:"address"`
	Fingerprint string `yaml:"fingerprint"`

	// Network and network-ACL (Description), network-only (AllowDisruption) -- see Resource.Description and Resource.AllowDisruption.
	Description     string `yaml:"description"`
	AllowDisruption bool   `yaml:"allow_disruption"`

	// Network-ACL-only -- see Resource.Ingress. Incus's own rule type, so a rule pastes in from `incus network acl show` unchanged and an unknown
	// rule field is as much a load error as an unknown resource field.
	Ingress []api.NetworkACLRule `yaml:"ingress"`
	Egress  yamlEgress           `yaml:"egress"` // a list of rules on a network-ACL, a word on a stack -- see yamlEgress

	// DeriveIngress -- see Resource.DeriveIngress.
	DeriveIngress bool `yaml:"derive_ingress"`

	// Storage-volume-only -- see backupext.VolumeBackup.
	Backup *yamlBackup `yaml:"backup"`

	// Stack-only -- see defaults.go.
	Defaults *yamlDefaults `yaml:"defaults"`

	// File-only.
	Instance string `yaml:"instance"`
	Path     string `yaml:"path"`

	// Stack-only (see ownership.go): "explicit" (the default) or "adopt-on-apply".
	Ownership string `yaml:"ownership"`

	// Stack-only (see site.go): the site the stack belongs to.
	Site string `yaml:"site"`

	// Publish-only (see publish.go).
	Offer string   `yaml:"offer"`
	Allow []string `yaml:"allow"`

	// Helper-only (see helperkind.go).
	Network  string   `yaml:"network"`
	Timezone string   `yaml:"timezone"`
	Remotes  []string `yaml:"remotes"`

	// Stack-only -- see Resource.NetworkPool.
	NetworkPool   string `yaml:"network_pool"`
	NetworkPrefix int    `yaml:"network_prefix"`

	Derive string `yaml:"derive"` // file-only -- see Resource.Derive
	// File-only, with derive -- see Resource.AdoptExisting.
	AdoptExisting bool   `yaml:"adopt_existing"`
	AllowEmpty    bool   `yaml:"allow_empty"`
	Content       string `yaml:"content"`
	SourcePath    string `yaml:"source_path"` // read at load time, relative to this YAML file's own directory

	// Shared between File and Instance -- see Resource.Restart's own doc
	// comment.
	Restart bool `yaml:"restart"`

	OnImageChange   string `yaml:"on_image_change"`
	SnapshotVolumes bool   `yaml:"snapshot_volumes"`

	// Incus-only. Both are argv for the incus binary, e.g. `command: [image,
	// import, /path/to.qcow2, --alias, haos-x86-64]` -- never a shell
	// string, so there's no quoting/escaping question and no way to run
	// anything but the incus CLI itself.
	//
	// Check, Command and Triggers are also exec-only -- see
	// Resource.Check's and Resource.Triggers' own doc comments for what
	// each field means there instead.
	Check    []string `yaml:"check"`
	Command  []string `yaml:"command"`
	Triggers []string `yaml:"triggers"`

	// Exec-only, the third guard: see Resource.CheckOutput.
	CheckOutput *struct {
		Command []string `yaml:"command"`
		Equals  string   `yaml:"equals"`
	} `yaml:"check_output"`

	// Exec-only. A duration string (e.g. "45s"), parsed below -- see
	// Resource.AgentTimeout's own doc comment for why this is one
	// duration rather than separate attempts/delay fields.
	AgentTimeout string `yaml:"agent_timeout"`

	// Exec-only. A duration string: how long one guest command may run. See Resource.Timeout.
	Timeout string `yaml:"timeout"`

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

// yamlDefaults is the on-disk shape of a stack's `defaults:`: one section per kind that has something to default. The loader is strict about
// every level of it, like the rest of the file.
type yamlDefaults struct {
	Instance *struct {
		Config  map[string]string            `yaml:"config"`
		Devices map[string]map[string]string `yaml:"devices"`
	} `yaml:"instance"`
	StorageVolume *struct {
		Backup *yamlBackup `yaml:"backup"`
	} `yaml:"storage-volume"`
}

func (d *yamlDefaults) toStackDefaults() *StackDefaults {
	if d == nil {
		return nil
	}
	out := &StackDefaults{}
	if d.Instance != nil {
		out.Instance = &InstanceDefaults{Config: d.Instance.Config, Devices: d.Instance.Devices}
	}
	if d.StorageVolume != nil {
		out.StorageVolume = &VolumeDefaults{Backup: d.StorageVolume.Backup.toVolumeBackup()}
	}
	return out
}

// yamlBackup is the on-disk shape of a volume's backup block.
type yamlBackup struct {
	Snapshots *struct {
		Schedule string `yaml:"schedule"`
		Retain   string `yaml:"retain"`
	} `yaml:"snapshots"`
	Copies []struct {
		Target   string `yaml:"target"`
		Schedule string `yaml:"schedule"`
		Retain   string `yaml:"retain"`
	} `yaml:"copies"`
	Verify yamlVerify `yaml:"verify"`
	None   string     `yaml:"none"`
}

// yamlVerify accepts both `verify: weekly` and
//
//	verify:
//	  every: weekly
//	  check: {image: ..., command: [...], mount: /data}
type yamlVerify struct {
	Every string
	Check *struct {
		Image   string   `yaml:"image"`
		Command []string `yaml:"command"`
		Mount   string   `yaml:"mount"`
	}
}

// yamlEgress is a document's `egress:`. On a network-ACL it is the list of egress rules (Incus's own rule type, read strictly like the ingress list); on a
// stack it is a word, what the stack's apps may send out (Resource.EgressMode). Which one it is follows from the node, and validation then checks that the
// kind may have it.
type yamlEgress struct {
	Rules []api.NetworkACLRule
	Word  string
}

func (e *yamlEgress) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Decode(&e.Word)
	case yaml.SequenceNode:
		// Load with WithKnownFields, as the ingress list is decoded: an unknown rule field is a load error.
		rules := []api.NetworkACLRule{}
		if err := node.Load(&rules, yaml.WithKnownFields()); err != nil {
			return err
		}
		e.Rules = rules
		return nil
	}
	return fmt.Errorf("line %d: egress must be a list of rules (a network-acl) or a word (a stack)", node.Line)
}

func (v *yamlVerify) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return node.Decode(&v.Every)
	case yaml.MappingNode:
		var m struct {
			Every string `yaml:"every"`
			Check *struct {
				Image   string   `yaml:"image"`
				Command []string `yaml:"command"`
				Mount   string   `yaml:"mount"`
			} `yaml:"check"`
		}
		// Load with WithKnownFields, not Decode: Decode would not be strict here, and a misspelled key
		// inside verify would silently drop the check.
		if err := node.Load(&m, yaml.WithKnownFields()); err != nil {
			return err
		}
		v.Every, v.Check = m.Every, m.Check
		return nil
	}
	return fmt.Errorf("line %d: verify must be a cadence (daily, weekly, monthly) or a mapping with every and check", node.Line)
}

func (b *yamlBackup) toVolumeBackup() *backupext.VolumeBackup {
	if b == nil {
		return nil
	}
	out := &backupext.VolumeBackup{None: b.None, Verify: b.Verify.Every}
	if c := b.Verify.Check; c != nil {
		out.VerifyCheck = &backupext.VerifyCheck{Image: c.Image, Command: c.Command, Mount: c.Mount}
	}
	for _, c := range b.Copies {
		out.Copies = append(out.Copies, backupext.BackupCopy{Target: c.Target, Schedule: c.Schedule, Retain: c.Retain})
	}
	if b.Snapshots != nil {
		out.Snapshots = &backupext.SnapshotPolicy{Schedule: b.Snapshots.Schedule, Retain: b.Snapshots.Retain}
	}
	return out
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
	resources, err := loadFiles(paths)
	if err == nil {
		applyStackProject(resources) // the stack's own project, if it names one, for every resource that names none
		applyStackSite(resources)    // and the stack's site, stamped on the instances the join reads across stacks
	}
	return resources, err
}

func loadFiles(paths []string) ([]Resource, error) {
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
	// A field tink does not know is an error, not something to skip: a misspelled key, a wrongly
	// indented block, or a field from a newer tink would otherwise be dropped silently and the stack
	// would converge without it (an app started without its secret, a volume without its backup).
	dec.KnownFields(true)
	for {
		var doc yamlResource
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if strings.Contains(err.Error(), "not found in type") {
				return nil, fmt.Errorf("%s: %w (tink rejects fields it does not know instead of ignoring them: check the spelling and the indentation, and that this tink is new enough)", path, err)
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
		src := filepath.Join(dir, d.SourcePath)
		data, err := os.ReadFile(src)
		if err != nil {
			return Resource{}, fmt.Errorf("resource %q: reading source_path: %w", d.Name, err)
		}
		content = string(data)
	}

	if kind == KindIncus && (len(d.Check) == 0 || len(d.Command) == 0) {
		return Resource{}, fmt.Errorf("resource %q: kind incus requires both check and command", d.Name)
	}

	var agentTimeout, timeout time.Duration
	if kind == KindExec {
		if d.Instance == "" {
			return Resource{}, fmt.Errorf("resource %q: kind exec requires instance", d.Name)
		}
		if len(d.Command) == 0 {
			return Resource{}, fmt.Errorf("resource %q: kind exec requires command", d.Name)
		}
		guards := 0
		for _, set := range []bool{len(d.Check) > 0, d.CheckOutput != nil, len(d.Triggers) > 0} {
			if set {
				guards++
			}
		}
		if guards == 0 {
			return Resource{}, fmt.Errorf("resource %q: kind exec requires one of check, check_output or triggers, to answer \"was this already done\" -- see Resource.Check's, Resource.CheckOutput's and Resource.Triggers' own doc comments", d.Name)
		}
		if guards > 1 {
			return Resource{}, fmt.Errorf("resource %q: kind exec check, check_output and triggers are mutually exclusive -- a check re-derives convergence from live reality every time, triggers only when no such check can be written at all", d.Name)
		}
		if d.CheckOutput != nil && (len(d.CheckOutput.Command) == 0 || d.CheckOutput.Equals == "") {
			return Resource{}, fmt.Errorf("resource %q: check_output requires both command and equals", d.Name)
		}
		if d.AgentTimeout != "" {
			var err error
			agentTimeout, err = time.ParseDuration(d.AgentTimeout)
			if err != nil {
				return Resource{}, fmt.Errorf("resource %q: agent_timeout: %w", d.Name, err)
			}
			// time.ParseDuration accepts a negative string ("-5s") without
			// complaint, but agentRetry's own <= 0 check treats zero and
			// negative identically ("use the default") -- silently, which
			// would swallow a typo (a stray "-") as if agent_timeout had
			// never been set at all instead of reporting it as the invalid
			// value it actually is.
			if agentTimeout < 0 {
				return Resource{}, fmt.Errorf("resource %q: agent_timeout: must not be negative, got %s", d.Name, d.AgentTimeout)
			}
		}
		if d.Timeout != "" {
			var err error
			timeout, err = time.ParseDuration(d.Timeout)
			if err != nil {
				return Resource{}, fmt.Errorf("resource %q: timeout: %w", d.Name, err)
			}
			if timeout < 0 {
				return Resource{}, fmt.Errorf("resource %q: timeout: must not be negative, got %s", d.Name, d.Timeout)
			}
		}
	} else if d.AgentTimeout != "" {
		return Resource{}, fmt.Errorf("resource %q: agent_timeout is exec-only", d.Name)
	} else if d.Timeout != "" {
		return Resource{}, fmt.Errorf("resource %q: timeout is exec-only", d.Name)
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
		Kind:            kind,
		Name:            d.Name,
		Project:         d.Project,
		DependsOn:       d.DependsOn,
		Image:           d.Image,
		Profiles:        d.Profiles,
		VM:              d.VM,
		Ephemeral:       d.Ephemeral,
		Pool:            d.Pool,
		Location:        d.Location,
		Engine:          d.Engine,
		Remote:          d.Remote,
		Address:         d.Address,
		Fingerprint:     d.Fingerprint,
		Backup:          d.Backup.toVolumeBackup(),
		Defaults:        d.Defaults.toStackDefaults(),
		Description:     d.Description,
		AllowDisruption: d.AllowDisruption,
		Ingress:         d.Ingress,
		Egress:          d.Egress.Rules,
		EgressMode:      d.Egress.Word,
		DeriveIngress:   d.DeriveIngress,
		Config:          d.Config,
		Devices:         d.Devices,
		Instance:        d.Instance,
		Ownership:       d.Ownership,
		Site:            d.Site,
		Offer:           d.Offer,
		Allow:           d.Allow,
		Network:         d.Network,
		NetworkPool:     d.NetworkPool,
		NetworkPrefix:   d.NetworkPrefix,
		Timezone:        d.Timezone,
		Remotes:         d.Remotes,
		Path:            d.Path,
		Derive:          d.Derive,
		AdoptExisting:   d.AdoptExisting,
		AllowEmpty:      d.AllowEmpty,
		Content:         content,
		Restart:         d.Restart,
		OnImageChange:   d.OnImageChange,
		SnapshotVolumes: d.SnapshotVolumes,
		Check:           d.Check,
		Command:         d.Command,
		Triggers:        d.Triggers,
		CheckOutput:     checkOutputOf(d.CheckOutput),
		AgentTimeout:    agentTimeout,
		Timeout:         timeout,
		Alias:           d.Alias,
		Source:          source,
		Architecture:    architecture,
		Properties:      d.Properties,
	}, nil
}

// checkOutputOf turns the parsed check_output block into the Resource's OutputCheck, or nil when there is none.
func checkOutputOf(c *struct {
	Command []string `yaml:"command"`
	Equals  string   `yaml:"equals"`
}) *OutputCheck {
	if c == nil {
		return nil
	}
	return &OutputCheck{Command: c.Command, Equals: c.Equals}
}
