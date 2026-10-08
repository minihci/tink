package resolve

import (
	"fmt"
	"strings"

	"github.com/minihci/tink/internal/backupmeta"
)

// BuildPolicy resolves a storage volume's declaration into the policy its volume should carry, as the exact text that is
// stored (so two builds of the same declaration compare equal). "" means the volume should carry no policy: it has no
// copies and no verification, or it opts out of backup.
func BuildPolicy(r Resource, targets map[string]Resource) (string, error) {
	b := r.Backup
	if b == nil || b.None != "" || (len(b.Copies) == 0 && b.Verify == "" && b.VerifyCheck == nil) {
		return "", nil
	}
	p := backupmeta.BackupPolicy{Proto: backupmeta.PolicyProto}
	for _, c := range b.Copies {
		t, ok := targets[c.Target]
		if !ok {
			return "", fmt.Errorf("copy target %q is not a kind: backup-target in this stack", c.Target)
		}
		p.Copies = append(p.Copies, backupmeta.PolicyCopy{
			Target:   backupmeta.PolicyTarget{Name: t.Name, Location: t.Location, Engine: t.Engine, Remote: t.Remote, Pool: t.Pool},
			Schedule: strings.TrimSpace(c.Schedule),
			Retain:   strings.TrimSpace(c.Retain),
		})
	}
	if b.Verify != "" || b.VerifyCheck != nil {
		v := &backupmeta.PolicyVerify{Every: b.Verify}
		if c := b.VerifyCheck; c != nil {
			v.Check = &backupmeta.PolicyCheck{Image: c.Image, Command: c.Command, Mount: c.Mount}
		}
		p.Verify = v
	}
	return backupmeta.EncodePolicy(p)
}
