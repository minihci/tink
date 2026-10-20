package resolve

import (
	"github.com/minihci/tink/internal/backupext"
)

// BuildPolicy resolves a storage volume's declaration into the policy its volume should carry, as the exact text that is
// stored (so two builds of the same declaration compare equal). "" means the volume should carry no policy: it has no
// copies and no verification, or it opts out of backup. The policy's shape and encoding are the backup capability's (backupext.Backup.Policy);
// this hands it the block and the targets its copies name.
func BuildPolicy(r Resource, targets map[string]Resource) (string, error) {
	var named map[string]backupext.Target
	if len(targets) > 0 {
		named = make(map[string]backupext.Target, len(targets))
		for name, t := range targets {
			named[name] = backupext.Target{Name: t.Name, Location: t.Location, Engine: t.Engine, Remote: t.Remote, Pool: t.Pool}
		}
	}
	return backupext.Get().Policy(r.Backup, named)
}
