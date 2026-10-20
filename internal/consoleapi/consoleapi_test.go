package consoleapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestAllReasons_ListsEveryReasonConstant holds AllReasons to the constants
// this package declares. The console proves it has a sentence for every entry
// of AllReasons, so a Reason constant left off the list would reach a publisher
// as a bare code with nothing failing.
func TestAllReasons_ListsEveryReasonConstant(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "consoleapi.go", nil, 0)
	if err != nil {
		t.Fatalf("parse consoleapi.go: %v", err)
	}
	var declared []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Reason") {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					t.Fatalf("%s is not a string literal", name.Name)
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", name.Name, err)
				}
				declared = append(declared, v)
			}
		}
	}
	listed := AllReasons()
	slices.Sort(declared)
	slices.Sort(listed)
	if !slices.Equal(declared, listed) {
		t.Fatalf("AllReasons() = %v, the declared Reason constants are %v", listed, declared)
	}
}
