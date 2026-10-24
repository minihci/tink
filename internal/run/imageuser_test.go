package run

import (
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

// instFake is a server whose instance "app" has the config in cfg, and which counts how often it was asked for it.
type instFake struct {
	readFake
	cfg   map[string]string
	err   error
	gone  bool
	reads int
}

func (f *instFake) GetInstance(name string) (*api.Instance, string, error) {
	f.reads++
	switch {
	case f.err != nil:
		return nil, "", f.err
	case f.gone:
		return nil, "", errNotFound
	}
	inst := &api.Instance{Name: name}
	inst.Config = f.cfg
	return inst, "", nil
}

func (f *instFake) UseProject(string) incus.InstanceServer { return f }

func specWithVolume() *Spec {
	s := diskOnPool()
	s.Name = "app"
	return s
}

func TestInheritImageUser(t *testing.T) {
	cases := []struct {
		name      string
		cfg       map[string]string
		want      map[string]string
		fromImage bool
	}{
		{"a non-root image: the volume takes its uid and gid", map[string]string{"oci.uid": "472", "oci.gid": "0"}, map[string]string{"initial.uid": "472", "initial.gid": "0"}, true},
		{"a uid and no gid: only the uid", map[string]string{"oci.uid": "1000"}, map[string]string{"initial.uid": "1000"}, true},
		{"root: nothing changes", map[string]string{"oci.uid": "0", "oci.gid": "0"}, nil, false},
		{"no user on the instance (a system container): nothing changes", map[string]string{}, nil, false},
		{"a user that is not a number: nothing is guessed", map[string]string{"oci.uid": "node", "oci.gid": "node"}, nil, false},
		{"a gid that is not a number: the uid alone", map[string]string{"oci.uid": "1000", "oci.gid": "node"}, map[string]string{"initial.uid": "1000"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &instFake{cfg: tc.cfg}
			spec := specWithVolume()
			if w := inheritImageUser(f, spec); w != "" {
				t.Fatalf("warning = %q", w)
			}
			if !reflect.DeepEqual(spec.VolumeConfig, tc.want) {
				t.Errorf("VolumeConfig = %v, want %v", spec.VolumeConfig, tc.want)
			}
			if spec.VolumeOwnerFromImage != tc.fromImage {
				t.Errorf("VolumeOwnerFromImage = %v, want %v", spec.VolumeOwnerFromImage, tc.fromImage)
			}
		})
	}
}

func TestInheritImageUser_LeavesWhatItShouldAlone(t *testing.T) {
	t.Run("--user decides: the instance is not even read", func(t *testing.T) {
		f := &instFake{cfg: map[string]string{"oci.uid": "472"}}
		spec := specWithVolume()
		spec.VolumeConfig = map[string]string{"initial.uid": "999"}
		inheritImageUser(f, spec)
		if spec.VolumeConfig["initial.uid"] != "999" || spec.VolumeOwnerFromImage || f.reads != 0 {
			t.Errorf("config %v, from image %v, reads %d", spec.VolumeConfig, spec.VolumeOwnerFromImage, f.reads)
		}
	})
	t.Run("no managed volume: the instance is not read", func(t *testing.T) {
		f := &instFake{cfg: map[string]string{"oci.uid": "472"}}
		spec := &Spec{Name: "app", Devices: map[string]map[string]string{"host": {"type": "disk", "source": "/srv/data", "path": "/data"}}}
		inheritImageUser(f, spec)
		if len(spec.VolumeConfig) != 0 || f.reads != 0 {
			t.Errorf("config %v, reads %d", spec.VolumeConfig, f.reads)
		}
	})
	t.Run("an instance that cannot be read: a warning, and the volumes stay root's", func(t *testing.T) {
		f := &instFake{err: api.StatusErrorf(500, "boom")}
		spec := specWithVolume()
		w := inheritImageUser(f, spec)
		if !strings.Contains(w, "boom") || !strings.Contains(w, "--user") || len(spec.VolumeConfig) != 0 {
			t.Errorf("warning %q, config %v", w, spec.VolumeConfig)
		}
	})
	t.Run("an instance that is not there: nothing", func(t *testing.T) {
		f := &instFake{gone: true}
		spec := specWithVolume()
		if w := inheritImageUser(f, spec); w != "" || len(spec.VolumeConfig) != 0 {
			t.Errorf("warning %q, config %v", w, spec.VolumeConfig)
		}
	})
}

// The owner reaches the volume that is created, and the lines about the volume say where it came from.
func TestInheritedOwner_ReachesTheVolumeAndTheLines(t *testing.T) {
	f := &instFake{readFake: readFake{volErr: errNotFound}, cfg: map[string]string{"oci.uid": "472", "oci.gid": "0"}}
	spec := specWithVolume()
	inheritImageUser(f, spec)

	if got := describeVolumes(f, spec); !reflect.DeepEqual(got, []string{"creating volume default/vol1, owned by 472:0 (the image's user)"}) {
		t.Errorf("lines = %q", got)
	}
	if err := ensureManagedVolumes(f, spec); err != nil {
		t.Fatal(err)
	}
	if want := []map[string]string{{"initial.uid": "472", "initial.gid": "0"}}; !reflect.DeepEqual(f.createdConfig, want) {
		t.Errorf("the volume was created with %v, want %v", f.createdConfig, want)
	}

	t.Run("a volume that exists keeps its owner, and the line says what to chown it to", func(t *testing.T) {
		g := &instFake{cfg: map[string]string{"oci.uid": "472", "oci.gid": "0"}}
		s := specWithVolume()
		inheritImageUser(g, s)
		got := describeVolumes(g, s)
		if len(got) != 1 || !strings.Contains(got[0], "its ownership is not changed") || !strings.Contains(got[0], "chown it to 472:0 (the image's user)") {
			t.Errorf("lines = %q", got)
		}
		if err := ensureManagedVolumes(g, s); err != nil || len(g.created) != 0 {
			t.Errorf("err %v, created %v", err, g.created)
		}
	})

	t.Run("--user's owner has no such suffix", func(t *testing.T) {
		g := &instFake{readFake: readFake{volErr: errNotFound}}
		s := specWithVolume()
		if err := applyUser(&Spec{Config: map[string]string{}}, "472:0"); err != nil {
			t.Fatal(err)
		}
		s.VolumeConfig = map[string]string{"initial.uid": "472", "initial.gid": "0"}
		if got := describeVolumes(g, s); !reflect.DeepEqual(got, []string{"creating volume default/vol1, owned by 472:0"}) {
			t.Errorf("lines = %q", got)
		}
	})
}
