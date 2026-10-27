package authz

import "fmt"

// Volume names a custom storage volume.
type Volume struct{ Project, Pool, Name string }

func (v Volume) object() string {
	return fmt.Sprintf("storage_volume:%s/%s/custom/%s", v.Project, v.Pool, v.Name)
}

// Instance names an instance.
type Instance struct{ Project, Name string }

// Copy is a volume the daemon copies and where its restore points land. A restore point is a volume named SOURCE-bk-TIME in the source's project on each target
// pool (docs/volume-backup.md).
type Copy struct {
	Source      Volume
	TargetPools []string
}

// Scope is what the daemon touches on one server. It is read from the server when the scriptlet is generated, so the rules name what is there and not what
// someone remembered to write.
type Scope struct {
	// Status is the volume the status document is written on (its config), or nil when the daemon publishes none.
	Status *Volume
	// Copies are the volumes that carry a copy policy.
	Copies []Copy
	// Reconcile says the daemon reconciles the dns records: it reads the instances, profiles and networks, subscribes to events, and DNS are the instances it
	// writes files on.
	Reconcile bool
	DNS       []Instance

	// Derived says the daemon keeps the stamped derived files (--reconcile-derived): it reads and watches what the dns reconciler does, and writes files on
	// Files, the instances that carry the stamp of a derived file. Restart are those among them whose stamp says restart: the daemon may restart them (the
	// entitlement is can_update_state, which is not a right to edit the instance or to run anything in it).
	Derived bool
	Files   []Instance
	Restart []Instance
}

// DaemonRules are the rules for the daemon's certificate: the pairs it was asked for on Incus 7.5.1 (a daemon, a scheduler, a copy, the status write and the
// reconciler, run as a TLS client against a scriptlet that recorded every request: L-2026-10-26c), and no others.
//
// There is no right to create, edit, delete or run anything in an instance, to change a network or a project, or to touch certificates or the server's
// config. An edit right on an instance covers its devices, and a device can be a way into the host, which is why the status document moved to a volume. What
// it can write is: the config of one status volume, the volumes it copies (their config and their snapshots), the restore points it creates, files in the
// dns helper, and with --reconcile-derived files on the instances that carry a derived file's stamp, and a restart of those whose stamp says so.
func DaemonRules(s Scope) []Rule {
	var rules []Rule
	view := func(prefix string) {
		rules = append(rules, Rule{Object: prefix, Entitlement: "can_view", Prefix: true})
	}

	// What it reads, everywhere: the scheduler lists the volumes and the pools to find the policies, and every list the server answers asks for each
	// object it would list.
	rules = append(rules, Rule{Object: "server:incus", Entitlement: "can_view"})
	view("project:")
	view("storage_pool:")
	view("storage_volume:")
	view("image:")

	if s.Status != nil {
		rules = append(rules, Rule{Object: s.Status.object(), Entitlement: "can_edit"})
	}

	created := map[string]bool{}
	for _, c := range s.Copies {
		src := c.Source.object()
		rules = append(rules,
			Rule{Object: src, Entitlement: "can_edit"},
			Rule{Object: src, Entitlement: "can_manage_snapshots"},
			Rule{Object: fmt.Sprintf("project:%s", c.Source.Project), Entitlement: "can_view_operations"},
		)
		if !created[c.Source.Project] {
			created[c.Source.Project] = true
			rules = append(rules, Rule{Object: fmt.Sprintf("project:%s", c.Source.Project), Entitlement: "can_create_storage_volumes"})
		}
		for _, pool := range c.TargetPools {
			rules = append(rules, Rule{
				Object:      Volume{Project: c.Source.Project, Pool: pool, Name: c.Source.Name + "-bk-"}.object(),
				Entitlement: "can_edit",
				Prefix:      true,
			})
		}
	}

	if s.Reconcile || s.Derived {
		view("instance:")
		view("profile:")
		view("network:")
		view("network_acl:") // a network's config is shown only to a client that may edit it, so the ACLs attached to it are read from the ACLs' used-by lists
		rules = append(rules,
			Rule{Object: "project:", Entitlement: "can_view_events", Prefix: true},
			Rule{Object: "project:", Entitlement: "can_view_operations", Prefix: true},
		)
		if s.Reconcile {
			for _, in := range s.DNS {
				rules = append(rules, Rule{Object: fmt.Sprintf("instance:%s/%s", in.Project, in.Name), Entitlement: "can_access_files"})
			}
		}
	}
	if s.Derived {
		for _, in := range s.Files {
			rules = append(rules, Rule{Object: fmt.Sprintf("instance:%s/%s", in.Project, in.Name), Entitlement: "can_access_files"})
		}
		for _, in := range s.Restart {
			rules = append(rules, Rule{Object: fmt.Sprintf("instance:%s/%s", in.Project, in.Name), Entitlement: "can_update_state"})
		}
	}
	return rules
}
