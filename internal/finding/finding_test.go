package finding

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestNew_RefusesAnEmptyReason(t *testing.T) {
	for _, reason := range []string{"", "   ", "\t\n"} {
		if f, err := New(Error, reason, "instance/web", "detail"); !errors.Is(err, ErrNoReason) || f != (Finding{}) {
			t.Errorf("New with reason %q = %+v, %v; want ErrNoReason and the zero Finding", reason, f, err)
		}
	}
}

func TestNew_RefusesASeverityThatIsNotOneOfThree(t *testing.T) {
	for _, sev := range []Severity{-1, 3, 99} {
		if _, err := New(sev, "a reason", "", "detail"); err == nil {
			t.Errorf("New with severity %d succeeded", sev)
		}
	}
}

func TestNew_KeepsEveryField(t *testing.T) {
	f, err := New(Warning, "image drift", "instance/default/web", "alias moved")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Finding{Severity: Warning, Reason: "image drift", Subject: "instance/default/web", Detail: "alias moved"}); f != want {
		t.Errorf("got %+v, want %+v", f, want)
	}
}

func TestConstructors_SetTheSeverityTheyNameAndFormatTheDetail(t *testing.T) {
	cases := []struct {
		f    Finding
		want Severity
	}{
		{Errorf("r", "s", "n=%d", 1), Error},
		{Warnf("r", "s", "n=%d", 1), Warning},
		{Notef("r", "s", "n=%d", 1), Note},
	}
	for _, c := range cases {
		if c.f.Severity != c.want || c.f.Detail != "n=1" || c.f.Reason != "r" || c.f.Subject != "s" {
			t.Errorf("got %+v, want severity %v", c.f, c.want)
		}
	}
}

func TestConstructors_PanicOnAnEmptyReason(t *testing.T) {
	for name, build := range map[string]func(){
		"Errorf": func() { Errorf("", "s", "d") },
		"Warnf":  func() { Warnf("", "s", "d") },
		"Notef":  func() { Notef(" ", "s", "d") },
	} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("%s with an empty reason did not panic", name)
				}
			}()
			build()
		}()
	}
}

// The severities mean what the issue says: an error blocks, a warning does not, a note is a fact. The words are the ones every report prints.
func TestSeverity_Words(t *testing.T) {
	for sev, want := range map[Severity]string{Error: "error", Warning: "warning", Note: "note"} {
		if sev.String() != want {
			t.Errorf("%d prints %q, want %q", int(sev), sev.String(), want)
		}
	}
	var zero Severity
	if zero != Note {
		t.Error("the zero severity must be the weakest, a note, so a finding that forgot its severity stops nothing")
	}
}

// The one-line form is what join and the Headscale projection have always printed: severity, then the subject when there is one, then the detail. The
// reason is a key, not text for a person, and is not in it.
func TestString_IsSeveritySubjectDetail(t *testing.T) {
	if got, want := Errorf("a reason", "publish/whoami", "names no instance").String(), "error: publish/whoami: names no instance"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := Warnf("a reason", "", "nothing to render").String(), "warning: nothing to render"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := Notef("a reason", "", "fyi").String(), "note: fyi"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if s := Errorf("secret-free reason", "", "detail").String(); strings.Contains(s, "secret-free reason") {
		t.Errorf("the reason leaked into the line for a person: %q", s)
	}
}

func TestCountAndHasErrors(t *testing.T) {
	fs := []Finding{Warnf("r", "", "a"), Errorf("r", "", "b"), Notef("r", "", "c"), Errorf("r", "", "d")}
	if Count(fs, Error) != 2 || Count(fs, Warning) != 1 || Count(fs, Note) != 1 {
		t.Errorf("counts wrong: %d %d %d", Count(fs, Error), Count(fs, Warning), Count(fs, Note))
	}
	if !HasErrors(fs) || HasErrors(fs[:1]) || HasErrors(nil) {
		t.Error("HasErrors is wrong")
	}
}

func TestSort_ErrorsFirstThenSubjectThenDetail_AndIsStable(t *testing.T) {
	fs := []Finding{
		Warnf("r", "a", "x"),
		Errorf("r", "b", "y"),
		Errorf("r", "a", "z"),
		Errorf("r", "a", "y"),
		Notef("r", "a", "x"),
		Errorf("first raised", "a", "y"),
	}
	Sort(fs)
	var got []string
	for _, f := range fs {
		got = append(got, f.String())
	}
	want := []string{"error: a: y", "error: a: y", "error: a: z", "error: b: y", "warning: a: x", "note: a: x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
	if fs[0].Reason != "r" || fs[1].Reason != "first raised" {
		t.Errorf("equal findings were reordered: %q then %q", fs[0].Reason, fs[1].Reason)
	}
}

// The constructor is where an empty reason is refused, so nothing else may build a Finding by hand: a literal would skip it. A slice of findings
// ([]finding.Finding{...}) is not a literal of one.
func TestNoCodeBuildsAFindingWithoutTheConstructor(t *testing.T) {
	literal := regexp.MustCompile(`[^\]\w]finding\.Finding\{`)
	var bad []string
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "finding") {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if literal.MatchString(line) {
				bad = append(bad, fmt.Sprintf("%s:%d", path, i+1))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("a finding.Finding literal skips the check for a reason; use finding.New, Errorf, Warnf or Notef: %s", strings.Join(bad, ", "))
	}
}
