package resolve

import (
	"fmt"
	"reflect"
)

// fieldOwners names, for each Resource field that isn't common to every
// kind, which Kinds are allowed to set it. Kind, Name, Project, and
// DependsOn aren't listed here -- every kind uses those, so they're
// never flagged. (One exception, checked in validateNetwork and validateNetworkACL: a network and a network ACL are
// server-global and reject a Project rather than silently ignoring it.) This is the single place that answers "does kind X use
// field Y," replacing what today is only spelled out in doc comments
// above each field in resource.go -- kept alongside kindPriority as
// another small table rather than a typed struct per kind, matching
// this package's existing "deliberately small substitute" approach
// (see resource.go's own package doc comment).
var fieldOwners = map[string][]Kind{
	"Image":           {KindInstance, KindHelper},
	"Profiles":        {KindInstance},
	"VM":              {KindInstance},
	"Ephemeral":       {KindInstance},
	"Pool":            {KindStorageVolume, KindBackupTarget, KindHelper},
	"Location":        {KindBackupTarget},
	"Engine":          {KindBackupTarget},
	"Remote":          {KindBackupTarget},
	"Address":         {KindBackupTarget},
	"Fingerprint":     {KindBackupTarget},
	"Backup":          {KindStorageVolume},
	"Config":          {KindProject, KindProfile, KindInstance, KindStorageVolume, KindNetwork, KindNetworkACL},
	"Description":     {KindNetwork, KindNetworkACL},
	"AllowDisruption": {KindNetwork},
	"Ingress":         {KindNetworkACL},
	"Egress":          {KindNetworkACL},
	"DeriveIngress":   {KindNetworkACL},
	"Devices":         {KindProfile, KindInstance},
	"Defaults":        {KindStack},
	"Instance":        {KindFile, KindExec, KindPublish},
	"Ownership":       {KindStack},
	"Offer":           {KindPublish},
	"Network":         {KindHelper, KindStack},
	"NetworkPool":     {KindStack},
	"NetworkPrefix":   {KindStack},
	"Timezone":        {KindHelper},
	"Remotes":         {KindHelper},
	"Allow":           {KindPublish},
	"Path":            {KindFile},
	"Content":         {KindFile},
	"Restart":         {KindFile, KindInstance},
	"OnImageChange":   {KindInstance},
	"SnapshotVolumes": {KindInstance},
	"Check":           {KindIncus, KindExec},
	"Command":         {KindIncus, KindExec},
	"Triggers":        {KindExec},
	"CheckOutput":     {KindExec},
	"AgentTimeout":    {KindExec},
	"Timeout":         {KindExec},
	"Alias":           {KindImage},
	"Source":          {KindImage},
	"Architecture":    {KindImage},
	"Properties":      {KindImage},
}

// Validate rejects a Resource that sets a field its own Kind doesn't
// use -- e.g. a kind: project document carrying a stray pool:, or a
// translator bug that puts Check/Command on an instance. Every producer
// of a Resource (LoadFile today, a future k8s-Deployment translator)
// should call this right after building one, so a bad document fails
// before Levels or Plan ever runs -- the same "fail before touching
// Incus" guarantee --dry-run already gives you, just moved earlier.
func Validate(r Resource) error {
	if _, ok := kindPriority[r.Kind]; !ok {
		return fmt.Errorf("resource %q: unknown kind %q", r.Name, r.Kind)
	}

	v := reflect.ValueOf(r)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		name := t.Field(i).Name
		owners, scoped := fieldOwners[name]
		if !scoped || v.Field(i).IsZero() {
			continue
		}
		if !kindAllowed(r.Kind, owners) {
			return fmt.Errorf("resource %q: kind %q does not use field %q", r.Name, r.Kind, name)
		}
	}
	if err := validateSecretRefs(r); err != nil {
		return err
	}
	if err := validateBackup(r); err != nil {
		return err
	}
	if err := validateVolumeConfig(r); err != nil {
		return err
	}
	if err := validateBackupTarget(r); err != nil {
		return err
	}
	if err := validateStack(r); err != nil {
		return err
	}
	if err := validateStackOwnership(r); err != nil {
		return err
	}
	if err := validateStampKey(r); err != nil {
		return err
	}
	if err := validateStackDefaults(r); err != nil {
		return err
	}
	if err := validateNetwork(r); err != nil {
		return err
	}
	if err := validateNetworkACL(r); err != nil {
		return err
	}
	if err := validatePublish(r); err != nil {
		return err
	}
	if err := validateOwnedConfig(r); err != nil {
		return err
	}
	if err := validateHelper(r); err != nil {
		return err
	}
	return validateOnImageChange(r)
}

func kindAllowed(k Kind, owners []Kind) bool {
	for _, o := range owners {
		if o == k {
			return true
		}
	}
	return false
}
