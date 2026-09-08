package foraroute_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestIsVerdict_TracksTheSDKsOwnVerdictSet pins IsVerdict to the classifier it
// mirrors. The SDK decides which resolver failures are verdicts inside its
// client tier's vetExchangeEndpoint and exports no predicate for the set, so
// this package keeps a copy — and the package comment names the hazard: a
// sentinel the SDK adds that the copy misses is reported as retryable, and
// nothing fails.
//
// This is what fails. Both sets are read out of SOURCE — the SDK's classifier
// at the pinned revision, and IsVerdict here — as the sentinels each passes to
// errors.Is, and they must be equal. A re-pin that grows the SDK's set turns the
// suite red until the copy is updated; a sentinel dropped upstream does the
// same, so the copy cannot go on calling a verdict what the SDK no longer does.
// A hand-written list of "the sentinels the SDK exports" would itself be a
// third copy, maintained from memory, which is the defect this guards against.
func TestIsVerdict_TracksTheSDKsOwnVerdictSet(t *testing.T) {
	t.Parallel()
	sdk := verdictSentinels(t, filepath.Join(sdkPackageDir(t, "connect"), "route.go"), "vetExchangeEndpoint")
	ours := verdictSentinels(t, "foraroute.go", "IsVerdict")
	if len(sdk) == 0 {
		t.Fatal("read no errors.Is sentinels out of the SDK's vetExchangeEndpoint — " +
			"the SDK moved or renamed its classifier, and this guard needs its new location")
	}
	if strings.Join(sdk, ",") != strings.Join(ours, ",") {
		t.Fatalf("IsVerdict checks %v but the pinned SDK's vetExchangeEndpoint classifies %v as verdicts — "+
			"update IsVerdict to the SDK's set", ours, sdk)
	}
}

// verdictSentinels parses file, finds the function named fn, and returns the
// sorted "pkg.Name" of every second argument to an errors.Is call in its body.
func verdictSentinels(t *testing.T, file, fn string) []string {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	var body *ast.BlockStmt
	for _, decl := range parsed.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == fn {
			body = f.Body
		}
	}
	if body == nil {
		t.Fatalf("%s declares no function %s", file, fn)
	}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 || !isSelector(call.Fun, "errors", "Is") {
			return true
		}
		if sel, ok := call.Args[1].(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok {
				out = append(out, pkg.Name+"."+sel.Sel.Name)
			}
		}
		return true
	})
	sort.Strings(out)
	return out
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// sdkPackageDir resolves the on-disk directory of one package of the pinned SDK
// through the module system, so the test reads the revision go.mod names and
// not whichever checkout happens to be nearby.
func sdkPackageDir(t *testing.T, pkg string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "go", "list", "-f", "{{.Dir}}",
		"github.com/FORA-Protocol/protocol/sdk/go/"+pkg).Output()
	if err != nil {
		t.Fatalf("locate the pinned SDK's %s package: %v", pkg, err)
	}
	return strings.TrimSpace(string(out))
}
