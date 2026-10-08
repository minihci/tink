package resolve

import (
	"errors"
	"strings"
	"testing"
)

// withoutSkopeo makes skopeo look missing for the length of the test.
func withoutSkopeo(t *testing.T) {
	t.Helper()
	old := skopeoAvailable
	skopeoAvailable = func() bool { return false }
	t.Cleanup(func() { skopeoAvailable = old })
}

func TestAMissingSkopeoIsFlaggedSoItCanBeSaidOnce(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"skopeo is missing":       {errNoSkopeo, true},
		"the registry said no":    {errors.New("manifest unknown"), false},
		"offline, on purpose":     {errOffline, false},
		"skopeo missing, wrapped": {errors.Join(errors.New("context"), errNoSkopeo), true},
	} {
		p := &probe{err: tc.err}
		got := checkImage(mqCfg, "docker-oci:library/eclipse-mosquitto:2", p.imageProbe())
		if got.NoSkopeo != tc.want {
			t.Errorf("%s: NoSkopeo = %v, want %v", name, got.NoSkopeo, tc.want)
		}
		if len(got.Unverified) != 1 {
			t.Errorf("%s: the image is still unverified, whatever the cause: %v", name, got.Unverified)
		}
	}
	got := checkImage(mqCfg, "docker-oci:library/eclipse-mosquitto:2", (&probe{err: errNoSkopeo}).imageProbe())
	if !strings.Contains(got.Unverified[0], "skopeo was not found") || !strings.Contains(got.Unverified[0], "docker.io/library/eclipse-mosquitto:2") {
		t.Errorf("the finding must name the image and the cause, because a rebuild is blocked with it: %v", got.Unverified)
	}
}

// A warning that reads the same on every instance is said once at the end; an instance set to rebuild is still blocked, and told why.
func TestAMissingSkopeoIsNotAWarningOnEveryInstanceButStillBlocksARebuild(t *testing.T) {
	missing := imageCheck{Unverified: []string{"image: could not verify \"docker.io/library/x:1\" against the registry: " + errNoSkopeo.Error()}, NoSkopeo: true}
	for _, policy := range []string{"", OnImageChangeReport, OnImageChangeIgnore} {
		p := decideInstance(instRes(policy), nil, missing, preflight{})
		if p.Action == ActionBlocked || len(p.Warnings) != 0 {
			t.Errorf("policy %q: action %d, warnings %v: want no block and no per-instance warning", policy, p.Action, p.Warnings)
		}
		// the control: the same finding from any other cause is still a warning on the instance
		other := missing
		other.NoSkopeo = false
		if q := decideInstance(instRes(policy), nil, other, preflight{}); len(q.Warnings) != 1 {
			t.Errorf("policy %q: a lookup that failed for another reason must still warn: %v", policy, q.Warnings)
		}
	}
	p := decideInstance(instRes(OnImageChangeRebuild), nil, missing, preflight{})
	if p.Action != ActionBlocked || !strings.Contains(strings.Join(p.Blocked, " | "), "skopeo was not found") {
		t.Errorf("rebuild must not act blind, and must say why: action %d, blocked %v", p.Action, p.Blocked)
	}
}

func TestARunSaysOnceThatImagesWereNotCheckedWithoutSkopeo(t *testing.T) {
	withoutSkopeo(t)

	env := newImageEnv(false)
	for _, ref := range []string{"library/a:1", "library/b:2", "library/a:1"} {
		if _, err := env.registryImage("docker-oci", ref); !errors.Is(err, errNoSkopeo) {
			t.Fatalf("%s: err = %v, want errNoSkopeo, and without running anything", ref, err)
		}
	}
	note := PlanOptions{env: env}.ImageCheckNote()
	for _, want := range []string{"2 images were not checked", "skopeo was not found", "--offline"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must say %q: %s", want, note)
		}
	}

	one := newImageEnv(false)
	if _, err := one.registryImage("docker-oci", "library/a:1"); !errors.Is(err, errNoSkopeo) {
		t.Fatal(err)
	}
	if note := (PlanOptions{env: one}).ImageCheckNote(); !strings.HasPrefix(note, "1 image was not checked") {
		t.Errorf("one image reads as one image: %s", note)
	}

	// the runtime-config read (a rebuild's) is a lookup too
	rt := newImageEnv(false)
	if _, err := rt.runtimeConfig(testRemotes["docker-oci"], "library/c:3"); !errors.Is(err, errNoSkopeo) {
		t.Errorf("runtimeConfig without skopeo: %v", err)
	}
	if rt.skippedCount() != 1 {
		t.Errorf("a runtime-config lookup that was not made is counted: %d", rt.skippedCount())
	}
}

func TestNothingIsSaidWhenNothingWasSkippedForWantOfSkopeo(t *testing.T) {
	if note := (PlanOptions{}).ImageCheckNote(); note != "" {
		t.Errorf("no environment: %q", note)
	}
	if note := (PlanOptions{env: newImageEnv(false)}).ImageCheckNote(); note != "" {
		t.Errorf("nothing looked up: %q", note)
	}

	// --offline is a choice, not a missing tool: it is not this note's business
	withoutSkopeo(t)
	off := newImageEnv(true)
	if _, err := off.registryImage("docker-oci", "library/a:1"); !errors.Is(err, errOffline) {
		t.Fatalf("offline wins: %v", err)
	}
	if note := (PlanOptions{env: off}).ImageCheckNote(); note != "" {
		t.Errorf("offline: %q", note)
	}

	// skopeo present: a lookup that then fails for another reason is not a missing skopeo
	old := skopeoAvailable
	skopeoAvailable = func() bool { return true }
	t.Cleanup(func() { skopeoAvailable = old })
	env := newImageEnv(false)
	env.conf = nil
	if _, err := env.registryImage("docker-oci", "library/a:1"); err == nil || errors.Is(err, errNoSkopeo) {
		t.Errorf("err = %v: with skopeo present the failure is something else", err)
	}
	if note := (PlanOptions{env: env}).ImageCheckNote(); note != "" {
		t.Errorf("skopeo present: %q", note)
	}
}
