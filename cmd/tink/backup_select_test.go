package main

import (
	"reflect"
	"testing"

	"github.com/minihci/tink/internal/resolve"
)

func vol(name string, copies int) resolve.Resource {
	r := resolve.Resource{Kind: resolve.KindStorageVolume, Name: name, Backup: &resolve.VolumeBackup{}}
	for i := 0; i < copies; i++ {
		r.Backup.Copies = append(r.Backup.Copies, resolve.BackupCopy{Target: "t"})
	}
	return r
}

func names(rs []resolve.Resource) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func TestSelectCopyVolumes(t *testing.T) {
	stack := []resolve.Resource{
		vol("a", 1),
		{Kind: resolve.KindBackupTarget, Name: "t"},
		vol("b", 1),
		vol("no-copies", 0),
		{Kind: resolve.KindStorageVolume, Name: "no-backup"}, // no backup block at all
		vol("c", 2),
	}
	for name, tc := range map[string]struct {
		args        []string
		want        []string
		wantUnknown []string
	}{
		"no names means every volume with copies": {nil, []string{"a", "b", "c"}, nil},
		// The regression: once the named volume had been handled the filter switched itself off, so a
		// volume declared AFTER it was backed up as well, though it had not been asked for.
		"a named volume is the only one run, even with volumes after it": {[]string{"a"}, []string{"a"}, nil},
		"the middle one":                          {[]string{"b"}, []string{"b"}, nil},
		"several, in stack order":                 {[]string{"c", "a"}, []string{"a", "c"}, nil},
		"a volume without copies is unknown":      {[]string{"no-copies"}, nil, []string{"no-copies"}},
		"a volume without a backup block":         {[]string{"no-backup"}, nil, []string{"no-backup"}},
		"a target is not a volume":                {[]string{"t"}, nil, []string{"t"}},
		"a name that does not exist":              {[]string{"nope", "a"}, []string{"a"}, []string{"nope"}},
		"a name given twice is reported once":     {[]string{"nope", "nope"}, nil, []string{"nope"}},
		"a name given twice runs the volume once": {[]string{"a", "a"}, []string{"a"}, nil},
	} {
		got, unknown := selectCopyVolumes(stack, tc.args)
		if !reflect.DeepEqual(names(got), tc.want) {
			t.Errorf("%s: selected %v, want %v", name, names(got), tc.want)
		}
		if !reflect.DeepEqual(unknown, tc.wantUnknown) {
			t.Errorf("%s: unknown %v, want %v", name, unknown, tc.wantUnknown)
		}
	}
}
