package resolve

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/lxc/incus/v7/shared/cliconfig"
	yaml "go.yaml.in/yaml/v4"

	"github.com/minihci/tink/internal/backupmeta"
	"github.com/minihci/tink/internal/incusapi"
	"github.com/minihci/tink/internal/run"
	"github.com/minihci/tink/internal/secrets"
)

// Export: the other direction. Plan and apply take a YAML stack to live objects; Export reads live objects back into a YAML stack, which
// is the "distill" step for something first brought up with `tink run` (or by hand with incus).
//
// The hard part is not reading, it is knowing what a person wrote. An Incus instance carries ~25 keys for every one a person set: the
// runtime's volatile.*, the image's own image.* and oci.*, and (for OCI images) environment.* copied from the image. So the filters are:
//
//   - volatile.* and image.* are dropped: Incus's.
//   - oci.* and environment.* that equal what the IMAGE says (its Entrypoint+Cmd, WorkingDir, User, Env, read from the registry the way a
//     rebuild reads them, see runtimeConfigDiff) are dropped; any that differ are an override and are kept. Without the registry
//     (--offline, or unreachable) they are all kept and a note says so.
//   - image: is a guess until proven. image.id is whatever string first filled the cached image record (see the top of imagedrift.go), so
//     the candidate is checked with the very comparison plan uses (checkImage, against volatile.base_image) before it is written, and
//     only then pinned to the digest the registry reports. A candidate that does not match is written anyway, marked, and noted.
//   - a value that looks like a secret (secrets.SensitiveKey), after the image's own env is gone, is never written: it becomes
//     ${secret:NAME} and a note says to add it with `tink secret set`.
//
// What it cannot know is said, not guessed: why a thing is the way it is (comments), imperative steps (kind: exec), dependencies
// between instances, a volume's copy targets, and what to do about a volume's backup (left out, so plan warns until someone decides).
// Finally the result is planned against the live server with the same code `tink plan` runs, so "exported" means "plan says no changes",
// and anything else is printed.

// ExportOptions says what to export.
type ExportOptions struct {
	// Project is the Incus project the instances are in ("" is the server's default project).
	Project string
	// Instances are the names to export. Their volumes and project come with them.
	Instances []string
	// Offline does not ask registries: the image cannot be verified or pinned and oci.*/environment.* are not compared with the image.
	Offline bool
	// NoPin writes the reference as the instance records it instead of pinning it to the digest the registry reports.
	NoPin bool
}

// ExportResult is a stack file and what the exporter wants a reader to know about it.
type ExportResult struct {
	// YAML is the stack, one document per resource. Never holds a secret value.
	YAML string
	// Notes are decisions the exporter made and things it could not know, for stderr.
	Notes []string
	// Plans is the result of planning YAML against the live server, in the order of the file: "no changes" for each is the goal. Each
	// Resource is reduced to its kind and name: the rest may hold a secret's value.
	Plans []PlannedResource
	// Verified is true when every resource planned as no changes.
	Verified bool
}

// exportDoc is one resource on its way to text: the resource as it is live (secret values included, for the self-check), the text to
// show for config keys whose value must not be written, and comments.
type exportDoc struct {
	res      Resource
	shown    map[string]string // config key -> text to write instead of its value
	comments []string          // leading comment lines
	trailing []string          // comment lines after the last field
	raw      string            // pre-rendered extra fields (a backup block), written after pool/config
}

// Export reads the named instances (and the project and volumes they use) from server.
func Export(server incus.InstanceServer, opts ExportOptions) (*ExportResult, error) {
	if len(opts.Instances) == 0 {
		return nil, fmt.Errorf("name at least one instance to export")
	}
	project := opts.Project
	if project == "default" {
		project = ""
	}
	scoped := server
	if project != "" {
		scoped = server.UseProject(project)
	}

	env := newImageEnv(opts.Offline)
	res := &ExportResult{}
	var docs []exportDoc
	seenVolume := map[string]bool{}

	if project != "" {
		p, _, found, err := incusapi.LookupProject(server, project)
		if err != nil {
			return nil, fmt.Errorf("reading project %s: %w", project, err)
		}
		if !found {
			return nil, fmt.Errorf("project %q does not exist", project)
		}
		docs = append(docs, exportDoc{res: Resource{Kind: KindProject, Name: project, Config: copyMap(p.Config)}})
	}

	var instDocs []exportDoc
	for _, name := range opts.Instances {
		inst, _, found, err := incusapi.LookupInstance(scoped, name)
		if err != nil {
			return nil, fmt.Errorf("reading instance %s: %w", name, err)
		}
		if !found {
			if project == "" {
				return nil, fmt.Errorf("instance %q does not exist (use --project for one in another project)", name)
			}
			return nil, fmt.Errorf("instance %q does not exist in project %q", name, project)
		}

		volDocs, err := exportVolumes(scoped, inst, project, seenVolume, res)
		if err != nil {
			return nil, err
		}
		docs = append(docs, volDocs...)
		instDocs = append(instDocs, exportInstance(server, scoped, env, inst, project, opts, res))
	}
	docs = append(docs, instDocs...)

	res.YAML = renderDocs(docs)

	// Self-check: plan what we just exported. It is the same code `tink plan` runs, on the same data, so what is printed here is what
	// the person would see running plan on the file.
	resources := make([]Resource, len(docs))
	for i, d := range docs {
		resources[i] = d.res
	}
	plans, err := PlanWithOptions(server, resources, PlanOptions{env: env, Offline: opts.Offline})
	if err != nil {
		return res, fmt.Errorf("planning the export against the live server: %w", err)
	}
	res.Verified = true
	for _, p := range plans {
		if p.Action != ActionNone {
			res.Verified = false
		}
		p.Resource = Resource{Kind: p.Resource.Kind, Name: p.Resource.Name}
		res.Plans = append(res.Plans, p)
	}
	return res, nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// exportVolumes returns a storage-volume document for each custom volume inst's own disk devices attach, in the pool they name.
func exportVolumes(scoped incus.InstanceServer, inst *api.Instance, project string, seen map[string]bool, res *ExportResult) ([]exportDoc, error) {
	var docs []exportDoc
	for _, dname := range sortedKeys(inst.Devices) {
		dev := inst.Devices[dname]
		if dev["type"] != "disk" || dev["pool"] == "" || dev["source"] == "" || strings.HasPrefix(dev["source"], "/") {
			continue // a bind mount, or the root disk
		}
		pool, name := dev["pool"], dev["source"]
		key := pool + "/" + name
		if seen[key] {
			continue
		}
		seen[key] = true

		vol, _, found, err := incusapi.LookupVolume(scoped, pool, "custom", name)
		if err != nil {
			return nil, fmt.Errorf("reading volume %s: %w", key, err)
		}
		if !found {
			res.Notes = append(res.Notes, fmt.Sprintf("volume %s: %s/%s attaches it but it does not exist; not exported (apply would create it empty)", key, inst.Name, dname))
			continue
		}

		d := exportDoc{res: Resource{Kind: KindStorageVolume, Name: name, Project: project, Pool: pool, Config: map[string]string{}}}
		var snapSchedule, snapExpiry string
		for k, v := range vol.Config {
			switch {
			case strings.HasPrefix(k, "volatile."):
			case k == backupmeta.KeySnapshotSchedule:
				snapSchedule = v
			case k == backupmeta.KeySnapshotExpiry:
				snapExpiry = v
			case k == backupmeta.PolicyKey:
				d.trailing = append(d.trailing, "this volume carries a copy policy (user.tink.backup.policy); the backup-target resources it names cannot be rebuilt from the volume, declare them in the stack")
				res.Notes = append(res.Notes, fmt.Sprintf("volume %s: carries a copy policy; copies and their targets are not exported", key))
			case strings.HasPrefix(k, "user.tink.backup.") || k == backupmeta.StackKey:
				// tink's own stamps (last copy, failures, owning stack): state, not declaration
			default:
				d.res.Config[k] = v
			}
		}
		if snapSchedule != "" || snapExpiry != "" {
			d.res.Backup = &backupmeta.VolumeBackup{Snapshots: &backupmeta.SnapshotPolicy{Schedule: snapSchedule, Retain: snapExpiry}}
			d.raw = fmt.Sprintf("backup:\n  snapshots:\n    schedule: %s\n    retain: %s\n", scalar(snapSchedule), scalar(snapExpiry))
			// the keys are written by the backup block; the planned resource must not also carry them as config
		} else if d.raw == "" {
			d.trailing = append(d.trailing, "no backup: block on purpose: the live volume says nothing about how it is backed up, and an invented answer would hide that. plan warns until you decide (docs/volume-backup.md)")
			res.Notes = append(res.Notes, fmt.Sprintf("volume %s: no backup policy exported (none is set on it); plan will warn", key))
		}
		docs = append(docs, d)
	}
	return docs, nil
}

// exportInstance builds the instance document, applying the filters in the package comment.
func exportInstance(server, scoped incus.InstanceServer, env *imageEnv, inst *api.Instance, project string, opts ExportOptions, res *ExportResult) exportDoc {
	cfg := copyMap(inst.Config)
	d := exportDoc{
		res:   Resource{Kind: KindInstance, Name: inst.Name, Project: project, Profiles: append([]string{}, inst.Profiles...), Devices: map[string]map[string]string{}},
		shown: map[string]string{},
	}
	note := func(format string, args ...any) {
		res.Notes = append(res.Notes, fmt.Sprintf("instance %s: ", inst.Name)+fmt.Sprintf(format, args...))
	}
	if inst.Type == "virtual-machine" {
		d.res.VM = true
	}
	d.res.Ephemeral = inst.Ephemeral
	for name, dev := range inst.Devices {
		d.res.Devices[name] = copyMap(dev)
	}

	if cmd := cfg[run.KeyCommand]; cmd != "" {
		d.comments = append(d.comments, "created by: "+cmd)
		delete(cfg, run.KeyCommand)
	}

	// 1. The image, and with it what the image itself says about oci.* and environment.*
	var rt *ociRuntime
	if cfg["volatile.container.oci"] == "true" {
		image, runtime, comment := exportImage(server, env, inst, cfg, opts, note)
		d.res.Image = image
		d.comments = append(d.comments, comment...)
		rt = runtime
	} else {
		d.comments = append(d.comments, fmt.Sprintf("image: not an OCI image (built from %q); set image: to the alias or reference to build it from", cfg["image.description"]))
		note("not an OCI instance: image: left for you to fill in")
	}

	// 2. Config: drop what Incus and the image wrote.
	derived := map[string]bool{}
	if rt != nil {
		derived = imageDerivedKeys(cfg, *rt)
		_, differing := runtimeConfigDiff(cfg, nil, *rt)
		for _, k := range differing {
			if _, set := cfg[k]; set && !strings.HasPrefix(k, "environment.") {
				note("%s differs from the image's own: kept as an override", k)
			}
		}
	}
	d.res.Config = map[string]string{}
	var dropped []string
	for _, k := range sortedKeys(cfg) {
		v := cfg[k]
		switch {
		case strings.HasPrefix(k, "volatile."), strings.HasPrefix(k, "image."):
			continue
		case derived[k]:
			dropped = append(dropped, k)
			continue
		case rt != nil && (k == "environment.HOME" && v == "/root" || k == "environment.TERM" && v == "xterm"):
			dropped = append(dropped, k) // set by Incus at creation, not by the image or a person
			continue
		}
		d.res.Config[k] = v
	}
	if rt != nil && len(dropped) > 0 {
		note("left out %d key(s) that equal what the image sets: %s", len(dropped), strings.Join(dropped, ", "))
	} else if rt == nil && cfg["volatile.container.oci"] == "true" {
		note("oci.* and environment.* are written as they are on the instance: they could not be compared with the image, so some are the image's own")
	}

	// 3. Secrets: never write a value that looks like one.
	d.res.SecretKeys = map[string]bool{}
	for _, k := range sortedKeys(d.res.Config) {
		if !strings.HasPrefix(k, "environment.") || !secrets.SensitiveKey(k) {
			continue
		}
		name := secretName(inst.Name, strings.TrimPrefix(k, "environment."))
		d.shown[k] = "${secret:" + name + "}"
		d.res.SecretKeys[k] = true
		note("%s looks like a secret: written as ${secret:%s}; add the value with `tink secret set %s`", k, name, name)
	}
	return d
}

// exportImage works out the image: reference. The reference is a candidate from image.id; it is kept only as the registry confirms it.
func exportImage(server incus.InstanceServer, env *imageEnv, inst *api.Instance, cfg map[string]string, opts ExportOptions, note func(string, ...any)) (image string, rt *ociRuntime, comments []string) {
	id := cfg["image.id"]
	remoteName, remote := ociRemoteFor(env, cfg["image.description"])
	if id == "" || remoteName == "" {
		comments = append(comments, fmt.Sprintf("image: could not be worked out (image.id %q, built from %q); set it by hand", id, cfg["image.description"]))
		note("image: could not be worked out; left for you to fill in")
		return "", nil, comments
	}
	candidate := remoteName + ":" + id

	if opts.Offline {
		note("image %s is as recorded (image.id), unverified and unpinned (--offline)", candidate)
		return candidate, nil, nil
	}

	chk := env.checkInstance(server, inst, Resource{Image: candidate})
	switch {
	case len(chk.Drift) > 0:
		// The instance was not built from what image.id names. Do not present a wrong answer as a right one.
		comments = append(comments, "image: WARNING: image.id says "+candidate+" but the instance was built from something else; check it before applying ("+strings.Join(chk.Drift, "; ")+")")
		note("image %s does NOT match what the instance was built from: %s", candidate, strings.Join(chk.Drift, "; "))
		return candidate, nil, comments
	case len(chk.Unverified) > 0:
		note("image %s could not be verified: %s", candidate, strings.Join(chk.Unverified, "; "))
		return candidate, nil, nil
	}

	ref := strings.TrimPrefix(candidate, remoteName+":")
	if rtVal, err := env.runtimeConfig(server, remote, ref); err == nil {
		rt = &rtVal
	} else {
		note("could not read the image's own config (%v): oci.* and environment.* cannot be compared with it", err)
	}
	if _, _, digest := splitRef(ref); digest == "" && !opts.NoPin {
		if img, err := env.registryImage(server, remoteName, ref); err == nil && img.Digest != "" {
			candidate += "@" + img.Digest
			note("image pinned to %s (verified against the registry; --no-pin to keep the bare reference)", img.Digest)
		}
	}
	return candidate, rt, nil
}

// imageDerivedKeys are the oci.* and environment.* keys of cfg whose value is what the image itself says (so no person wrote it): the
// keys runtimeConfigDiff does NOT report as different. A key that differs is an override (or stale after a rebuild) and is not in the set.
// An environment variable the image does not define at all is not in the set either: nobody but a person put it there.
func imageDerivedKeys(cfg map[string]string, rt ociRuntime) map[string]bool {
	_, differing := runtimeConfigDiff(cfg, nil, rt)
	differs := map[string]bool{}
	for _, k := range differing {
		differs[k] = true
	}
	derived := map[string]bool{}
	for _, k := range []string{"oci.entrypoint", "oci.cwd", "oci.uid"} {
		if !differs[k] {
			derived[k] = true
		}
	}
	// runtimeConfigDiff compares the group only when the image names one (user "uid:gid"), so "not reported as different" does not mean
	// "equal" here: an image with no user, or a bare uid, says nothing about the group, and a gid a person set (--user 1000:1000) would be
	// dropped as the image's own. The group is the image's own only when it is the root group, or the one the image names.
	if !differs["oci.gid"] {
		_, igid, hasGID, ok := parseImageUser(rt.User)
		switch gid := cfg["oci.gid"]; {
		case gid == "" || gid == "0":
			derived["oci.gid"] = true
		case ok && hasGID && gid == strconv.Itoa(igid):
			derived["oci.gid"] = true
		}
	}
	for _, kv := range rt.Env {
		k, _, _ := strings.Cut(kv, "=")
		if !differs["environment."+k] {
			derived["environment."+k] = true
		}
	}
	return derived
}

// ociRemoteFor finds the configured OCI remote for an image.description such as "docker.io/louislam/uptime-kuma (OCI)".
func ociRemoteFor(env *imageEnv, description string) (string, cliconfig.Remote) {
	host, _, _ := strings.Cut(strings.TrimSuffix(description, " (OCI)"), "/")
	var names []string
	for n, r := range env.remotes() {
		if r.Protocol == "oci" && remoteHost(r) == host {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", cliconfig.Remote{}
	}
	sort.Strings(names)
	return names[0], env.remotes()[names[0]]
}

// secretName is a store name for an environment variable of an instance.
func secretName(instance, key string) string {
	return strings.ToLower(instance + "-" + key)
}

// scalar writes s as a YAML scalar.
func scalar(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimRight(string(b), "\n")
}

// renderDocs writes the documents as a stack file. Written by hand, not marshalled: a reader will edit it, so the order of fields, the
// comments and the quoting have to be chosen.
func renderDocs(docs []exportDoc) string {
	var b strings.Builder
	b.WriteString("# Exported by `tink export` from live objects. A starting point to review, not the stack: it records what is\n")
	b.WriteString("# there, not why, and not what is done to it (kind: exec), what depends on what, or how it is backed up.\n")
	for i, d := range docs {
		if i > 0 {
			b.WriteString("---\n")
		}
		for _, c := range d.comments {
			fmt.Fprintf(&b, "# %s\n", c)
		}
		r := d.res
		fmt.Fprintf(&b, "kind: %s\nname: %s\n", r.Kind, scalar(r.Name))
		if r.Project != "" && r.Kind != KindProject {
			fmt.Fprintf(&b, "project: %s\n", scalar(r.Project))
		}
		if r.Kind == KindInstance {
			if r.Image != "" {
				fmt.Fprintf(&b, "image: %s\n", scalar(r.Image))
			}
			if r.VM {
				b.WriteString("vm: true\n")
			}
			if r.Ephemeral {
				b.WriteString("ephemeral: true\n")
			}
			b.WriteString("profiles:\n")
			for _, p := range r.Profiles {
				fmt.Fprintf(&b, "  - %s\n", scalar(p))
			}
		}
		if r.Kind == KindStorageVolume {
			fmt.Fprintf(&b, "pool: %s\n", scalar(r.Pool))
		}
		if len(r.Config) > 0 {
			b.WriteString("config:\n")
			for _, k := range sortedKeys(r.Config) {
				v := r.Config[k]
				if s, ok := d.shown[k]; ok {
					fmt.Fprintf(&b, "  %s: %s\n", k, s) // a ${secret:...} reference: plain, never quoted into something else
					continue
				}
				fmt.Fprintf(&b, "  %s: %s\n", k, scalar(v))
			}
		}
		if len(r.Devices) > 0 {
			b.WriteString("devices:\n")
			for _, n := range sortedKeys(r.Devices) {
				fmt.Fprintf(&b, "  %s:\n", n)
				dev := r.Devices[n]
				if t, ok := dev["type"]; ok {
					fmt.Fprintf(&b, "    type: %s\n", scalar(t))
				}
				for _, k := range sortedKeys(dev) {
					if k != "type" {
						fmt.Fprintf(&b, "    %s: %s\n", k, scalar(dev[k]))
					}
				}
			}
		}
		b.WriteString(d.raw)
		for _, c := range d.trailing {
			fmt.Fprintf(&b, "# %s\n", c)
		}
	}
	return b.String()
}
