package compose

import (
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/run"
)

// Guard 1 of #205. Every field of run.Options is either filled from compose keys or deliberately not, and the table says which (classify.go).
func TestEveryRunOptionIsClassified(t *testing.T) {
	typ := reflect.TypeOf(run.Options{})
	fields := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		fields[name] = true
		c, ok := optionClasses[name]
		if !ok {
			t.Errorf("run.Options.%s is not in optionClasses: classify it for import (internal/compose/classify.go): the compose keys that fill it, or the reason import does not", name)
			continue
		}
		if (len(c.keys) == 0) == (c.why == "") {
			t.Errorf("optionClasses[%s] must have keys or a reason, and not both: %+v", name, c)
		}
		for _, k := range c.keys {
			if rule, ok := serviceKeys[k]; !ok && k != "services" {
				t.Errorf("optionClasses[%s] names the compose key %q, which the importer does not know", name, k)
			} else if ok && rule.kind != translated {
				t.Errorf("optionClasses[%s] names the compose key %q, which the importer does not translate", name, k)
			}
		}
	}
	for name := range optionClasses {
		if !fields[name] {
			t.Errorf("optionClasses has %s, which is not a field of run.Options (removed? delete the entry)", name)
		}
	}
}

// The other direction of guard 1: a translated service key is either named by a field or says why it is not one.
func TestEveryTranslatedKeyHasAHome(t *testing.T) {
	named := map[string]bool{}
	for _, c := range optionClasses {
		for _, k := range c.keys {
			named[k] = true
		}
	}
	for k, rule := range serviceKeys {
		if rule.kind != translated {
			continue
		}
		if _, ok := notOptions[k]; !named[k] && !ok {
			t.Errorf("service key %q is translated but no run.Options field in optionClasses names it, and it is not in notOptions", k)
		}
	}
	for k := range notOptions {
		if serviceKeys[k].kind != translated {
			t.Errorf("notOptions has %q, which is not a translated service key", k)
		}
	}
}

var backticked = regexp.MustCompile("`([^`]+)`")

// Guard 2 of #205. Every compose key the importer knows has a row in docs/import.md, and every key in that table is known to the importer.
func TestEveryComposeKeyIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/import.md")
	if err != nil {
		t.Fatal(err)
	}
	_, table, ok := strings.Cut(string(doc), "## What it does with each key")
	if !ok {
		t.Fatal(`docs/import.md has no "## What it does with each key" section`)
	}
	if i := strings.Index(table, "\n## "); i >= 0 {
		table = table[:i]
	}
	documented := map[string]bool{}
	for _, line := range strings.Split(table, "\n") {
		if !strings.HasPrefix(line, "|") {
			continue
		}
		first := strings.Split(line, "|")[1]
		for _, m := range backticked.FindAllStringSubmatch(first, -1) {
			if tok := m[1]; !strings.HasPrefix(tok, "$") {
				documented[tok] = true
			}
		}
	}
	known := map[string]bool{}
	for k := range serviceKeys {
		known[k] = true
	}
	for k := range topKeys {
		known[k] = true
	}
	var missing, extra []string
	for k := range known {
		if !documented[k] {
			missing = append(missing, k)
		}
	}
	for k := range documented {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("the importer knows these compose keys, but docs/import.md has no row for them: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("docs/import.md has rows for these keys, which the importer does not know: %v", extra)
	}
}
