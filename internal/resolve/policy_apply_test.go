package resolve

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func instRes(policy string) Resource {
	return Resource{Kind: KindInstance, Name: "x", OnImageChange: policy}
}

var (
	drift      = imageCheck{Drift: []string{"image: A -> B"}}
	unverified = imageCheck{Unverified: []string{"image: could not verify"}}
)

// The policy table from the design note, including decision C:
// an unverifiable image blocks only under rebuild.
func TestDecideInstance(t *testing.T) {
	changes := []string{"config.limits.memory: 1 -> 2"}
	tests := []struct {
		name        string
		policy      string
		changes     []string
		chk         imageCheck
		pre         preflight
		want        Action
		wantBlocked string
		wantWarn    string
	}{
		{"no drift, no changes", "", nil, imageCheck{}, preflight{}, ActionNone, "", ""},
		{"no drift, config change", "", changes, imageCheck{}, preflight{}, ActionUpdate, "", ""},

		{"report + drift: blocked", "", nil, drift, preflight{}, ActionBlocked, "nothing on this instance is changed", ""},
		{"report + drift + config change: config withheld", "", changes, drift, preflight{}, ActionBlocked, "withheld", ""},
		{"explicit report is the same as the default", OnImageChangeReport, nil, drift, preflight{}, ActionBlocked, "", ""},
		{"report + unverified: warning only, proceeds (C)", "", changes, unverified, preflight{}, ActionUpdate, "", "could not verify"},

		{"ignore + drift: config still applies", OnImageChangeIgnore, changes, drift, preflight{}, ActionUpdate, "", "ignored"},
		{"ignore + drift, nothing else: converged", OnImageChangeIgnore, nil, drift, preflight{}, ActionNone, "", "ignored"},
		{"ignore + unverified: warning only", OnImageChangeIgnore, nil, unverified, preflight{}, ActionNone, "", "could not verify"},

		{"rebuild + drift: would rebuild", OnImageChangeRebuild, changes, drift, preflight{}, ActionRebuild, "", ""},
		{"rebuild + drift + failed preflight: blocked", OnImageChangeRebuild, nil, drift, preflight{Blockers: []string{"has instance snapshots"}}, ActionBlocked, "has instance snapshots", ""},
		{"rebuild + unverified: blocked, never rebuild blind (C)", OnImageChangeRebuild, nil, unverified, preflight{}, ActionBlocked, "will not act on an image it cannot verify", ""},
		{"rebuild + unverified + config change: also withheld", OnImageChangeRebuild, changes, unverified, preflight{}, ActionBlocked, "withheld", ""},
		{"rebuild + no drift: nothing to do", OnImageChangeRebuild, nil, imageCheck{}, preflight{}, ActionNone, "", ""},
		{"rebuild + preflight warnings are surfaced", OnImageChangeRebuild, nil, drift, preflight{Warnings: []string{"could not size the image"}}, ActionRebuild, "", "could not size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := decideInstance(instRes(tc.policy), tc.changes, tc.chk, tc.pre)
			if p.Action != tc.want {
				t.Fatalf("action = %d, want %d (blocked=%v)", p.Action, tc.want, p.Blocked)
			}
			got := strings.Join(p.Blocked, " | ")
			if tc.want != ActionBlocked && got != "" {
				t.Errorf("a non-blocked disposition must carry no blocked reasons, got %q", got)
			}
			if !strings.Contains(got, tc.wantBlocked) {
				t.Errorf("blocked = %q, want to contain %q", got, tc.wantBlocked)
			}
			if got := strings.Join(p.Warnings, " | "); !strings.Contains(got, tc.wantWarn) {
				t.Errorf("warnings = %q, want to contain %q", got, tc.wantWarn)
			}
		})
	}
}

// ---- apply loop: blocked propagation, summary, exit status

func inst(name string) Resource { return Resource{Kind: KindInstance, Name: name} }
func file(name, onInstance string) Resource {
	return Resource{Kind: KindFile, Name: name, Instance: onInstance}
}

type recorder struct {
	mu  sync.Mutex
	ran []string
}

func (r *recorder) did(name string) {
	r.mu.Lock()
	r.ran = append(r.ran, name)
	r.mu.Unlock()
}
func (r *recorder) ranOnly(names ...string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ran) != len(names) {
		return false
	}
	seen := map[string]bool{}
	for _, n := range r.ran {
		seen[n] = true
	}
	for _, n := range names {
		if !seen[n] {
			return false
		}
	}
	return true
}

func TestApplyLevelsBlockedPropagation(t *testing.T) {
	var rec recorder
	// level 0: A is blocked, B converges. level 1: a file inside A, a file inside B,
	// and an instance that merely depends_on A.
	levels := [][]Resource{
		{inst("A"), inst("B")},
		{file("fA", "A"), file("fB", "B"), inst("C")},
	}
	outcomes := map[string]outcome{"A": outBlocked, "B": outConverged, "fB": outChanged, "C": outConverged}
	notes, err := applyLevels(levels, func(r Resource, note func(string, ...any)) (outcome, error) {
		rec.did(r.Name)
		return outcomes[r.Name], nil
	})

	if !rec.ranOnly("A", "B", "fB", "C") {
		t.Errorf("ran %v; the file inside the blocked instance must be skipped, depends_on-only C must run", rec.ran)
	}
	var nce *NotConvergedError
	if !errors.As(err, &nce) || nce.Blocked != 2 {
		t.Fatalf("err = %v, want NotConvergedError with 2 blocked (A and the skipped file)", err)
	}
	joined := strings.Join(notes, "\n")
	if !strings.Contains(joined, "file/fA: SKIPPED: instance/A is not converged") {
		t.Errorf("missing skip note:\n%s", joined)
	}
	if last := notes[len(notes)-1]; last != "summary: 2 converged, 1 changed, 2 blocked, 0 failed" {
		t.Errorf("summary = %q", last)
	}
}

func TestApplyLevelsAllConvergedIsNilError(t *testing.T) {
	notes, err := applyLevels([][]Resource{{inst("A")}}, func(Resource, func(string, ...any)) (outcome, error) { return outChanged, nil })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if notes[len(notes)-1] != "summary: 0 converged, 1 changed, 0 blocked, 0 failed" {
		t.Errorf("summary = %q", notes[len(notes)-1])
	}
}

func TestApplyLevelsFailureStopsLaterLevels(t *testing.T) {
	var rec recorder
	levels := [][]Resource{{inst("A"), inst("B")}, {inst("C")}}
	notes, err := applyLevels(levels, func(r Resource, _ func(string, ...any)) (outcome, error) {
		rec.did(r.Name)
		if r.Name == "B" {
			return outConverged, errors.New("boom")
		}
		return outConverged, nil
	})
	if err == nil || !strings.Contains(err.Error(), "instance/B: boom") {
		t.Fatalf("err = %v", err)
	}
	var nce *NotConvergedError
	if errors.As(err, &nce) {
		t.Error("a failure must not be reported as a mere not-converged error")
	}
	if !rec.ranOnly("A", "B") {
		t.Errorf("ran %v; level 1 must not start after a failure", rec.ran)
	}
	if !strings.Contains(notes[len(notes)-1], "1 failed") {
		t.Errorf("summary = %q", notes[len(notes)-1])
	}
}

// ---- validation and parsing

func TestValidateOnImageChange(t *testing.T) {
	pinned := "ghcr:home-assistant/home-assistant:2026.9.4@sha256:aa"
	ok := func(r Resource) {
		t.Helper()
		if err := Validate(r); err != nil {
			t.Errorf("%+v: unexpected error %v", r, err)
		}
	}
	bad := func(r Resource, want string) {
		t.Helper()
		if err := Validate(r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: error = %v, want it to contain %q", r, err, want)
		}
	}
	ok(Resource{Kind: KindInstance, Name: "x"})
	ok(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeReport})
	ok(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeIgnore, Image: "ghcr:a/b:tag"})
	ok(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeRebuild, Image: pinned})
	ok(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeRebuild, Image: pinned, SnapshotVolumes: true})

	bad(Resource{Kind: KindInstance, Name: "x", OnImageChange: "yolo"}, "on_image_change must be")
	bad(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeRebuild, Image: "ghcr:a/b:2026.9.4"}, "digest-pinned")
	bad(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeRebuild}, "requires an image")
	bad(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeRebuild, Image: pinned, VM: true}, "does not support VMs")
	bad(Resource{Kind: KindInstance, Name: "x", OnImageChange: OnImageChangeRebuild, Image: pinned, Config: map[string]string{"snapshots.schedule": "@daily"}}, "snapshots.schedule")
	bad(Resource{Kind: KindInstance, Name: "x", SnapshotVolumes: true}, "only applies with on_image_change: rebuild")
	bad(Resource{Kind: KindProfile, Name: "p", OnImageChange: OnImageChangeReport}, "does not use field")
}

func TestLoadFileParsesOnImageChangeFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tink.yaml")
	doc := "kind: instance\nname: ha\nimage: ghcr:home-assistant/home-assistant:2026.9.4@sha256:aa\non_image_change: rebuild\nsnapshot_volumes: true\n---\nkind: instance\nname: plain\n"
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != 2 || rs[0].OnImageChange != OnImageChangeRebuild || !rs[0].SnapshotVolumes {
		t.Fatalf("parsed %+v", rs)
	}
	if rs[1].OnImageChange != "" || rs[1].onImageChangePolicy() != OnImageChangeReport {
		t.Errorf("default policy should be report, got %q", rs[1].onImageChangePolicy())
	}
}
