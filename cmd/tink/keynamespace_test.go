package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/minihci/tink/internal/ownership"
)

// eachSourceFile calls fn with the path and text of every non-test Go file of the module.
func eachSourceFile(t *testing.T, fn func(path, text string)) {
	t.Helper()
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
		fn(path, string(b))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Everything tink reads or writes on an object's metadata lives under user.tink.*, so that one filter finds all of it. This fails if tink's own
// code introduces a key outside that namespace. It is about tink's code and not about users' own keys, which tink never judges.
func TestTinkKeysAreUnderTheTinkNamespace(t *testing.T) {
	literal := regexp.MustCompile(`"(user\.[A-Za-z0-9_.\-]*)`)
	var bad []string
	eachSourceFile(t, func(path, text string) {
		for _, m := range literal.FindAllStringSubmatch(text, -1) {
			if !strings.HasPrefix(m[1], "user.tink.") {
				bad = append(bad, path+": "+m[1])
			}
		}
	})
	if len(bad) > 0 {
		t.Errorf("keys outside user.tink.* in tink's own code (put them under user.tink.):\n  %s", strings.Join(bad, "\n  "))
	}
}

// Every key under user.tink.* is a fact, intent or an outcome, and the class is the prefix (internal/ownership). This fails if tink's own code writes
// a user.tink.* literal that is none of the three: neither under user.tink.intent. or user.tink.outcome. nor on the short list of facts there. A new key
// cannot be added without choosing its class; one built from IntentPrefix or OutcomePrefix has chosen it by being built from it.
func TestEveryTinkKeyHasAClass(t *testing.T) {
	literal := regexp.MustCompile(`"(user\.tink\.[A-Za-z0-9_.\-]*)`)
	var bad []string
	eachSourceFile(t, func(path, text string) {
		for _, m := range literal.FindAllStringSubmatch(text, -1) {
			if ownership.ClassOf(m[1]) == ownership.Unclassified {
				bad = append(bad, path+": "+m[1])
			}
		}
	})
	if len(bad) > 0 {
		t.Errorf("keys under user.tink.* that are neither a fact nor intent nor an outcome (put them under %s or %s, or add a fact to internal/ownership):\n  %s",
			ownership.IntentPrefix, ownership.OutcomePrefix, strings.Join(bad, "\n  "))
	}
}
