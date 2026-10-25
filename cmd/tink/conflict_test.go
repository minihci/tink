package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A whole-object PUT sends the ETag it read, and Incus answers 412 if the object changed since. The read, the change and the write must be done again
// from a new read then (incusapi.RetryOnConflict), so a conflict with another writer is not a failed apply. This fails if tink's own code calls one of the
// updates that take an ETag outside a function literal passed to RetryOnConflict.
//
// The one named exception is a function whose every caller is such a literal, and says so in its comment.
func TestEveryETagWriteIsRetriedOnConflict(t *testing.T) {
	update := regexp.MustCompile(`^Update(Profile|Project|Network|NetworkACL|Image|StoragePoolVolume|Instance|StoragePoolBucket|NetworkZone|ClusterGroup)$`)
	calledOnlyInsideRetry := map[string]bool{"adoptOnce": true}

	var bad []string
	fset := token.NewFileSet()
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
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || calledOnlyInsideRetry[fn.Name.Name] {
				continue
			}
			walkRetry(fn.Body, false, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && update.MatchString(sel.Sel.Name) {
					bad = append(bad, fset.Position(call.Pos()).String()+": "+sel.Sel.Name)
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Errorf("ETag writes outside incusapi.RetryOnConflict (read, change and write inside its function, so a 412 is redone from a new read):\n  %s", strings.Join(bad, "\n  "))
	}
}

// walkRetry calls found for every call in n that is not inside a function literal passed straight to RetryOnConflict.
func walkRetry(n ast.Node, inside bool, found func(*ast.CallExpr)) {
	ast.Inspect(n, func(c ast.Node) bool {
		call, ok := c.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isRetryOnConflict(call) {
			for _, arg := range call.Args {
				if lit, ok := arg.(*ast.FuncLit); ok {
					walkRetry(lit.Body, true, found)
				} else {
					walkRetry(arg, inside, found)
				}
			}
			return false
		}
		if !inside {
			found(call)
		}
		return true
	})
}

func isRetryOnConflict(call *ast.CallExpr) bool {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name == "RetryOnConflict"
	case *ast.Ident:
		return f.Name == "RetryOnConflict"
	}
	return false
}
