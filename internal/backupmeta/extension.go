package backupmeta

import (
	"fmt"
	"strings"
	"time"

	"github.com/minihci/tink/internal/backupext"
)

// This package is the backup capability the converger asks questions of (backupext.Backup). It registers itself, so a program that links
// backupmeta has backup and the converger (internal/resolve) never imports it.
func init() { backupext.Register(extension{}) }

// extension is backupext.Backup, in terms of the functions of this package.
type extension struct{}

func (extension) Keys() backupext.Keys {
	return backupext.Keys{
		SnapshotSchedule: KeySnapshotSchedule,
		SnapshotExpiry:   KeySnapshotExpiry,
		Policy:           PolicyKey,
		Prefix:           "user.tink.backup.",
	}
}

func (extension) PolicyProto() int { return PolicyProto }

func (extension) Validate(volume string, b *VolumeBackup) error { return ValidateBackup(volume, b) }

func (extension) SnapshotConfig(b *VolumeBackup) map[string]string { return SnapshotConfig(b) }

func (extension) DescribePolicyChange(have, want string) []string {
	return DescribePolicyChange(have, want)
}

func (extension) CopyWarnings(volume string, b *VolumeBackup, current map[string]string, now time.Time) []string {
	return CopyWarnings(volume, b, current, now)
}

func (extension) VerifyWarning(volume string, b *VolumeBackup, current map[string]string, now time.Time) string {
	return VerifyWarning(volume, b, current, now)
}

// Policy resolves a storage volume's declaration into the policy its volume should carry, as the exact text that is
// stored (so two builds of the same declaration compare equal). "" means the volume should carry no policy: it has no
// copies and no verification, or it opts out of backup.
func (extension) Policy(b *VolumeBackup, targets map[string]backupext.Target) (string, error) {
	if b == nil || b.None != "" || (len(b.Copies) == 0 && b.Verify == "" && b.VerifyCheck == nil) {
		return "", nil
	}
	p := BackupPolicy{Proto: PolicyProto}
	for _, c := range b.Copies {
		t, ok := targets[c.Target]
		if !ok {
			return "", fmt.Errorf("copy target %q is not a kind: backup-target in this stack", c.Target)
		}
		p.Copies = append(p.Copies, PolicyCopy{
			Target:   PolicyTarget{Name: t.Name, Location: t.Location, Engine: t.Engine, Remote: t.Remote, Pool: t.Pool},
			Schedule: strings.TrimSpace(c.Schedule),
			Retain:   strings.TrimSpace(c.Retain),
		})
	}
	if b.Verify != "" || b.VerifyCheck != nil {
		v := &PolicyVerify{Every: b.Verify}
		if c := b.VerifyCheck; c != nil {
			v.Check = &PolicyCheck{Image: c.Image, Command: c.Command, Mount: c.Mount}
		}
		p.Verify = v
	}
	return EncodePolicy(p)
}
