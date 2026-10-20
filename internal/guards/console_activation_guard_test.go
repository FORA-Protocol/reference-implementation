// Structural guard: the publisher console asks the Exchange to verify a domain
// in exactly one place, and only after it has seen the domain's package answer.
//
// Verifying a domain is what lets its pages be sold. The Exchange reads the
// site's discovery document itself before it verifies, but it cannot see the
// edge package, which runs on the publisher's own CDN. The console can, so its
// server checks the package right before it asks. A second place that asks
// would be a way to verify a domain without that check, and so would the one
// sanctioned place if it stopped checking first. This guard catches both.
package guards

import (
	"fmt"
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

// consoleVerifyHome and consoleVerifyFunc are the one function sanctioned to
// ask the Exchange to verify a domain.
const (
	consoleVerifyHome = "src/console/internal/wizard/finish.go"
	consoleVerifyFunc = "activate"
)

// verifyMethod is the call that asks the Exchange to verify a domain, and
// packageGate the one whose answer has to come before it.
const (
	verifyMethod = "CheckDomain"
	packageGate  = "CanAdvance"
)

// TestConsoleVerifiesDomainsOnlyAfterThePackageCheck fails when any console
// file other than the sanctioned one asks the Exchange to verify a domain, when
// the sanctioned function asks more or less than once, or when it asks before
// it has checked the package.
//
// Zero calls fails too. A renamed function or a deleted call would otherwise
// leave the console verifying nothing and a green gate saying it verifies in
// exactly one place.
func TestConsoleVerifiesDomainsOnlyAfterThePackageCheck(t *testing.T) {
	t.Parallel()
	forEachAppFile(t, func(rel string, f *ast.File) {
		if !strings.HasPrefix(rel, "src/console/") {
			return
		}
		for _, problem := range verifyPlacementProblems(rel, f) {
			t.Error(problem)
		}
	})
}

// verifyPlacementProblems is the guard's verdict on one file.
func verifyPlacementProblems(rel string, f *ast.File) []string {
	verifies := callsByFunc(f, verifyMethod)
	total := 0
	for _, positions := range verifies {
		total += len(positions)
	}
	if rel != consoleVerifyHome {
		if total == 0 {
			return nil
		}
		return []string{fmt.Sprintf("%s calls %s %d times -- only %s in %s may ask the Exchange to verify "+
			"a domain, because it checks the package first", rel, verifyMethod, total, consoleVerifyFunc, consoleVerifyHome)}
	}
	inFunc := verifies[consoleVerifyFunc]
	if total != 1 || len(inFunc) != 1 {
		return []string{fmt.Sprintf("%s calls %s %d times, %d of them in %s; want exactly one, in %s",
			rel, verifyMethod, total, len(inFunc), consoleVerifyFunc, consoleVerifyFunc)}
	}
	gates := callsByFunc(f, packageGate)[consoleVerifyFunc]
	if len(gates) == 0 || gates[0] > inFunc[0] {
		return []string{fmt.Sprintf("%s's %s asks the Exchange to verify a domain before it checks the package "+
			"(%s); the package check has to come first", rel, consoleVerifyFunc, packageGate)}
	}
	return nil
}

// callsByFunc maps each function declared in f onto the positions, in source
// order, of the calls it makes to a method or package function named name.
// Calls inside a function literal count towards the function that holds it.
func callsByFunc(f *ast.File, name string) map[string][]token.Pos {
	out := map[string][]token.Pos{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == name {
				out[fn.Name.Name] = append(out[fn.Name.Name], call.Pos())
			}
			return true
		})
	}
	return out
}

// --- meta-tests: the guard fires on each broken shape, and not on the good one --

// sanctionedVerify is the shape the guard requires: the package checked, then
// the Exchange asked, once.
const sanctionedVerify = "package wizard\n\n" +
	"func (s *Service) activate() bool {\n" +
	"\tif !s.inspect().CanAdvance() {\n\t\treturn false\n\t}\n" +
	"\tres, _ := s.registry.CheckDomain()\n\treturn res.Passed\n}\n"

func TestMeta_VerifyGuardPassesTheSanctionedShape(t *testing.T) {
	t.Parallel()
	if got := verifyPlacementProblems(consoleVerifyHome, parseSnippet(t, sanctionedVerify)); len(got) != 0 {
		t.Fatalf("the guard objected to the sanctioned shape: %v", got)
	}
}

func TestMeta_VerifyGuardCatchesASecondPlace(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package web\n\nfunc (a *API) Verify() { _, _ = a.client.CheckDomain() }\n")
	if got := verifyPlacementProblems("src/console/internal/web/api_domain.go", f); len(got) != 1 {
		t.Fatalf("the guard did not catch a call outside %s: %v", consoleVerifyHome, got)
	}
}

func TestMeta_VerifyGuardCatchesAVerifyBeforeThePackageCheck(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package wizard\n\n"+
		"func (s *Service) activate() bool {\n"+
		"\tres, _ := s.registry.CheckDomain()\n"+
		"\treturn res.Passed && s.inspect().CanAdvance()\n}\n")
	if got := verifyPlacementProblems(consoleVerifyHome, f); len(got) != 1 {
		t.Fatalf("the guard did not catch a verify asked before the package check: %v", got)
	}
}

func TestMeta_VerifyGuardCatchesASecondCallInTheSanctionedFile(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, sanctionedVerify+
		"\nfunc (s *Service) again() { _, _ = s.registry.CheckDomain() }\n")
	if got := verifyPlacementProblems(consoleVerifyHome, f); len(got) != 1 {
		t.Fatalf("the guard did not catch a second call in %s: %v", consoleVerifyHome, got)
	}
}

func TestMeta_VerifyGuardCatchesNoCallAtAll(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package wizard\n\nfunc (s *Service) activate() bool { return false }\n")
	if got := verifyPlacementProblems(consoleVerifyHome, f); len(got) != 1 {
		t.Fatalf("the guard did not catch a sanctioned file that verifies nothing: %v", got)
	}
}
