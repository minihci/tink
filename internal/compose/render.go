package compose

import (
	"fmt"
	"sort"
	"strings"

	yaml "go.yaml.in/yaml/v4"
)

// render writes the stack: the stack's name, the managed volumes, then the instances in the order of the compose file. Written by hand, not
// marshalled, because a person will edit it: the order of fields, the comments and the quoting are chosen.
func (im *importer) render(stack, egress string) string {
	var b strings.Builder
	b.WriteString("# Imported by `tink import` from a compose file. A starting point to review, not the stack: it states what the compose file\n")
	b.WriteString("# says about the app, and nothing about the site (its project, address, name, sign-on or backup).\n")
	fmt.Fprintf(&b, "kind: stack\nname: %s\n", scalar(stack))
	b.WriteString("network: own  # a bridge of its own, made and secured by tink; egress needs it\n")
	if egress == "open" {
		b.WriteString("egress: open  # compose lets a service send traffic out unless every network it is on is `internal: true`\n")
	} else {
		b.WriteString("egress: none  # every service is on `internal: true` networks only\n")
	}

	names := make([]string, 0, len(im.volumes))
	for n, v := range im.volumes {
		if v.used {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		v := im.volumes[n]
		b.WriteString("---\n")
		fmt.Fprintf(&b, "kind: storage-volume\nname: %s\npool: %s\n", scalar(n), scalar(im.opts.Pool))
		if len(v.config) > 0 {
			b.WriteString("config:\n")
			for _, k := range sortedKeys(v.config) {
				fmt.Fprintf(&b, "  %s: %s\n", k, scalar(v.config[k]))
			}
		}
		b.WriteString("# backup: not decided. Say how this volume is backed up (`backup: {snapshots: ...}`, or `backup: {none: \"why\"}`); `tink plan` warns until you do.\n")
	}
	for _, d := range im.docs {
		b.WriteString("---\n")
		for _, c := range d.comments {
			fmt.Fprintf(&b, "# %s\n", c)
		}
		fmt.Fprintf(&b, "kind: instance\nname: %s\nimage: %s\nprofiles:\n  - default\n", scalar(d.name), scalar(d.image))
		if len(d.dependsOn) > 0 {
			b.WriteString("depends_on:\n")
			for _, n := range d.dependsOn {
				fmt.Fprintf(&b, "  - %s\n", scalar(n))
			}
		}
		if len(d.config) > 0 {
			b.WriteString("config:\n")
			for _, k := range sortedKeys(d.config) {
				fmt.Fprintf(&b, "  %s: %s\n", k, scalar(d.config[k]))
			}
		}
		if len(d.devices) > 0 {
			b.WriteString("devices:\n")
			for _, n := range sortedKeys(d.devices) {
				fmt.Fprintf(&b, "  %s:\n", n)
				dev := d.devices[n]
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
	}
	return b.String()
}

func scalar(s string) string {
	b, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Sprintf("%q", s)
	}
	return strings.TrimRight(string(b), "\n")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
