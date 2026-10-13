package resolve

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func containerNamed(name, project string) Resource {
	return Resource{Kind: KindInstance, Name: name, Project: project}
}

func fileFor(name, instance, project string, dependsOn ...string) Resource {
	return Resource{Kind: KindFile, Name: name, Instance: instance, Project: project, Path: "/etc/" + name, Content: name, DependsOn: dependsOn}
}

func TestFirstStartFiles_AContainersFilesAreChosenInDeclarationOrder(t *testing.T) {
	rs := []Resource{
		fileFor("config", "hs", "p", "hs", "policy"), // waits for the instance and for another chosen file
		containerNamed("hs", "p"),
		fileFor("policy", "hs", "p", "hs"),
		fileFor("plain", "hs", "p"), // no depends_on at all
	}
	got := firstStartFiles(rs)[instanceKey("p", "hs")]
	if want := []string{"config", "policy", "plain"}; !reflect.DeepEqual(names(got), want) {
		t.Errorf("chosen = %v, want %v (declaration order)", names(got), want)
	}
}

func TestFirstStartFiles_AVirtualMachineKeepsToday(t *testing.T) {
	vm := containerNamed("ha", "p")
	vm.VM = true
	if got := firstStartFiles([]Resource{vm, fileFor("f", "ha", "p")}); len(got) != 0 {
		t.Errorf("a virtual machine needs its agent running to take a file, got %v", got)
	}
}

func TestFirstStartFiles_AFileThatWaitsForAnythingElseIsLeftToItsOwnTurn(t *testing.T) {
	rs := []Resource{
		containerNamed("hs", "p"),
		{Kind: KindExec, Name: "prepare", Instance: "hs", Project: "p"},
		fileFor("after-exec", "hs", "p", "hs", "prepare"), // waits for an exec: not chosen
		fileFor("after-that", "hs", "p", "after-exec"),    // waits for a file that is not chosen: not chosen (the fixed point)
		fileFor("after-volume", "hs", "p", "some-volume"), // waits for something that is not the instance or a chosen file
		fileFor("fine", "hs", "p", "hs"),
	}
	if got := names(firstStartFiles(rs)[instanceKey("p", "hs")]); !reflect.DeepEqual(got, []string{"fine"}) {
		t.Errorf("chosen = %v, want only [fine]", got)
	}
}

func TestFirstStartFiles_NothingToPlaceMeansNoEntry(t *testing.T) {
	rs := []Resource{containerNamed("hs", "p"), fileFor("f", "hs", "p", "elsewhere")}
	if got := firstStartFiles(rs); len(got) != 0 {
		t.Errorf("an instance with no eligible file should have no entry, got %v", got)
	}
}

func TestFirstStartFiles_AFileForAnInstanceNotInTheStackIsIgnored(t *testing.T) {
	if got := firstStartFiles([]Resource{fileFor("f", "elsewhere", "p")}); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestFirstStartFiles_TwoProjectsEachWithAnInstanceOfTheSameNameDoNotMix(t *testing.T) {
	rs := []Resource{containerNamed("hs", "a"), containerNamed("hs", "b"), fileFor("fa", "hs", "a"), fileFor("fb", "hs", "b")}
	got := firstStartFiles(rs)
	if !reflect.DeepEqual(names(got[instanceKey("a", "hs")]), []string{"fa"}) || !reflect.DeepEqual(names(got[instanceKey("b", "hs")]), []string{"fb"}) {
		t.Errorf("got %v", got)
	}
}

func TestWithTargets_FillsInTheFirstStartFiles(t *testing.T) {
	rs := []Resource{containerNamed("hs", ""), fileFor("f", "hs", "")}
	o := PlanOptions{}.withTargets(rs)
	if len(o.firstStart[instanceKey("", "hs")]) != 1 {
		t.Errorf("firstStart = %v", o.firstStart)
	}
}

type stepRecorder struct {
	events []string
	fail   map[string]error
}

func (r *stepRecorder) steps() instanceSteps {
	do := func(name string) error {
		r.events = append(r.events, name)
		return r.fail[name]
	}
	return instanceSteps{
		create: func() error { return do("create") },
		config: func() error { return do("config") },
		place:  func(f Resource) error { return do("place " + f.Name) },
		start:  func() error { return do("start") },
	}
}

func TestCreateInOrder_CreatesConfiguresPlacesThenStarts(t *testing.T) {
	r := &stepRecorder{}
	placed, err := createInOrder(r.steps(), []Resource{fileFor("a", "hs", ""), fileFor("b", "hs", "")})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.events, ", "); got != "create, config, place a, place b, start" {
		t.Errorf("order = %s", got)
	}
	if !reflect.DeepEqual(placed, []string{"a", "b"}) {
		t.Errorf("placed = %v", placed)
	}
}

func TestCreateInOrder_NoFilesIsTheOldSequence(t *testing.T) {
	r := &stepRecorder{}
	if _, err := createInOrder(r.steps(), nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(r.events, ", "); got != "create, config, start" {
		t.Errorf("order = %s", got)
	}
}

// Placing is an ordering improvement, never a new way to fail.
func TestCreateInOrder_AFailedPlacementIsSkippedAndTheInstanceStillStarts(t *testing.T) {
	r := &stepRecorder{fail: map[string]error{"place a": errors.New("no such directory")}}
	placed, err := createInOrder(r.steps(), []Resource{fileFor("a", "hs", ""), fileFor("b", "hs", "")})
	if err != nil {
		t.Fatalf("a failed placement must not fail the creation: %v", err)
	}
	if got := strings.Join(r.events, ", "); got != "create, config, place a, place b, start" {
		t.Errorf("order = %s", got)
	}
	if !reflect.DeepEqual(placed, []string{"b"}) {
		t.Errorf("placed = %v, want only the one that was placed", placed)
	}
}

func TestCreateInOrder_AFailedCreateOrConfigStopsEverything(t *testing.T) {
	for _, step := range []string{"create", "config"} {
		r := &stepRecorder{fail: map[string]error{step: errors.New(step + " failed")}}
		placed, err := createInOrder(r.steps(), []Resource{fileFor("a", "hs", "")})
		if err == nil || !strings.Contains(err.Error(), step+" failed") {
			t.Errorf("%s: err = %v", step, err)
		}
		if len(placed) != 0 {
			t.Errorf("%s: nothing may be placed after a failure, got %v", step, placed)
		}
		for _, e := range r.events {
			if strings.HasPrefix(e, "place") || e == "start" {
				t.Errorf("%s failed but %q still ran: %v", step, e, r.events)
			}
		}
	}
}

func TestCreateInOrder_AFailedStartReturnsTheErrorAndWhatWasPlaced(t *testing.T) {
	r := &stepRecorder{fail: map[string]error{"start": errors.New("could not start")}}
	placed, err := createInOrder(r.steps(), []Resource{fileFor("a", "hs", "")})
	if err == nil || !strings.Contains(err.Error(), "could not start") || !reflect.DeepEqual(placed, []string{"a"}) {
		t.Errorf("placed %v err %v", placed, err)
	}
}

func TestPlacedNote_NamesTheFiles(t *testing.T) {
	got := placedNote("headscale", []string{"headscale-policy", "headscale-config"})
	want := "instance/headscale: placed 2 file(s) before its first start: file/headscale-policy, file/headscale-config"
	if got != want {
		t.Errorf("note = %q, want %q", got, want)
	}
}

// What makes the whole thing quiet: once a file's content is in place, its own turn plans no changes, so it neither pushes again nor restarts.
func TestAPlacedFileThenPlansAsNoChanges(t *testing.T) {
	f := fileFor("headscale-config", "hs", "")
	f.Restart = true
	p, err := planFile(&lookupServer{fileContent: f.Content}, f)
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != ActionNone {
		t.Errorf("action = %v, want none: a file already in place with restart: true must not restart the instance again", p.Action)
	}
}
