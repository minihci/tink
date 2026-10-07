package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/resolve"
	"github.com/minihci/tink/internal/volbackup"
)

type listEngine struct{ vols []volbackup.ListedVolume }

func (e listEngine) LiveConfig(volbackup.Volume) (map[string]string, error) { return nil, nil }
func (e listEngine) Volumes() ([]volbackup.ListedVolume, map[string]error, error) {
	return e.vols, nil, nil
}
func (e listEngine) Copy(volbackup.Volume, volbackup.Target, volbackup.CopyOptions) (volbackup.CopyResult, error) {
	return volbackup.CopyResult{}, nil
}

func policied(project, pool, name, owner string) volbackup.ListedVolume {
	cfg := map[string]string{resolve.PolicyKey: `{"proto":1}`}
	if owner != "" {
		cfg[resolve.StackKey] = owner
	}
	return volbackup.ListedVolume{Volume: volbackup.Volume{Project: project, Pool: pool, Name: name}, Config: cfg}
}

func TestThePlanNoteSeparatesCertainOrphansFromGuessesAndIsSilentWithNone(t *testing.T) {
	vol := func(name string) resolve.Resource {
		return resolve.Resource{Kind: resolve.KindStorageVolume, Name: name, Backup: &resolve.VolumeBackup{None: "x"}}
	}
	eng := listEngine{vols: []volbackup.ListedVolume{
		policied("default", "default", "kept", "immich"),
		policied("default", "default", "dropped", "immich"),
		policied("tenant", "fast", "moved", "immich"),
		policied("default", "default", "mystery", ""),
		policied("default", "default", "theirs", "nextcloud"),
	}}
	var out bytes.Buffer
	noteUndeclaredPolicies(&out, eng, []resolve.Resource{{Kind: resolve.KindStack, Name: "immich"}, vol("kept")})
	got := out.String()
	for _, want := range []string{
		`2 volume(s) applied by stack "immich" carry a copy policy but the stack no longer declares them`,
		"dropped, tenant/moved (pool fast)",
		"tink backup forget",
		"1 volume(s) in this stack's projects and pools",
		"mystery",
		"point at no stack",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the note must contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "theirs") || strings.Contains(got, "kept") {
		t.Errorf("another stack's volume, and a declared one, are never mentioned:\n%s", got)
	}

	out.Reset()
	moved := vol("moved")
	moved.Project, moved.Pool = "tenant", "fast"
	noteUndeclaredPolicies(&out, eng, []resolve.Resource{{Kind: resolve.KindStack, Name: "immich"}, vol("kept"), vol("dropped"), moved, vol("mystery")})
	if strings.Contains(out.String(), "immich") {
		t.Errorf("once they are declared again there is nothing to say about them:\n%s", out.String())
	}
	out.Reset()
	noteUndeclaredPolicies(&out, listEngine{}, []resolve.Resource{vol("kept")})
	if out.Len() != 0 {
		t.Errorf("nothing undeclared, nothing printed: %q", out.String())
	}
}
