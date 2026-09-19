package resolve

import (
	"fmt"
	"reflect"
)

// fieldOwners names, for each Resource field that isn't common to every
// kind, which Kinds are allowed to set it. Kind, Name, Project, and
// DependsOn aren't listed here -- every kind uses those, so they're
// never flagged. This is the single place that answers "does kind X use
// field Y," replacing what today is only spelled out in doc comments
// above each field in resource.go -- kept alongside kindPriority as
// another small table rather than a typed struct per kind, matching
// this package's existing "deliberately small substitute" approach
// (see resource.go's own package doc comment).
var fieldOwners = map[string][]Kind{
	"Image":        {KindInstance},
	"Profiles":     {KindInstance},
	"VM":           {KindInstance},
	"Pool":         {KindStorageVolume},
	"Config":       {KindProject, KindProfile, KindInstance},
	"Devices":      {KindProfile, KindInstance},
	"Instance":     {KindFile, KindExec},
	"Path":         {KindFile},
	"Content":      {KindFile},
	"Restart":      {KindFile, KindInstance},
	"Check":        {KindIncus, KindExec},
	"Command":      {KindIncus, KindExec},
	"Triggers":     {KindExec},
	"AgentTimeout": {KindExec},
	"Alias":        {KindImage},
	"Source":       {KindImage},
	"Architecture": {KindImage},
	"Properties":   {KindImage},
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
	return nil
}

func kindAllowed(k Kind, owners []Kind) bool {
	for _, o := range owners {
		if o == k {
			return true
		}
	}
	return false
}
