package resolve

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	incus "github.com/lxc/incus/v7/client"
)

// A file with seed: true is written when the path is not there and never again. See seedfile.go.

func seedFile(name, p, content string) Resource {
	f := fileAt(name, p)
	f.Content = content
	f.Seed = true
	return f
}

func TestPlanPlacedFile_Seed(t *testing.T) {
	const want = "declared\n"
	f := seedFile("ha-yaml", "/config/configuration.yaml", want)
	notePrefix := "seed: /config/configuration.yaml on ha exists and differs from the declared content"

	cases := []struct {
		name     string
		fake     *fileFake
		setup    func(*fileFake)
		wantAct  Action
		wantNote bool
	}{
		{"running, the file is not there: would create", newFileFake("Running", stateRunning, haVolumeDevices),
			func(*fileFake) {}, ActionCreate, false},
		{"running, the file holds other content: no changes, and a note says it differs", newFileFake("Running", stateRunning, haVolumeDevices),
			func(s *fileFake) { s.instance[instCfg] = "the app's own\n" }, ActionNone, true},
		{"running, the file holds the declared content: no changes, no note", newFileFake("Running", stateRunning, haVolumeDevices),
			func(s *fileFake) { s.instance[instCfg] = want }, ActionNone, false},
		{"running, the file is there and empty: it counts as present", newFileFake("Running", stateRunning, haVolumeDevices),
			func(s *fileFake) { s.instance[instCfg] = "" }, ActionNone, true},
		{"stopped, the volume holds other content: no changes, read from the volume", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func(s *fileFake) { s.volumes[volCfg] = "the app's own\n" }, ActionNone, true},
		{"stopped, the volume has no such file: would create", newFileFake("Stopped", stateStopped, haVolumeDevices),
			func(*fileFake) {}, ActionCreate, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(tc.fake)
			p, err := planPlacedFile(tc.fake, f)
			if err != nil {
				t.Fatal(err)
			}
			if p.Action != tc.wantAct {
				t.Errorf("action = %v, want %v", p.Action, tc.wantAct)
			}
			hasNote := false
			for _, n := range p.Notes {
				if strings.HasPrefix(n, notePrefix) {
					hasNote = true
				}
			}
			if hasNote != tc.wantNote {
				t.Errorf("notes = %q, want the seed note: %v", p.Notes, tc.wantNote)
			}
			if len(p.Changes) != 0 {
				t.Errorf("changes = %v, want none: a seed file is never a change once it exists", p.Changes)
			}
		})
	}
}

func TestPlanFile_ADirectoryInTheWayIsAnErrorNotAPanic(t *testing.T) {
	s := &dirServer{mountFake: newMountFake()}
	for _, seed := range []bool{false, true} {
		f := fileAt("f", "/config/x")
		f.Seed = seed
		if _, err := planFile(s, f); err == nil || !strings.Contains(err.Error(), "/config/x on ha is a directory") {
			t.Errorf("seed=%v: err = %v", seed, err)
		}
	}
}

// dirServer answers every instance file read as a directory, the shape the client gives: no reader, and a response of Type directory.
type dirServer struct{ *mountFake }

func (s *dirServer) UseProject(string) incus.InstanceServer { return s }
func (s *dirServer) GetInstanceFile(string, string) (io.ReadCloser, *incus.InstanceFileResponse, error) {
	return nil, &incus.InstanceFileResponse{Type: "directory"}, nil
}

func TestPushFile_SeedNeverWritesOverAFileThatIsThere(t *testing.T) {
	t.Run("running, the file exists: nothing is written and no restart is asked", func(t *testing.T) {
		s := newFileFake("Running", stateRunning, haVolumeDevices)
		s.instance[instCfg] = "the app's own\n"
		f := seedFile("ha-yaml", "/config/configuration.yaml", "declared\n")
		f.Restart = true
		if err := placeFileAt(s, f); err != nil {
			t.Fatal(err)
		}
		if got := s.instance[instCfg]; got != "the app's own\n" {
			t.Errorf("file now holds %q", got)
		}
		for _, l := range s.log {
			if strings.HasPrefix(l, "push ") || strings.HasPrefix(l, "restart") {
				t.Errorf("calls = %v", s.log)
			}
		}
	})

	t.Run("running, the file is not there: it is written", func(t *testing.T) {
		s := newFileFake("Running", stateRunning, haVolumeDevices)
		f := seedFile("ha-yaml", "/config/configuration.yaml", "declared\n")
		if err := placeFileAt(s, f); err != nil {
			t.Fatal(err)
		}
		if got := s.instance[instCfg]; got != "declared\n" {
			t.Errorf("file holds %q", got)
		}
	})

	t.Run("stopped, the volume holds the file: it is read and left alone", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, haVolumeDevices)
		s.volumes[volCfg] = "the app's own\n"
		f := seedFile("ha-yaml", "/config/configuration.yaml", "declared\n")
		if err := placeFileAt(s, f); err != nil {
			t.Fatal(err)
		}
		if want := []string{"read custom " + volCfg}; !reflect.DeepEqual(s.log, want) {
			t.Errorf("calls = %v, want %v", s.log, want)
		}
		if got := s.volumes[volCfg]; got != "the app's own\n" {
			t.Errorf("volume holds %q", got)
		}
	})

	t.Run("the first start of an instance whose image ships the file: the image's file is kept", func(t *testing.T) {
		s := newFileFake("Stopped", stateStopped, map[string]map[string]string{"root": rootDisk()})
		s.instance["ha:/etc/app.conf"] = "the image's own\n"
		f := seedFile("app", "/etc/app.conf", "declared\n")
		if err := placeFile(s, f); err != nil {
			t.Fatal(err)
		}
		if got := s.instance["ha:/etc/app.conf"]; got != "the image's own\n" {
			t.Errorf("file now holds %q", got)
		}
	})
}

// The apply of a seed file that exists is a no-op that says why; of one that does not, a create.
func TestApplyOne_SeedFile(t *testing.T) {
	t.Run("exists with other content: no changes, a note, and the instance is not restarted", func(t *testing.T) {
		s := newFileFake("Running", stateRunning, haVolumeDevices)
		s.instance[instCfg] = "the app's own\n"
		f := seedFile("ha-yaml", "/config/configuration.yaml", "declared\n")
		f.Restart = true
		var lines []string
		out, err := applyOne(s, f, PlanOptions{}, func(format string, a ...any) { lines = append(lines, fmt.Sprintf(format, a...)) })
		if err != nil {
			t.Fatal(err)
		}
		if out != outConverged {
			t.Errorf("outcome = %v, want converged", out)
		}
		if len(lines) < 2 || lines[0] != "file/ha-yaml: no changes" || !strings.Contains(lines[1], "seed: ") {
			t.Errorf("lines = %q", lines)
		}
		for _, l := range s.log {
			if strings.HasPrefix(l, "restart") || strings.HasPrefix(l, "push ") {
				t.Errorf("calls = %v", s.log)
			}
		}
	})
}

func TestSeed_LoaderRules(t *testing.T) {
	t.Run("seed: true is read from a document", func(t *testing.T) {
		rs, err := LoadFile(writeTemp(t, "kind: file\nname: a\ninstance: i\npath: /p\nseed: true\ncontent: x\n"))
		if err != nil || len(rs) != 1 || !rs[0].Seed {
			t.Fatalf("rs = %+v, err = %v", rs, err)
		}
	})
	t.Run("a file without seed is not a seed file", func(t *testing.T) {
		rs, err := LoadFile(writeTemp(t, "kind: file\nname: a\ninstance: i\npath: /p\ncontent: x\n"))
		if err != nil || rs[0].Seed {
			t.Fatalf("rs = %+v, err = %v", rs, err)
		}
	})
	t.Run("seed with derive is refused", func(t *testing.T) {
		_, err := LoadFile(writeTemp(t, "kind: file\nname: a\ninstance: i\npath: /p\nseed: true\nderive: caddy\ncontent: |\n  # BEGIN tink derive\n  # END tink derive\n"))
		if err == nil || !strings.Contains(err.Error(), "seed and derive cannot be combined") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("seed on another kind is refused", func(t *testing.T) {
		if err := Validate(Resource{Kind: KindProject, Name: "p", Seed: true}); err == nil {
			t.Fatal("no error")
		}
	})
	t.Run("a check_output.equals that names a seed file is refused", func(t *testing.T) {
		file := Resource{Kind: KindFile, Name: "policy", Instance: "hs", Content: "{}", Seed: true}
		exec := Resource{Kind: KindExec, Name: "set", Instance: "hs", CheckOutput: &OutputCheck{Command: []string{"get"}, Equals: "policy"}}
		err := validateCheckOutputs([]Resource{file, exec})
		if err == nil || !strings.Contains(err.Error(), "seed: true") {
			t.Fatalf("err = %v", err)
		}
	})
}

// A seed file in a first-start placement still goes through the same selection as any other file.
func TestFirstStartFiles_ASeedFileIsAnOrdinaryCandidate(t *testing.T) {
	rs := []Resource{instanceWith(map[string]map[string]string{"root": rootDisk()}), seedFile("conf", "/etc/app.conf", "x")}
	got := firstStartFiles(rs)[instanceKey("p", "ha")]
	if len(got) != 1 || got[0].Name != "conf" {
		t.Errorf("candidates = %+v", firstStartFiles(rs))
	}
}
