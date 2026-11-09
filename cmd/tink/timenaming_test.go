package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/minihci/tink/internal/backuprun"
	"github.com/minihci/tink/internal/daemon"
	"github.com/minihci/tink/internal/finding"
	"github.com/minihci/tink/internal/helper"
	"github.com/minihci/tink/internal/instant"
	"github.com/minihci/tink/internal/observe"
)

// The glossary's "Time words": an instant is `<past-participle>-at`, and a bare `at`, `time` or `tick` is not a key or the last part of one.
// The one place `at` stands alone is inside an event record, where an `event` field beside it names the verb. This is the test that keeps the
// rule from drifting: it reads tink's own non-test source for a JSON field or a stamp key that breaks it. (Like keynamespace_test.go, it is
// about tink's code and not about a user's own keys, or the names of other tools' formats, which are mirrored where tink reads them.)
//
// The same file holds the rule for separators (glossary, "JSON keys": none of tink's own JSON keys has an underscore; a hyphen joins the words of
// one) and the list of the keys that belong to another tool's format.

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
	eachSourceFile(t, func(path, src string) {
		found, err := timeNamingProblems(path, src)
		if err != nil {
			t.Fatal(err)
		}
		problems = append(problems, found...)
	})
	if len(problems) > 0 {
		t.Errorf("a bare time word in tink's own names (docs/glossary.md, \"Time words\"):\n  %s", strings.Join(problems, "\n  "))
	}
}

// externalKeys are the JSON keys with an underscore that tink does not define: it reads another tool's document, and a key of that
// document is spelled the way its owner spells it (the glossary's rule for other tools' words). Each is listed under the file that holds
// it, with the format it comes from; a key that is not here is tink's own and has no underscore.
var externalKeys = map[string]map[string]string{
	"internal/imageconfig/imageconfig.go": {"access_token": "the token response of a container registry's token service"},
	"internal/mesh/headscale/users.go":    {"display_name": "the Headscale REST API's user object"},
}

// underscoreKeyProblems parses one Go source file and lists the JSON field names with an underscore that are not a key of another
// tool's format (external, by name). It reads struct tags, which is where tink defines the keys of the documents it writes and prints.
func underscoreKeyProblems(filename, src string, external map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	var problems []string
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		for _, f := range st.Fields.List {
			if name := jsonName(f); strings.Contains(name, "_") && external[name] == "" {
				problems = append(problems, fset.Position(f.Pos()).String()+": the JSON key "+strconv.Quote(name)+" has an underscore; spell it "+strconv.Quote(strings.ReplaceAll(name, "_", "-"))+", or, if it is another tool's key, list it in externalKeys with the format it comes from")
			}
		}
		return true
	})
	return problems, nil
}

func TestTheUnderscoreCheckSeesAnUnderscoreKey(t *testing.T) {
	ext := map[string]string{"access_token": "a registry"}
	bad := map[string]string{
		"an underscore":      "package p\ntype T struct {\n\tA string `json:\"last_job\"`\n}\n",
		"with an option":     "package p\ntype T struct {\n\tA string `json:\"dry_run,omitempty\"`\n}\n",
		"in a nested struct": "package p\ntype T struct {\n\tA struct {\n\t\tB int `json:\"exit_code\"`\n\t} `json:\"a\"`\n}\n",
		"another tool's key": "package p\ntype T struct {\n\tA string `json:\"display_name\"`\n}\n",
	}
	for name, src := range bad {
		if got, err := underscoreKeyProblems(name+".go", src, ext); err != nil || len(got) != 1 {
			t.Errorf("%s: %v %v, want exactly one problem", name, got, err)
		}
	}
	good := map[string]string{
		"a hyphen":             "package p\ntype T struct {\n\tA string `json:\"last-job,omitempty\"`\n}\n",
		"the external key":     "package p\ntype T struct {\n\tA string `json:\"access_token\"`\n}\n",
		"a field with no name": "package p\ntype T struct {\n\tLast_job string\n\tB string `json:\"-\"`\n\tC string `yaml:\"on_image_change\"`\n}\n",
	}
	for name, src := range good {
		if got, err := underscoreKeyProblems(name+".go", src, ext); err != nil || len(got) != 0 {
			t.Errorf("%s: %v %v, want none", name, got, err)
		}
	}
}

func TestNoJSONKeyTinkDefinesHasAnUnderscore(t *testing.T) {
	var problems []string
	used := map[string]map[string]bool{}
	eachSourceFile(t, func(path, src string) {
		path = strings.TrimPrefix(filepath.ToSlash(path), "../../") // externalKeys is keyed by the path from the repository root
		found, err := underscoreKeyProblems(path, src, externalKeys[path])
		if err != nil {
			t.Fatal(err)
		}
		problems = append(problems, found...)
		for key := range externalKeys[path] {
			if strings.Contains(src, "`json:\""+key+"\"") || strings.Contains(src, "`json:\""+key+",") {
				if used[path] == nil {
					used[path] = map[string]bool{}
				}
				used[path][key] = true
			}
		}
	})
	if len(problems) > 0 {
		t.Errorf("an underscore in a JSON key tink defines (docs/glossary.md, \"JSON keys\"):\n  %s", strings.Join(problems, "\n  "))
	}
	// An entry that matches no field any more is a stale one, and would let that key come back unnoticed.
	for path, keys := range externalKeys {
		for key := range keys {
			if !used[path][key] {
				t.Errorf("externalKeys lists %q in %s, which has no such JSON field; remove the entry", key, path)
			}
		}
	}
}

// jsonKeys lists every object key of a JSON document, at any depth.
func jsonKeys(t *testing.T, doc []byte) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	var keys []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				keys = append(keys, k)
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	walk(v)
	sort.Strings(keys)
	return keys
}

// The documents tink writes or prints, filled in so that every field appears, carry the keys below and no key with an underscore.
func TestTheDocumentsTinkWritesHaveNoUnderscoreKeys(t *testing.T) {
	when := instant.At(time.Date(2026, 11, 1, 9, 30, 0, 0, time.UTC))
	status := helper.Status{Proto: helper.StatusProto, Version: "v1", JobProto: 2, PolicyProto: 1, TZ: "UTC", StartedAt: when, WrittenAt: when,
		Heartbeat: instant.Duration(10 * time.Minute),
		States: []observe.Record{
			{What: "backup", Resource: "lib", State: "skipped", Reason: "x", Since: when},
			{What: "backup", Resource: "lib", Target: "t", State: "failing", Reason: "r", Since: when, FailedAt: when, Failures: 2},
			{What: "derived", Resource: "ingress-routes", Site: "s", State: "failing", Reason: "r", Since: when},
		},
		Events:  []observe.Event{{Event: "healed", What: "derived", Resource: "ingress-routes", Site: "s", At: when}},
		LastJob: &helper.LastJob{ID: "j", State: "failed", FinishedAt: when}, Running: 1, Queued: 2, Draining: true,
		Remotes: []helper.Remote{{Name: "host", Addr: "https://192.0.2.10:8443", Project: "default"}}}
	stored, err := helper.Encode(status)
	if err != nil {
		t.Fatal(err)
	}
	var printed bytes.Buffer
	report := helper.Report{Health: helper.Degraded, Found: helper.Found{Project: "p", Name: "h", State: "Running", StatusVolume: "default/helper-status"}, Status: &status, Findings: []finding.Finding{finding.Warnf("a reason", "instance/default/h", "r")}}
	if err := printHelperJSON(&printed, []helper.Report{report}); err != nil {
		t.Fatal(err)
	}
	summary, err := json.Marshal(backuprun.Report{Copies: []backuprun.CopyReport{{Volume: "lib", Target: "t", Outcome: backuprun.Copied, Detail: "d",
		RestorePoint: "lib-2026", Pruned: []string{"a"}, Swept: []string{"b"}, OtherServers: []string{"c"}}}, Unknown: []string{"u"}, Failed: 1, Tried: 2, Skipped: 3})
	if err != nil {
		t.Fatal(err)
	}
	args, err := json.Marshal(daemon.BackupRunArgs{Volumes: []string{"lib"}, Due: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}

	docs := []struct {
		name string
		doc  []byte
		want []string // keys the document must carry, at any depth
	}{
		{"the status document", []byte(stored), []string{"job-proto", "policy-proto", "last-job", "running-jobs", "queued-jobs", "written-at", "heartbeat", "failed-at", "failures", "states", "events"}},
		{"tink helper status --json", printed.Bytes(), []string{"instance-state", "exit-code", "status-volume", "status", "last-job", "job-proto"}},
		{"the backup run summary", summary, []string{"restore-point", "other-servers"}},
		{"the backup-run job's arguments", args, []string{"dry-run"}},
	}
	for _, d := range docs {
		keys := jsonKeys(t, d.doc)
		have := map[string]bool{}
		for _, k := range keys {
			have[k] = true
			if strings.Contains(k, "_") {
				t.Errorf("%s has the key %q", d.name, k)
			}
		}
		for _, k := range d.want {
			if !have[k] {
				t.Errorf("%s has no key %q: %v", d.name, k, keys)
			}
		}
	}
}
