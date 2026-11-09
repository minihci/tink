package join

import (
	"strings"

	"github.com/lxc/incus/v7/shared/api"

	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/ownership"
	"github.com/minihci/tink/internal/resolve"
)

// IgnoredStamps is an instance of the server whose stamps were not read, and the finding that says so.
type IgnoredStamps struct {
	Project, Instance string
	Finding           finding.Finding
}

// readableStamps is the config of an instance as the join may read it from the server. An instance whose stamps are its own (ownership.Classify: here), one
// that no stack stamped and one that another stack stamped are read as they are. One that is a copy of another object (it carries this stack's stamps and
// the resource stamp of another instance), or whose owner stamp has no resource stamp yet, is read without its intent keys: its publish, site and derive
// stamps describe the original (or cannot yet be told from a copy's), and acting on them would publish a name twice or write a file twice and restart the
// instance. The second result is the one finding for it, or nil when there was nothing to ignore (the instance carries none of the stamps the join reads),
// so an instance made before stamps is not reported just for being there.
//
// The finding is a warning and not an error: the join fails closed on an error, and a copy that someone made by hand must not stop a site's derived files.
// The reasons are fixed phrases; the detail may name the instance.
func readableStamps(in api.Instance) (map[string]string, *IgnoredStamps) {
	owner := ownership.Owner(in.Config)
	if owner == "" {
		return in.Config, nil
	}
	state, _ := ownership.Classify(in.Config, owner, ownership.Address(ownership.KindInstance, in.Project, "", in.Name))
	if state != ownership.Copy && state != ownership.Unbound {
		return in.Config, nil
	}
	cfg := make(map[string]string, len(in.Config))
	reads := false // does it carry anything the join would have read?
	for k, v := range in.Config {
		if ownership.ClassOf(k) == ownership.Intent {
			reads = reads || strings.HasPrefix(k, resolve.PublishStampPrefix) || strings.HasPrefix(k, resolve.DeriveStampPrefix) || k == resolve.SiteKey
			continue
		}
		cfg[k] = v
	}
	if !reads {
		return cfg, nil
	}
	subject := "instance/" + in.Name
	var f finding.Finding
	switch state {
	case ownership.Copy:
		f = finding.Warnf("an instance's stamps were made for another object (a copy?)", subject,
			"(project %s) it carries the stamps of another instance, so its publish, site and derive stamps are ignored", projectOrDefault(in.Project))
	default:
		f = finding.Warnf("an instance has no resource stamp yet", subject,
			"(project %s) its publish, site and derive stamps are ignored until `plan apply` writes its resource stamp", projectOrDefault(in.Project))
	}
	return cfg, &IgnoredStamps{Project: projectOrDefault(in.Project), Instance: in.Name, Finding: f}
}

func projectOrDefault(p string) string {
	if p == "" {
		return "default"
	}
	return p
}
