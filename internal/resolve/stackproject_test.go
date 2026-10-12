package resolve

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectOf(rs []Resource, name string) string {
	for _, r := range rs {
		if r.Name == name {
			return r.Project
		}
	}
	return "<missing>"
}

func TestStackProject_DefaultGoesToResourcesThatLiveInAProject(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "home", Project: "smarthome"},
		{Kind: KindProject, Name: "smarthome"},
		{Kind: KindProfile, Name: "p"},
		{Kind: KindStorageVolume, Name: "v"},
		{Kind: KindInstance, Name: "i"},
		{Kind: KindImage, Name: "img", Alias: "a", Source: "s"},
		{Kind: KindBackupTarget, Name: "t"},
		{Kind: KindIncus, Name: "x", Check: []string{"a"}, Command: []string{"b"}},
	}
	applyStackProject(rs)
	for _, name := range []string{"p", "v", "i", "img"} {
		if got := projectOf(rs, name); got != "smarthome" {
			t.Errorf("%s: project = %q, want the stack's default", name, got)
		}
	}
	// a project is not inside a project; the stack is metadata; a target is not in a project; kind: incus is the local CLI
	for _, name := range []string{"smarthome", "home", "t", "x"} {
		if got := projectOf(rs, name); name == "home" {
			if got != "smarthome" {
				t.Errorf("the stack declaration must keep what it declared, got %q", got)
			}
		} else if got != "" {
			t.Errorf("%s must not receive a default project, got %q", name, got)
		}
	}
}

func TestStackProject_AResourcesOwnProjectWins(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "home", Project: "smarthome"},
		{Kind: KindInstance, Name: "elsewhere", Project: "other"},
		{Kind: KindInstance, Name: "plain"},
	}
	applyStackProject(rs)
	if got := projectOf(rs, "elsewhere"); got != "other" {
		t.Errorf("an explicit project must win, got %q", got)
	}
	if got := projectOf(rs, "plain"); got != "smarthome" {
		t.Errorf("plain = %q", got)
	}
}

func TestStackProject_FileAndExecFollowTheirInstance(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "home", Project: "smarthome"},
		{Kind: KindInstance, Name: "in-default-of-stack"},
		{Kind: KindInstance, Name: "in-other", Project: "other"},
		{Kind: KindFile, Name: "f1", Instance: "in-default-of-stack", Path: "/x"},
		{Kind: KindFile, Name: "f2", Instance: "in-other", Path: "/x"},
		{Kind: KindExec, Name: "e1", Instance: "in-other", Command: []string{"true"}, Triggers: []string{"1"}},
		{Kind: KindExec, Name: "e2", Instance: "not-in-this-stack", Command: []string{"true"}, Triggers: []string{"1"}},
		{Kind: KindFile, Name: "f3", Instance: "in-other", Path: "/y", Project: "explicit"},
	}
	applyStackProject(rs)
	want := map[string]string{"f1": "smarthome", "f2": "other", "e1": "other", "e2": "smarthome", "f3": "explicit"}
	for name, w := range want {
		if got := projectOf(rs, name); got != w {
			t.Errorf("%s: project = %q, want %q", name, got, w)
		}
	}
}

func TestStackProject_NoDefaultWhenTheStackNamesNone(t *testing.T) {
	rs := []Resource{{Kind: KindStack, Name: "home"}, {Kind: KindInstance, Name: "i"}}
	applyStackProject(rs)
	if got := projectOf(rs, "i"); got != "" {
		t.Errorf("a stack without a project must change nothing, got %q", got)
	}
}

func TestStackProject_TwoStacksGiveNoDefault(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "a", Project: "pa"},
		{Kind: KindStack, Name: "b", Project: "pb"},
		{Kind: KindInstance, Name: "i"},
	}
	applyStackProject(rs)
	if got := projectOf(rs, "i"); got != "" {
		t.Errorf("with two stacks there is no single answer, got %q", got)
	}
	if _, err := Levels(rs); err == nil {
		t.Error("Levels must still refuse a stack that names itself twice")
	}
}

func TestStackProject_IsAppliedTwiceHarmlessly(t *testing.T) {
	rs := []Resource{
		{Kind: KindStack, Name: "home", Project: "smarthome"},
		{Kind: KindInstance, Name: "i"},
		{Kind: KindFile, Name: "f", Instance: "i", Path: "/x"},
	}
	applyStackProject(rs)
	applyStackProject(rs)
	if projectOf(rs, "i") != "smarthome" || projectOf(rs, "f") != "smarthome" {
		t.Errorf("got i=%q f=%q", projectOf(rs, "i"), projectOf(rs, "f"))
	}
}

func TestStackProject_LoadFilesAppliesItAndOrdersAfterTheProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tink.yaml")
	yaml := "kind: stack\nname: home\nproject: smarthome\n---\n" +
		"kind: project\nname: smarthome\n---\n" +
		"kind: storage-volume\nname: data\nbackup:\n  none: test\n---\n" +
		"kind: instance\nname: app\nimage: docker-oci:library/alpine:3\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if projectOf(rs, "data") != "smarthome" || projectOf(rs, "app") != "smarthome" {
		t.Fatalf("loaded resources should carry the stack's project: data=%q app=%q", projectOf(rs, "data"), projectOf(rs, "app"))
	}
	levels, err := Levels(rs)
	if err != nil {
		t.Fatal(err)
	}
	if !inLevel(levels, 0, "smarthome") || inLevel(levels, 0, "app") || inLevel(levels, 0, "data") {
		t.Errorf("the project must come first now that things name it: %v", levels)
	}
}

func TestMixedProjectNotes(t *testing.T) {
	t.Run("nothing names a project: a plain stack, no note", func(t *testing.T) {
		if n := mixedProjectNotes([]Resource{{Kind: KindInstance, Name: "a"}, {Kind: KindStorageVolume, Name: "b"}}); n != nil {
			t.Errorf("got %v", n)
		}
	})
	t.Run("everything names one: no note", func(t *testing.T) {
		if n := mixedProjectNotes([]Resource{{Kind: KindInstance, Name: "a", Project: "p"}, {Kind: KindStorageVolume, Name: "b", Project: "p"}}); n != nil {
			t.Errorf("got %v", n)
		}
	})
	t.Run("one forgot: only that one is told, naming what is in the project", func(t *testing.T) {
		n := mixedProjectNotes([]Resource{
			{Kind: KindInstance, Name: "app", Project: "smarthome"},
			{Kind: KindStorageVolume, Name: "data", Project: "smarthome"},
			{Kind: KindStorageVolume, Name: "forgot"},
		})
		if len(n) != 1 || !strings.Contains(n["forgot"], `"smarthome"`) || !strings.Contains(n["forgot"], "instance/app") || !strings.Contains(n["forgot"], "kind: stack") {
			t.Errorf("got %v", n)
		}
	})
	t.Run("long lists are abbreviated", func(t *testing.T) {
		rs := []Resource{{Kind: KindInstance, Name: "forgot"}}
		for _, name := range []string{"a", "b", "c", "d", "e"} {
			rs = append(rs, Resource{Kind: KindInstance, Name: name, Project: "p"})
		}
		if note := mixedProjectNotes(rs)["forgot"]; !strings.Contains(note, "2 more") {
			t.Errorf("got %q", note)
		}
	})
	t.Run("files, execs, projects and targets are not judged", func(t *testing.T) {
		n := mixedProjectNotes([]Resource{
			{Kind: KindInstance, Name: "app", Project: "p"},
			{Kind: KindProject, Name: "p"},
			{Kind: KindBackupTarget, Name: "t"},
			{Kind: KindFile, Name: "f", Instance: "app", Path: "/x"},
		})
		if n != nil {
			t.Errorf("got %v", n)
		}
	})
	t.Run("a stack default leaves nothing to warn about", func(t *testing.T) {
		rs := []Resource{{Kind: KindStack, Name: "s", Project: "p"}, {Kind: KindInstance, Name: "a"}, {Kind: KindInstance, Name: "b", Project: "q"}}
		applyStackProject(rs)
		if n := mixedProjectNotes(rs); n != nil {
			t.Errorf("got %v", n)
		}
	})
}

func TestForResourcesCarriesTheProjectNoteIntoThePlan(t *testing.T) {
	rs := []Resource{
		{Kind: KindInstance, Name: "app", Project: "smarthome"},
		{Kind: KindStorageVolume, Name: "forgot"}, // lives in a project but names none
	}
	opts := PlanOptions{}.ForResources(rs)
	if opts.projectNotes["forgot"] == "" {
		t.Fatalf("ForResources should compute the note, got %v", opts.projectNotes)
	}
	// planOne appends it whatever the kind decides (a backup target needs no server)
	target := Resource{Kind: KindBackupTarget, Name: "forgot", Location: "same-host", Engine: "incus", Pool: "p"}
	p, err := planOne(nil, target, PlanOptions{projectNotes: map[string]string{"forgot": "the note"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Warnings) != 1 || p.Warnings[0] != "the note" {
		t.Errorf("warnings = %v", p.Warnings)
	}
}
