// Package compose reads a compose file and writes a stack file, once (docs/import.md, the proposal is #179).
//
// It is the onramp for the compose files that popular self-hosted projects publish, the way `tink run` is the onramp for their docker run lines,
// and it shares tink run's translation: every service becomes a dockerrun.Options, dockerrun.Build turns that into the config and devices, and what comes out is
// the same as `tink run` would make. The output is a stack the person keeps. Nothing reads the compose file again, so there is no second source of
// truth.
//
// It carries the facts a compose file states about an app and nothing about the site: no network, ACL, address, publish or backup policy. Where the
// person has to decide something, a comment says so.
//
// The subset is the keys that appear in the compose files of the survey in corpus/ (internal/compose/testdata holds them). Any other key is a
// refusal that names the key and the service, never a guess. A key that tink cannot express but the app may not need (a healthcheck) is dropped with
// a warning, and the warning is also a comment in the output.
package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/dockerrun"
	"github.com/minihci/tink/internal/secrets"
)

// Options says where a compose file's relative paths are and what the stack is called.
type Options struct {
	// Dir is where an env_file and the .env that interpolation reads are looked up. Default ".".
	Dir string
	// Stack names the stack when the file has no top-level name:. Default: the name of Dir.
	Stack string
	// Pool is the storage pool managed volumes are made in. Default "default".
	Pool string
}

// Refusal is a key (or a value) the importer will not translate.
type Refusal struct {
	Service string // empty for a top-level key
	Key     string
	Reason  string
}

func (r Refusal) String() string {
	where := "top level"
	if r.Service != "" {
		where = "service " + r.Service
	}
	return fmt.Sprintf("%s: %s: %s", where, r.Key, r.Reason)
}

// Result is what Import found. YAML is empty when anything was refused.
type Result struct {
	YAML string
	// Warnings are things that were dropped or changed and that the app may need. Each is also a comment in the YAML.
	Warnings []string
	// Notes are decisions made on the person's behalf.
	Notes []string
	// Refused lists everything that stopped the import, all of it, not only the first.
	Refused []Refusal
}

// keyKind says what the importer does with a compose key it knows. Any key not in serviceKeys or topKeys is refused as "not in the supported subset".
type keyKind int

const (
	translated keyKind = iota // becomes part of the stack
	dropped                   // dropped with a warning and a comment in the output: the app may or may not need it
	ignored                   // ignored with a note: the stack says it another way
	refused                   // refused, with a reason of its own
)

type keyRule struct {
	kind keyKind
	why  string // the reason, for dropped and refused
}

// serviceKeys is the subset of a service's keys, and docs/import.md has a row for each of them (a test reads both). The keys the survey's compose
// files use are the first ones; the rest were added with their rule because a real example needs them (#205).
var serviceKeys = map[string]keyRule{
	"image": {kind: translated}, "restart": {kind: translated}, "volumes": {kind: translated}, "ports": {kind: translated}, "environment": {kind: translated},
	"env_file": {kind: translated}, "depends_on": {kind: translated}, "user": {kind: translated}, "command": {kind: translated}, "entrypoint": {kind: translated},
	"privileged": {kind: translated}, "devices": {kind: translated}, "tmpfs": {kind: translated}, "shm_size": {kind: translated},

	"healthcheck":       {dropped, "an image's healthcheck is not run, and nothing waits for it"},
	"stop_grace_period": {dropped, "Incus has no per-instance stop timeout"},
	"extra_hosts":       {dropped, "nothing sets extra hosts entries on an Incus instance"},
	"cap_add":           {dropped, "Incus has no per-capability option; privileged is the blunt instrument"},

	"container_name": {kind: ignored},
	"hostname":       {kind: ignored},
	"networks":       {kind: ignored},

	"network_mode": {refused, dockerrun.HostNetworkAdvice},
	"read_only":    {refused, "Incus can make an instance's root disk read-only (a root device of its own with readonly=true), but the one instance made so that was tried, the image of an app whose page asks for read_only, did not start (\"Failed to setup mount entries\", Incus 7.5.1); if the app needs it, add that device by hand with --incus-device and see whether it starts"},
	"security_opt": {refused, "Incus confines a container with an AppArmor profile of its own and has no instance key for a choice of profile or for no-new-privileges; raw.apparmor and raw.lxc can carry one by hand, with --incus-config, if the app really needs it"},
	"cap_drop":     {refused, "an unprivileged Incus container has a fixed set of capabilities and nothing narrows it, so the hardening the file asks for would silently not happen; delete the key if that is acceptable"},
}

// topKeys is the same for the top level of the file.
var topKeys = map[string]keyRule{
	"services": {kind: translated}, "volumes": {kind: translated}, "name": {kind: translated},
	"networks": {kind: ignored}, "version": {kind: ignored},
}

var instanceNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,62}$`)

type importer struct {
	opts    Options
	vars    map[string]string
	res     *Result
	svcName map[string]string // compose service name -> instance name
	volumes map[string]*volume
	volDecl map[string]string // top-level volume key -> the name it is made with
	docs    []instanceDoc

	// internalNet is the top-level networks marked `internal: true`; reach holds, for each service, whether every network it is on is one of them (or it is on none).
	// Together they say what the stack's apps may send out (egress).
	internalNet map[string]bool
	reach       []serviceReach
}

// serviceReach says whether a service can send traffic out: false when every network it is attached to is `internal: true`.
type serviceReach struct {
	service string
	out     bool
}

type volume struct {
	name   string
	config map[string]string
	used   bool
}

type instanceDoc struct {
	name      string
	image     string
	dependsOn []string
	config    map[string]string
	devices   map[string]map[string]string
	comments  []string
}

// Import translates one compose file.
func Import(src []byte, opts Options) (*Result, error) {
	if opts.Dir == "" {
		opts.Dir = "."
	}
	if opts.Pool == "" {
		opts.Pool = "default"
	}
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("not YAML: %w", err)
	}
	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("not a compose file: the top level is not a mapping")
	}
	im := &importer{opts: opts, res: &Result{}, svcName: map[string]string{}, volumes: map[string]*volume{}, volDecl: map[string]string{}, internalNet: map[string]bool{}}
	vars, err := readDotEnv(filepath.Join(opts.Dir, ".env"))
	if err != nil {
		return nil, err
	}
	im.vars = vars

	stack := opts.Stack
	var serviceKeys []string
	var services map[string]any
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, val := doc.Content[i].Value, doc.Content[i+1]
		if _, known := topKeys[key]; !known {
			im.refuse("", key, "not in the supported subset")
			continue
		}
		switch key {
		case "services":
			if err := val.Decode(&services); err != nil {
				return nil, fmt.Errorf("services: %w", err)
			}
			for j := 0; j+1 < len(val.Content); j += 2 {
				serviceKeys = append(serviceKeys, val.Content[j].Value)
			}
		case "volumes":
			var v map[string]any
			if err := val.Decode(&v); err != nil {
				return nil, fmt.Errorf("volumes: %w", err)
			}
			im.declareVolumes(v)
		case "name":
			var n string
			if err := val.Decode(&n); err == nil && n != "" {
				stack = n
			}
		case "networks":
			var n map[string]any
			if err := val.Decode(&n); err != nil {
				return nil, fmt.Errorf("networks: %w", err)
			}
			for name, def := range n {
				if m, ok := def.(map[string]any); ok && truthy(m["internal"]) {
					im.internalNet[name] = true
				}
			}
			im.note("top-level networks is not made: the stack's apps share the one bridge tink derives (network: own). Only `internal: true` is read, and it becomes the stack's egress")
		case "version":
			im.note("version is ignored")
		}
	}
	if stack == "" {
		abs, _ := filepath.Abs(opts.Dir)
		stack = filepath.Base(abs)
	}
	if len(serviceKeys) == 0 {
		im.refuse("", "services", "there are no services")
	}
	// Instance names first, so a volume that shares a service's name can be told apart.
	for _, k := range serviceKeys {
		name := strings.ReplaceAll(k, "_", "-")
		if name != k {
			im.note(fmt.Sprintf("service %s is named %s: an instance name cannot hold an underscore. Anything that refers to it by name must say %s", k, name, name))
		}
		if !instanceNameRe.MatchString(name) {
			im.refuse(k, "name", fmt.Sprintf("%q cannot be an instance name (letters, digits and hyphens, starting with a letter)", name))
		}
		im.svcName[k] = name
	}
	for _, k := range serviceKeys {
		sv, ok := services[k].(map[string]any)
		if !ok {
			im.refuse(k, "service", "not a mapping")
			continue
		}
		im.service(k, sv)
	}
	egress := im.egress()
	if len(im.res.Refused) > 0 {
		return im.res, nil
	}
	im.res.YAML = im.render(stack, egress)
	return im.res, nil
}

func (im *importer) refuse(service, key, reason string) {
	im.res.Refused = append(im.res.Refused, Refusal{Service: service, Key: key, Reason: reason})
}

func (im *importer) note(s string) { im.res.Notes = append(im.res.Notes, s) }

func (im *importer) warn(service, s string) *importer {
	im.res.Warnings = append(im.res.Warnings, fmt.Sprintf("service %s: %s", service, s))
	return im
}

// declareVolumes reads the top-level volumes: a name, or a mapping holding only name:.
func (im *importer) declareVolumes(v map[string]any) {
	for key, raw := range v {
		name := key
		switch d := raw.(type) {
		case nil:
		case map[string]any:
			for k, val := range d {
				if k == "name" {
					if s, ok := val.(string); ok && s != "" {
						name = s
						continue
					}
				}
				im.refuse("", "volumes."+key+"."+k, "a volume option other than name is not in the supported subset")
			}
		default:
			im.refuse("", "volumes."+key, "not a mapping")
		}
		im.volDecl[key] = name
	}
}

func (im *importer) service(compose string, sv map[string]any) {
	name := im.svcName[compose]
	keys := make([]string, 0, len(sv))
	for k := range sv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rule, known := serviceKeys[k]
		switch {
		case !known:
			im.refuse(compose, k, "not in the supported subset")
		case rule.kind == refused && k == "network_mode":
			if v := fmt.Sprint(sv[k]); v == "none" {
				im.refuse(compose, k, "none gives the container no network at all, not even towards the other services of the file. tink has no equivalent for one instance (an instance with no NIC is a separate question). If what the file means is no outbound traffic, put its networks on `internal: true`: that becomes `egress: none` for the whole stack")
			} else if v != "host" {
				im.refuse(compose, k, fmt.Sprintf("%q is not supported; the only network_mode values tink explains are host and none: %s", v, rule.why))
			} else {
				im.refuse(compose, k, rule.why)
			}
		case rule.kind == refused:
			im.refuse(compose, k, rule.why)
		}
	}
	// Interpolate everything the translation reads.
	val, err := im.interpolate(sv)
	if err != nil {
		im.refuse(compose, "interpolation", err.Error())
		return
	}
	sv = val.(map[string]any)

	o := dockerrun.Options{Name: name, Pool: im.opts.Pool}
	if s, ok := sv["image"].(string); ok && s != "" {
		o.Image = s
	} else {
		im.refuse(compose, "image", "a service needs an image (build is not supported)")
		return
	}
	if s, ok := sv["restart"].(string); ok {
		o.Restart = s
	}
	if u, ok := sv["user"]; ok {
		o.User = fmt.Sprint(u)
	}
	if v, present := sv["privileged"]; present {
		b, ok := asBool(v)
		if !ok {
			im.refuse(compose, "privileged", fmt.Sprintf("%v is not true or false", v))
			return
		}
		o.Privileged = b
	}
	devs, ok := im.devices(compose, sv["devices"])
	if !ok {
		return
	}
	o.Device = devs
	env, ok := im.environment(compose, sv["environment"])
	if !ok {
		return
	}
	o.Env = env
	files, ok := im.envFiles(compose, sv["env_file"])
	if !ok {
		return
	}
	o.EnvFile = files
	if o.Publish, ok = im.ports(compose, sv["ports"]); !ok {
		return
	}
	cmd, ok := im.command(compose, sv)
	if !ok {
		return
	}
	o.Cmd = cmd

	vols, made, tmpfs, ok := im.volumeArgs(compose, name, sv["volumes"])
	if !ok {
		return
	}
	o.Volume = vols
	if o.IncusDevice, ok = im.tmpfsDevices(compose, tmpfs, sv); !ok {
		return
	}
	var comments []string
	for _, k := range keys {
		if serviceKeys[k].kind != dropped {
			continue
		}
		msg := fmt.Sprintf("%s was dropped: %s", k, serviceKeys[k].why)
		im.warn(compose, msg)
		comments = append(comments, msg)
	}
	for _, k := range []string{"container_name", "hostname"} {
		if _, ok := sv[k]; ok {
			im.note(fmt.Sprintf("service %s: %s is ignored: the instance is named for the service, which is the name siblings use", compose, k))
		}
	}
	if _, ok := sv["networks"]; ok {
		im.note(fmt.Sprintf("service %s: networks is ignored: the stack's apps share the one bridge tink derives (network: own). Only `internal: true` on a network is read", compose))
	}
	im.reach = append(im.reach, serviceReach{service: compose, out: im.canSendOut(sv["networks"])})

	spec, err := dockerrun.Build(o)
	if err != nil {
		key, reason := translationRefusal(err)
		im.refuse(compose, key, reason)
		return
	}
	delete(spec.Config, dockerrun.KeyCommand)
	for _, n := range spec.Notes {
		im.note(fmt.Sprintf("service %s: %s", compose, n))
	}
	for k := range spec.Config {
		if v, ok := strings.CutPrefix(k, "environment."); ok && secrets.SensitiveKey(v) && !flagLike(spec.Config[k]) {
			msg := fmt.Sprintf("environment %s looks like a secret and is written as it is: add it with `tink secret set` and write ${secret:NAME} instead", v)
			im.warn(compose, msg)
			comments = append(comments, msg)
		}
	}
	// A volume this service makes is owned by its user, as tink run does it.
	for _, vn := range made {
		if v := im.volumes[vn]; v != nil && len(spec.VolumeConfig) > 0 && v.config == nil {
			v.config = map[string]string{}
			for k, val := range spec.VolumeConfig {
				v.config[k] = val
			}
		}
	}

	d := instanceDoc{name: name, image: o.Image, config: spec.Config, devices: spec.Devices, comments: comments}
	d.dependsOn = im.dependsOn(compose, sv["depends_on"])
	im.docs = append(im.docs, d)
}

func (im *importer) environment(service string, raw any) ([]string, bool) {
	var out []string
	switch e := raw.(type) {
	case nil:
	case map[string]any:
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if e[k] == nil {
				im.refuse(service, "environment."+k, "has no value: compose would take it from the shell, and tink does not")
				return nil, false
			}
			out = append(out, k+"="+fmt.Sprint(e[k]))
		}
	case []any:
		for _, item := range e {
			s := fmt.Sprint(item)
			if !strings.Contains(s, "=") {
				im.refuse(service, "environment."+s, "has no value: compose would take it from the shell, and tink does not")
				return nil, false
			}
			out = append(out, s)
		}
	default:
		im.refuse(service, "environment", "neither a mapping nor a list")
		return nil, false
	}
	return out, true
}

func (im *importer) envFiles(service string, raw any) ([]string, bool) {
	var paths []string
	switch f := raw.(type) {
	case nil:
		return nil, true
	case string:
		paths = []string{f}
	case []any:
		for _, item := range f {
			s, ok := item.(string)
			if !ok {
				im.refuse(service, "env_file", "an entry that is not a path (the long form is not supported)")
				return nil, false
			}
			paths = append(paths, s)
		}
	default:
		im.refuse(service, "env_file", "neither a path nor a list of paths")
		return nil, false
	}
	for i, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(im.opts.Dir, p)
		}
		if _, err := os.Stat(p); err != nil {
			im.refuse(service, "env_file", fmt.Sprintf("%s cannot be read: %v", paths[i], err))
			return nil, false
		}
		paths[i] = p
	}
	return paths, true
}

func (im *importer) ports(service string, raw any) ([]string, bool) {
	var out []string
	switch p := raw.(type) {
	case nil:
	case []any:
		for _, item := range p {
			switch v := item.(type) {
			case string:
				out = append(out, v)
			case int:
				out = append(out, strconv.Itoa(v))
			default:
				im.refuse(service, "ports", "the long form is not supported")
				return nil, false
			}
		}
	default:
		im.refuse(service, "ports", "not a list")
		return nil, false
	}
	return out, true
}

// command applies compose's rule for entrypoint and command to what tink has: oci.entrypoint replaces BOTH. An entrypoint alone also drops the
// image's command in compose, so it translates exactly. A command alone only adds arguments to the image's entrypoint, which tink cannot do without
// reading the image, so it is refused.
func (im *importer) command(service string, sv map[string]any) ([]string, bool) {
	ep, hasEP := sv["entrypoint"]
	cm, hasCM := sv["command"]
	if hasCM && !hasEP {
		im.refuse(service, "command", "it adds arguments to the image's own entrypoint, and an oci.entrypoint written in a stack replaces the entrypoint as well as the command, so import cannot tell what to put before them offline; write `entrypoint:` too, with the image's own (Entrypoint in `docker inspect IMAGE` or `skopeo inspect --config docker://IMAGE`; `tink run IMAGE ARGS` prints it)")
		return nil, false
	}
	var out []string
	for _, part := range []struct {
		key string
		v   any
		has bool
	}{{"entrypoint", ep, hasEP}, {"command", cm, hasCM}} {
		if !part.has {
			continue
		}
		switch c := part.v.(type) {
		case string:
			if strings.ContainsAny(c, `'"\`) {
				im.refuse(service, part.key, "a string with quotes needs a shell to split; write it as a list")
				return nil, false
			}
			out = append(out, strings.Fields(c)...)
		case []any:
			for _, a := range c {
				out = append(out, fmt.Sprint(a))
			}
		default:
			im.refuse(service, part.key, "neither a string nor a list")
			return nil, false
		}
	}
	return out, true
}

func (im *importer) dependsOn(service string, raw any) []string {
	var out []string
	switch d := raw.(type) {
	case []any:
		for _, item := range d {
			out = append(out, im.depName(fmt.Sprint(item)))
		}
	case map[string]any:
		keys := make([]string, 0, len(d))
		for k := range d {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, im.depName(k))
			im.warn(service, fmt.Sprintf("depends_on %s: the condition is dropped; tink orders the start and does not wait for readiness", k))
		}
	}
	return out
}

func (im *importer) depName(k string) string {
	if n, ok := im.svcName[k]; ok {
		return n
	}
	return k
}

// tmpfsMount is a tmpfs a service asks for, from a tmpfs: entry, a long-form volume of type tmpfs, or shm_size.
type tmpfsMount struct{ path, size string }

// volumeArgs turns the service's volumes into tink run -v arguments and registers the managed volumes they name. A long-form volume of type tmpfs
// is returned in tmpfs, because it is not a -v argument.
func (im *importer) volumeArgs(service, instance string, raw any) (args []string, made []string, tmpfs []tmpfsMount, ok bool) {
	list, isList := raw.([]any)
	if raw != nil && !isList {
		im.refuse(service, "volumes", "not a list")
		return nil, nil, nil, false
	}
	for _, item := range list {
		var src, dst string
		var opts []string
		switch v := item.(type) {
		case string:
			parts := strings.Split(v, ":")
			if len(parts) < 2 || len(parts) > 3 {
				im.refuse(service, "volumes", fmt.Sprintf("%q: expected SOURCE:TARGET[:OPTIONS]", v))
				return nil, nil, nil, false
			}
			src, dst = parts[0], parts[1]
			if len(parts) == 3 {
				opts = strings.Split(parts[2], ",")
			}
		case map[string]any:
			for k := range v {
				if k != "type" && k != "source" && k != "target" && k != "read_only" && k != "tmpfs" {
					im.refuse(service, "volumes."+k, "not in the supported subset")
					return nil, nil, nil, false
				}
			}
			t, _ := v["type"].(string)
			if t == "tmpfs" {
				m, ok := im.longTmpfs(service, v)
				if !ok {
					return nil, nil, nil, false
				}
				tmpfs = append(tmpfs, m)
				continue
			}
			if _, has := v["tmpfs"]; has {
				im.refuse(service, "volumes.tmpfs", "only a volume of type tmpfs has tmpfs options")
				return nil, nil, nil, false
			}
			if t != "bind" && t != "volume" {
				im.refuse(service, "volumes.type", fmt.Sprintf("%q is not supported (bind, volume and tmpfs are)", t))
				return nil, nil, nil, false
			}
			src, _ = v["source"].(string)
			dst, _ = v["target"].(string)
			if ro, _ := v["read_only"].(bool); ro {
				opts = []string{"ro"}
			}
			if src == "" || dst == "" {
				im.refuse(service, "volumes", "a long-form volume needs a source and a target")
				return nil, nil, nil, false
			}
		default:
			im.refuse(service, "volumes", "an entry that is neither a string nor a mapping")
			return nil, nil, nil, false
		}
		if strings.HasSuffix(src, "docker.sock") {
			im.refuse(service, "volumes", "the Docker socket: there is no Docker on an Incus host")
			return nil, nil, nil, false
		}
		source := src
		switch {
		case strings.HasPrefix(src, "/"):
			// a host path, taken as it is
		case src == "." || src == ".." || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") || strings.HasPrefix(src, "~"):
			base := strings.Trim(filepath.Base(filepath.Clean(src)), ".")
			if base == "" || base == "~" {
				im.refuse(service, "volumes", fmt.Sprintf("%q is a whole directory; name a subdirectory", src))
				return nil, nil, nil, false
			}
			source = im.volumeName(instance + "-" + sanitize(base))
			im.note(fmt.Sprintf("service %s: %s was a path relative to the compose file; it is now the managed volume %s. Write it as a bind mount to keep it a directory you can see", service, src, source))
			im.useVolume(source)
			made = append(made, source)
		default:
			decl, declared := im.volDecl[src]
			if !declared {
				im.refuse(service, "volumes", fmt.Sprintf("volume %q is not declared in the top-level volumes", src))
				return nil, nil, nil, false
			}
			source = im.volumeName(decl)
			im.useVolume(source)
			made = append(made, source)
		}
		arg := source + ":" + dst
		if len(opts) > 0 {
			arg += ":" + strings.Join(opts, ",")
		}
		args = append(args, arg)
	}
	return args, made, tmpfs, true
}

// longTmpfs reads a long-form volume of type tmpfs: a target, and tmpfs: {size, mode}. A mode means nothing to Incus's tmpfs device and is noted.
func (im *importer) longTmpfs(service string, v map[string]any) (tmpfsMount, bool) {
	target, _ := v["target"].(string)
	if target == "" {
		im.refuse(service, "volumes", "a tmpfs volume needs a target")
		return tmpfsMount{}, false
	}
	if src, _ := v["source"].(string); src != "" {
		im.refuse(service, "volumes.source", "a tmpfs volume has no source")
		return tmpfsMount{}, false
	}
	m := tmpfsMount{path: target}
	opts, _ := v["tmpfs"].(map[string]any)
	for k, val := range opts {
		switch k {
		case "size":
			m.size = fmt.Sprint(val)
		case "mode":
			im.note(fmt.Sprintf("service %s: the mode of the tmpfs at %s is ignored: Incus's tmpfs device has no mode option", service, target))
		default:
			im.refuse(service, "volumes.tmpfs."+k, "not in the supported subset (size is)")
			return tmpfsMount{}, false
		}
	}
	return m, true
}

// shortTmpfs reads the tmpfs: key: a path or a list of paths, each with docker's options after a colon (/run:size=64m,noexec).
func (im *importer) shortTmpfs(service string, raw any) ([]tmpfsMount, bool) {
	var items []string
	switch t := raw.(type) {
	case nil:
		return nil, true
	case string:
		items = []string{t}
	case []any:
		for _, it := range t {
			s, ok := it.(string)
			if !ok {
				im.refuse(service, "tmpfs", "an entry that is not a path")
				return nil, false
			}
			items = append(items, s)
		}
	default:
		im.refuse(service, "tmpfs", "neither a path nor a list of paths")
		return nil, false
	}
	var out []tmpfsMount
	for _, it := range items {
		path, optstr, _ := strings.Cut(it, ":")
		m := tmpfsMount{path: path}
		for _, o := range strings.Split(optstr, ",") {
			switch k, v, _ := strings.Cut(o, "="); k {
			case "":
			case "size":
				m.size = v
			default:
				im.note(fmt.Sprintf("service %s: the tmpfs option %q at %s is ignored: Incus's tmpfs device takes a size and nothing else", service, o, path))
			}
		}
		out = append(out, m)
	}
	return out, true
}

// tmpfsDevices turns every tmpfs a service asks for (volumes of type tmpfs, tmpfs:, shm_size) into --incus-device entries, in a fixed order.
func (im *importer) tmpfsDevices(service string, fromVolumes []tmpfsMount, sv map[string]any) ([]string, bool) {
	short, ok := im.shortTmpfs(service, sv["tmpfs"])
	if !ok {
		return nil, false
	}
	mounts := append(append([]tmpfsMount{}, fromVolumes...), short...)
	if v, present := sv["shm_size"]; present {
		mounts = append(mounts, tmpfsMount{path: "/dev/shm", size: fmt.Sprint(v)})
	}
	var out []string
	seen := map[string]bool{}
	for i, m := range mounts {
		if seen[m.path] {
			im.refuse(service, "tmpfs", fmt.Sprintf("two tmpfs mounts at %s", m.path))
			return nil, false
		}
		seen[m.path] = true
		e, err := dockerrun.TmpfsIncusDevice(fmt.Sprintf("tmpfs%d", i), m.path, m.size)
		if err != nil {
			im.refuse(service, "tmpfs", err.Error())
			return nil, false
		}
		out = append(out, e)
	}
	return out, true
}

// devices reads devices: HOST:CONTAINER[:rwm], docker's form, as tink run --device does. The long form is not supported.
func (im *importer) devices(service string, raw any) ([]string, bool) {
	var out []string
	switch d := raw.(type) {
	case nil:
	case []any:
		for _, it := range d {
			s, ok := it.(string)
			if !ok {
				im.refuse(service, "devices", "the long form is not supported; write HOST:CONTAINER")
				return nil, false
			}
			out = append(out, s)
		}
	default:
		im.refuse(service, "devices", "not a list")
		return nil, false
	}
	return out, true
}

// asBool reads a compose boolean, which interpolation may have turned into a string.
func asBool(v any) (b, ok bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(x) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

// volumeName returns the name a managed volume is made with. A volume that shares a name with a service would be a "duplicate resource name" in a
// stack (one namespace for every kind), so it gets -data.
func (im *importer) volumeName(name string) string {
	for _, svc := range im.svcName {
		if svc == name {
			renamed := name + "-data"
			im.note(fmt.Sprintf("volume %s is named %s: a volume and an instance cannot share a name in a stack", name, renamed))
			return renamed
		}
	}
	return name
}

func (im *importer) useVolume(name string) {
	if im.volumes[name] == nil {
		im.volumes[name] = &volume{name: name}
	}
	im.volumes[name].used = true
}

var nonName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitize(s string) string { return strings.Trim(nonName.ReplaceAllString(s, "-"), "-") }

func readDotEnv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	vars := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		vars[strings.TrimSpace(k)] = v
	}
	return vars, nil
}

// interpolate replaces $VAR, ${VAR}, ${VAR:-default} and ${VAR-default} in every string, from the .env file beside the compose file only (never the
// shell's environment, so the result does not depend on who ran the import). A default is itself expanded, as compose does (${USERDIR:-$HOME}). An
// unset variable with no default is an error, and so is any other form.
func (im *importer) interpolate(v any) (any, error) {
	switch x := v.(type) {
	case string:
		return im.expand(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			r, err := im.interpolate(e)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			r, err := im.interpolate(e)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	}
	return v, nil
}

func (im *importer) expand(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '$' {
			b.WriteByte('$')
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] != '{' {
			j := i + 1
			for j < len(s) && isNameByte(s[j], j == i+1) {
				j++
			}
			if j == i+1 {
				return "", fmt.Errorf("%q: a $ that is not $NAME or ${...} is not supported", s)
			}
			val, set := im.vars[s[i+1:j]]
			if !set {
				return "", fmt.Errorf("$%s is not set: put it in a .env file beside the compose file (the shell's environment is not read)", s[i+1:j])
			}
			b.WriteString(val)
			i = j - 1
			continue
		}
		if i+1 >= len(s) {
			return "", fmt.Errorf("%q: a $ that is not $NAME or ${...} is not supported", s)
		}
		end, depth := -1, 0
		for j := i + 1; j < len(s); j++ {
			if s[j] == '{' {
				depth++
			} else if s[j] == '}' {
				if depth--; depth == 0 {
					end = j
					break
				}
			}
		}
		if end < 0 {
			return "", fmt.Errorf("%q: unclosed ${", s)
		}
		expr := s[i+2 : end]
		i = end
		name, def, hasDef, colon := expr, "", false, false
		if n, d, ok := strings.Cut(expr, ":-"); ok {
			name, def, hasDef, colon = n, d, true, true
		} else if n, d, ok := strings.Cut(expr, "-"); ok {
			name, def, hasDef = n, d, true
		}
		if strings.ContainsAny(name, ":?+") || name == "" {
			return "", fmt.Errorf("${%s}: only ${VAR}, ${VAR:-default} and ${VAR-default} are supported", expr)
		}
		val, set := im.vars[name]
		switch {
		case set && (val != "" || !colon):
			b.WriteString(val)
		case hasDef:
			d, err := im.expand(def)
			if err != nil {
				return "", err
			}
			b.WriteString(d)
		default:
			return "", fmt.Errorf("${%s} is not set: put it in a .env file beside the compose file (the shell's environment is not read)", name)
		}
	}
	return b.String(), nil
}

func isNameByte(c byte, first bool) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (!first && c >= '0' && c <= '9')
}

// flagLike reports a value that is a switch and not a secret (MYSQL_RANDOM_ROOT_PASSWORD: "1"), or empty.
func flagLike(v string) bool {
	switch strings.ToLower(v) {
	case "", "0", "1", "true", "false", "yes", "no", "on", "off":
		return true
	}
	return false
}

// buildFlag is how dockerrun.Build names what it refused: by the docker-run flag. The person wrote a compose key, so say that.
var buildFlag = regexp.MustCompile(`^--(user|publish|volume|env-file|env|device) `)

var flagKey = map[string]string{"user": "user", "publish": "ports", "volume": "volumes", "env-file": "env_file", "env": "environment", "device": "devices"}

func translationRefusal(err error) (key, reason string) {
	msg := err.Error()
	if m := buildFlag.FindStringSubmatch(msg); m != nil {
		return flagKey[m[1]], strings.TrimPrefix(msg, m[0])
	}
	return "translation", msg
}

// truthy reads a compose boolean, which a file may write as true, "true" or yes.
func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(x) {
		case "true", "yes", "on", "1":
			return true
		}
	}
	return false
}

// canSendOut says whether a service with these networks can send traffic out. Compose lets it unless every network it is on is `internal: true`: a service
// with no networks key is on the project's `default` network, which a file can mark internal too. A network the file does not define counts as not internal.
func (im *importer) canSendOut(raw any) bool {
	var names []string
	switch n := raw.(type) {
	case []any:
		for _, x := range n {
			names = append(names, fmt.Sprint(x))
		}
	case map[string]any:
		for k := range n {
			names = append(names, k)
		}
	}
	if len(names) == 0 {
		names = []string{"default"}
	}
	for _, name := range names {
		if !im.internalNet[name] {
			return true
		}
	}
	return false
}

// egress is the stack's egress, from what its services can send out: open when all of them can, none when none can. Mixed is refused: a bridge is the stack's,
// so it cannot isolate one app of a file from the outside.
func (im *importer) egress() string {
	var out, in []string
	for _, r := range im.reach {
		if r.out {
			out = append(out, r.service)
		} else {
			in = append(in, r.service)
		}
	}
	switch {
	case len(in) == 0:
		return "open"
	case len(out) == 0:
		return "none"
	}
	im.refuse("", "networks", fmt.Sprintf("%s can send traffic out and %s cannot (every network they are on is `internal: true`). A stack has one bridge, so it cannot isolate some of its apps from the outside: put them all on internal networks, or none, or split the file", strings.Join(out, ", "), strings.Join(in, ", ")))
	return ""
}
