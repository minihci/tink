package volbackup

import (
	"errors"
	"sort"
	"testing"
)

func TestListVolumesFindsEveryCustomVolumeInEveryProjectAndPool(t *testing.T) {
	f := newFake("tron", "default", "nas")
	f.add("default", "lib", map[string]string{"user.x": "1"})
	f.add("default", "lib2", nil)
	f.add("nas", "docs", nil)
	f.vols["default/lib"].Project = "tenant-a"
	f.vols["default/lib2"].Project = "tenant-b" // the same name could be in both
	f.vols["nas/docs"].Project = "default"
	f.add("default", "an-image", nil)
	f.vols["default/an-image"].Type = "image" // not a custom volume

	got, perr, err := ListVolumes(f)
	if err != nil || len(perr) != 0 {
		t.Fatal(err, perr)
	}
	var names []string
	for _, v := range got {
		names = append(names, v.Volume.Project+"/"+v.Volume.Pool+"/"+v.Volume.Name)
	}
	sort.Strings(names)
	want := []string{"default/nas/docs", "tenant-a/default/lib", "tenant-b/default/lib2"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Errorf("listed %v, want %v", names, want)
	}
	for _, v := range got {
		if v.Volume.Name == "lib" && v.Config["user.x"] != "1" {
			t.Errorf("a listed volume carries its config: %v", v.Config)
		}
		if v.Config == nil {
			t.Errorf("config is never nil: %+v", v)
		}
	}
}

func TestAPoolThatCannotBeListedDoesNotHideTheOthers(t *testing.T) {
	f := newFake("tron", "default", "nas")
	f.add("default", "lib", nil)
	f.add("nas", "docs", nil)
	f.failPool = map[string]error{"nas": errors.New("the NAS is unreachable")}

	got, perr, err := ListVolumes(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Volume.Name != "lib" {
		t.Errorf("the pool that works is still listed: %+v", got)
	}
	if perr["nas"] == nil || len(perr) != 1 {
		t.Errorf("the broken pool is reported by name: %v", perr)
	}
}
