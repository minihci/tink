package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The glossary's "Time words": an instant is `<past-participle>-at`, and a bare `at`, `time` or `tick` is not a key or the last part of one.
// The one place `at` stands alone is inside an event record, where an `event` field beside it names the verb. This is the test that keeps the
// rule from drifting: it reads tink's own non-test source for a JSON field or a stamp key that breaks it. (Like keynamespace_test.go, it is
// about tink's code and not about a user's own keys, or the names of other tools' formats, which are mirrored where tink reads them.)

var bareTimeWords = map[string]bool{"at": true, "time": true, "tick": true}

// timeNamingProblems parses one Go source file and lists what breaks the rule.
func timeNamingProblems(filename, src string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	var problems []string
	at := func(p token.Pos) string { return fset.Position(p).String() }

	ast.Inspect(file, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.StructType:
			fields := map[string]*ast.Field{}
			for _, f := range n.Fields.List {
				if name := jsonName(f); name != "" {
					fields[name] = f
				}
			}
			if _, isEvent := fields["event"]; isEvent {
				return true // an event record: a bare `at` beside `event` is the one allowed place
			}
			for name, f := range fields {
				if bareTimeWords[name] {
					problems = append(problems, at(f.Pos())+": the JSON field "+strconv.Quote(name)+" is a bare time word; name the instant (created-at, written-at, ...), or put it in a record with an `event` field")
				}
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(n.Value)
			if err != nil {
				return true
			}
			if key, ok := stampKeyEnd(s); ok && bareTimeWords[key] {
				problems = append(problems, at(n.Pos())+": the stamp key "+strconv.Quote(s)+" ends in the bare time word "+strconv.Quote(key)+"; name the instant (copied-at, failed-at, ...)")
			}
		}
		return true
	})
	return problems, nil
}

// jsonName is the name a struct field has in JSON, or "" when it has none or is left out.
func jsonName(f *ast.Field) string {
	if f.Tag == nil {
		return ""
	}
	tag, err := strconv.Unquote(f.Tag.Value)
	if err != nil {
		return ""
	}
	name, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}

// stampKeyEnd is the last part of a stamp key in a string literal: of a whole key (user.tink.a.b.at), or of the suffix a key is built with
// (".at" added to a prefix). Parts are separated by dots (a hyphen joins the words of one part, so `verified-at` is fine). It is false for a literal that is neither.
func stampKeyEnd(s string) (string, bool) {
	whole := strings.HasPrefix(s, "user.tink.")
	suffix := strings.HasPrefix(s, ".") && !strings.ContainsAny(s[1:], " /\\:")
	if !whole && !suffix {
		return "", false
	}
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:], true
	}
	return s, true
}

func TestTheNamingCheckSeesABareTimeWord(t *testing.T) {
	bad := map[string]string{
		"a bare at":           "package p\ntype T struct {\n\tAt string `json:\"at\"`\n}\n",
		"a bare time":         "package p\ntype T struct {\n\tTime string `json:\"time,omitzero\"`\n}\n",
		"a bare tick":         "package p\ntype T struct {\n\tTick string `json:\"tick\"`\n}\n",
		"a stamp ending at":   "package p\nvar k = \"user.tink.outcome.backup.copy.x.at\"\n",
		"a suffix .at":        "package p\nvar k = prefix + target + \".at\"\n",
		"a stamp ending time": "package p\nvar k = \"user.tink.thing.time\"\n",
	}
	for name, src := range bad {
		if got, err := timeNamingProblems(name+".go", src); err != nil || len(got) != 1 {
			t.Errorf("%s: %v %v, want exactly one problem", name, got, err)
		}
	}
	good := map[string]string{
		"an event record":                 "package p\ntype E struct {\n\tEvent string `json:\"event\"`\n\tAt string `json:\"at\"`\n}\n",
		"named instants":                  "package p\ntype T struct {\n\tA string `json:\"written-at\"`\n\tB string `json:\"created-at,omitzero\"`\n}\nvar k = \"user.tink.outcome.backup.verified-at\"\nvar s = \".copied-at\"\n",
		"since and until":                 "package p\ntype T struct {\n\tA string `json:\"since\"`\n\tB string `json:\"until\"`\n}\nvar k = \"user.tink.x.failing-since\"\n",
		"a word that merely contains one": "package p\nvar k = \"user.tink.outcome.backup.format\"\nvar f = \".json\"\nvar g = \"user.tink.thing-time\"\n",
		"a field with no JSON name":       "package p\ntype T struct {\n\tAt string\n\tTime string `json:\"-\"`\n}\n",
	}
	for name, src := range good {
		if got, err := timeNamingProblems(name+".go", src); err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v, want none", name, got, err)
		}
	}
}

func TestNoJSONFieldOrStampKeyIsABareTimeWord(t *testing.T) {
	var problems []string
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "testdata" || name == "examples" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found, err := timeNamingProblems(path, string(b))
		problems = append(problems, found...)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Errorf("a bare time word in tink's own names (docs/glossary.md, \"Time words\"):\n  %s", strings.Join(problems, "\n  "))
	}
}
