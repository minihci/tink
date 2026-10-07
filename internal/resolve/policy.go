package resolve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/minihci/tink/internal/backupmeta"
)

// BackupPolicy is what the backupmeta.PolicyKey holds, as JSON. It is resolved: each copy carries its target inline, so the reader
// does not need the stack's kind: backup-target resources. Secrets can never be in it (it is plain volume config).
type BackupPolicy struct {
	Proto  int           `json:"proto"`
	Copies []PolicyCopy  `json:"copies,omitempty"`
	Verify *PolicyVerify `json:"verify,omitempty"`
}

// PolicyCopy is one copy: where, how often, and how long restore points are kept there.
type PolicyCopy struct {
	Target   PolicyTarget `json:"target"`
	Schedule string       `json:"schedule"`
	Retain   string       `json:"retain"`
}

// PolicyTarget is a kind: backup-target, resolved.
type PolicyTarget struct {
	Name     string `json:"name"`
	Location string `json:"location,omitempty"`
	Engine   string `json:"engine,omitempty"`
	Remote   string `json:"remote,omitempty"`
	Pool     string `json:"pool,omitempty"`
}

// PolicyVerify is how often a restore should be rehearsed, and the check that decides whether the data is good.
type PolicyVerify struct {
	Every string       `json:"every,omitempty"`
	Check *PolicyCheck `json:"check,omitempty"`
}

// PolicyCheck is a VerifyCheck as the volume stores it.
type PolicyCheck struct {
	Image   string   `json:"image"`
	Command []string `json:"command"`
	Mount   string   `json:"mount,omitempty"`
}

// BuildPolicy resolves a storage volume's declaration into the policy its volume should carry, as the exact text that is
// stored (so two builds of the same declaration compare equal). "" means the volume should carry no policy: it has no
// copies and no verification, or it opts out of backup.
func BuildPolicy(r Resource, targets map[string]Resource) (string, error) {
	b := r.Backup
	if b == nil || b.None != "" || (len(b.Copies) == 0 && b.Verify == "" && b.VerifyCheck == nil) {
		return "", nil
	}
	p := BackupPolicy{Proto: backupmeta.PolicyProto}
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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // a check's `&&` should read as `&&` in `incus storage volume show`
	if err := enc.Encode(p); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// ParsePolicy reads a backupmeta.PolicyKey value. It refuses a document of another protocol, an unknown field, and a copy that
// could not be scheduled, so a reader skips a volume it cannot understand instead of acting on half of it.
func ParsePolicy(s string) (BackupPolicy, error) {
	var p BackupPolicy
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return BackupPolicy{}, fmt.Errorf("%s is not a policy tink wrote: %w", backupmeta.PolicyKey, err)
	}
	if p.Proto != backupmeta.PolicyProto {
		return BackupPolicy{}, fmt.Errorf("%s was written for protocol %d; this tink speaks %d", backupmeta.PolicyKey, p.Proto, backupmeta.PolicyProto)
	}
	seen := map[string]bool{}
	for i, c := range p.Copies {
		if c.Target.Name == "" {
			return BackupPolicy{}, fmt.Errorf("%s: copies[%d] has no target name", backupmeta.PolicyKey, i)
		}
		if c.Target.Remote == "" && c.Target.Pool == "" {
			return BackupPolicy{}, fmt.Errorf("%s: copies[%d] (%s) has neither a remote nor a pool", backupmeta.PolicyKey, i, c.Target.Name)
		}
		if seen[c.Target.Name] {
			return BackupPolicy{}, fmt.Errorf("%s: names target %q twice", backupmeta.PolicyKey, c.Target.Name)
		}
		seen[c.Target.Name] = true
		if err := validateSchedule(c.Schedule); err != nil {
			return BackupPolicy{}, fmt.Errorf("%s: copies[%d] (%s) schedule: %w", backupmeta.PolicyKey, i, c.Target.Name, err)
		}
		if err := validateRetain(c.Retain); err != nil {
			return BackupPolicy{}, fmt.Errorf("%s: copies[%d] (%s) retain: %w", backupmeta.PolicyKey, i, c.Target.Name, err)
		}
	}
	if v := p.Verify; v != nil && v.Every != "" && !verifyCadences[v.Every] {
		return BackupPolicy{}, fmt.Errorf("%s: verify.every must be daily, weekly or monthly, got %q", backupmeta.PolicyKey, v.Every)
	}
	return p, nil
}
