package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Everything tink reads or writes on an object's metadata lives under user.tink.*, so that one filter finds all of it. This fails if tink's own
// code introduces a key outside that namespace. It is about tink's code and not about users' own keys, which tink never judges.
//
// The one exception is the ingress registration keys' old names, which are still read so that instances registered under them keep their
// routes (internal/ingress/keys.go). Remove it from the list when that support goes.
func TestTinkKeysAreUnderTheTinkNamespace(t *testing.T) {
	legacy := map[string]bool{
		"user.ingress.enabled": true, "user.ingress.domain": true, "user.ingress.port": true,
	}
	literal := regexp.MustCompile(`"(user\.[A-Za-z0-9_.\-]*)`)
	var bad []string
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
		for _, m := range literal.FindAllStringSubmatch(string(b), -1) {
			key := m[1]
			if strings.HasPrefix(key, "user.tink.") || legacy[key] {
				continue
			}
			bad = append(bad, path+": "+key)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("keys outside user.tink.* in tink's own code (put them under user.tink.):\n  %s", strings.Join(bad, "\n  "))
	}
}
