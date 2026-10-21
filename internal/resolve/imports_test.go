package resolve

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The converger knows about kinds, never about capabilities (docs/roadmap.md, "Three extension points"; docs/resolver-architecture.md, "Extension
// points"). A capability reaches it through a seam the converger can import without importing the capability (backupext for backup), so this
// fails if the resolver's own code imports an internal package that is not on the list below. The list is the rule: adding a line to it is
// deciding that the converger now depends on that package, which a capability must never be.
//
// It reads the import declarations of the non-test files, so it needs no go toolchain and sees exactly what the compiler would link in directly.
const modulePrefix = "github.com/minihci/tink/internal/"

var allowedInternalImports = map[string]string{
	"incusapi":  "ring 0: the Incus client the converger converges through",
	"incusconf": "ring 0: reading the Incus CLI's remotes and credentials",
	"remote":    "ring 0: the remotes tink may talk to",
	"secrets":   "ring 0: ${secret:NAME} expansion and redaction",
	"run":       "ring 1: the instance engine (create, apply config, start, qualify) that applying an instance IS; it imports only ring 0",
	"ownership": "ring 0: the stamp key and its classification (user.tink.stack), importing nothing of tink's",
	"backupext": "the seam for backup: the declaration types and the Backup interface, importing nothing of tink's",
	"helper":    "KNOWN EXCEPTION, not yet moved: kind: helper (helperkind.go) installs and evaluates the helper; it needs a kind extension point first (issue #128, 'what remains')",
}

func TestTheConvergerImportsNoCapability(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string][]string{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if rest, ok := strings.CutPrefix(path, modulePrefix); ok {
				used[rest] = append(used[rest], file)
			}
		}
	}
	var bad []string
	for pkg, in := range used {
		if _, ok := allowedInternalImports[pkg]; !ok {
			sort.Strings(in)
			bad = append(bad, pkg+" (in "+strings.Join(in, ", ")+")")
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		t.Errorf("internal/resolve imports packages that are not on the converger's list (a capability reaches the converger through an extension point, "+
			"and the converger imports none of them; see the package comment):\n  %s", strings.Join(bad, "\n  "))
	}
	// A stale entry would let a package be imported again without anyone deciding to.
	for pkg := range allowedInternalImports {
		if _, ok := used[pkg]; !ok {
			t.Errorf("%s is on the allowed list but the converger no longer imports it: remove it from the list", pkg)
		}
	}
}
