// Structural guard: the publisher console asks the Exchange to verify a domain
// in exactly one place, and only after that same function has asked the site
// and found both of the last step's tasks done.
//
// Verifying a domain is what lets its pages be sold. The Exchange reads the
// site's discovery document again itself before it verifies, so nothing the
// console reads about the file can verify a domain the Exchange would not. The
// package is different: it runs on the publisher's own CDN, and only the
// console asks the site whether it answers. So the guard holds the package
// half, and checks this much:
//
//   - CheckDomain is called exactly once in the console's code, in
//     checkConnection in the sanctioned file, and that file exists;
//   - before that call, a statement of its own declares the tasks as
//     C := connectionOf(..., s.inspectEdge(...), ...), with the inspectEdge
//     call an argument of connectionOf itself, so the tasks can only be built
//     from what the site answers in this same call;
//   - nothing else in the function writes C: no other assignment to C or to a
//     part of it, no second declaration of C, no C++ or C--, and no &C;
//   - between the two, a statement of its own returns when !C.Ready().
//
// It does not see a write through a pointer-receiver method or a range clause.
// What Ready decides is TestConnectionOf's to pin, in the wizard package, and
// which domain the probe asks is held by the console's integration tests.
package guards

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// consoleVerifyHome and consoleVerifyFunc are the one function sanctioned to
// ask the Exchange to verify a domain.
const (
	consoleVerifyHome = "src/console/internal/wizard/connection.go"
	consoleVerifyFunc = "checkConnection"
)

// verifyMethod is the call that asks the Exchange to verify a domain,
// tasksBuilder the one that builds the last step's two tasks from what was
// read, siteProbe the one that asks the site, and readyGate the one that says
// whether both tasks are done.
const (
	verifyMethod = "CheckDomain"
	tasksBuilder = "connectionOf"
	siteProbe    = "inspectEdge"
	readyGate    = "Ready"
)

// TestConsoleVerifiesDomainsOnlyAfterThePackageCheck fails when any console
// file other than the sanctioned one asks the Exchange to verify a domain, when
// the sanctioned function asks more or less than once, or when it asks without
// first building both tasks from the site in the same call and returning
// unless they are done.
//
// Zero calls fails too. A renamed function or a deleted call would otherwise
// leave the console verifying nothing and a green gate saying it verifies in
// exactly one place.
func TestConsoleVerifiesDomainsOnlyAfterThePackageCheck(t *testing.T) {
	t.Parallel()
	var walked []string
	forEachAppFile(t, func(rel string, f *ast.File) {
		if !strings.HasPrefix(rel, "src/console/") {
			return
		}
		walked = append(walked, rel)
		for _, problem := range verifyPlacementProblems(rel, f) {
			t.Error(problem)
		}
	})
	for _, problem := range verifyHomeProblems(walked) {
		t.Error(problem)
	}
}

// verifyHomeProblems is the guard's verdict on the files it walked as a whole:
// the sanctioned file has to be among them.
func verifyHomeProblems(walked []string) []string {
	for _, rel := range walked {
		if rel == consoleVerifyHome {
			return nil
		}
	}
	return []string{fmt.Sprintf("%s is not among the console's files, so nothing holds where the console "+
		"asks the Exchange to verify a domain; point consoleVerifyHome at the file that does", consoleVerifyHome)}
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
			"a domain, because it asks the site first", rel, verifyMethod, total, consoleVerifyFunc, consoleVerifyHome)}
	}
	inFunc := verifies[consoleVerifyFunc]
	if total != 1 || len(inFunc) != 1 {
		return []string{fmt.Sprintf("%s calls %s %d times, %d of them in %s; want exactly one, in %s",
			rel, verifyMethod, total, len(inFunc), consoleVerifyFunc, consoleVerifyFunc)}
	}
	return verifyGateProblems(rel, funcNamed(f, consoleVerifyFunc), inFunc[0])
}

// verifyGateProblems is the verdict on the sanctioned function itself: before
// the one verify call at verify, the tasks built from a probe of the site, never
// written again, and a return unless they are ready. It names the first rule
// broken, in the shape the guard wants instead.
func verifyGateProblems(rel string, fn *ast.FuncDecl, verify token.Pos) []string {
	tasks, at := probedTasks(fn.Body)
	switch {
	case tasks == "":
		return []string{fmt.Sprintf("%s's %s does not build its tasks from the site in this same call: declare "+
			"them as a statement of their own, tasks := %s(..., s.%s(...), ...)",
			rel, consoleVerifyFunc, tasksBuilder, siteProbe)}
	case at > verify:
		return []string{fmt.Sprintf("%s's %s asks the site only after it asks the Exchange to verify; "+
			"build the tasks from the site first", rel, consoleVerifyFunc)}
	case writesTo(fn.Body, tasks) != 1:
		return []string{fmt.Sprintf("%s's %s writes %s again after building it from the site; the tasks "+
			"it returns on must be the ones the site answered", rel, consoleVerifyFunc, tasks)}
	case !exitsUnlessReady(fn.Body, tasks, at, verify):
		return []string{fmt.Sprintf("%s's %s does not return when !%s.%s() before it asks the Exchange to "+
			"verify; write that check as a statement of its own between the two", rel, consoleVerifyFunc, tasks, readyGate)}
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

// funcNamed is the function or method with a body that f declares under name.
// The caller has already found a call inside it, so it exists.
func funcNamed(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name && fn.Body != nil {
			return fn
		}
	}
	return nil
}

// probedTasks finds the statement that builds the tasks from a probe of the
// site: a top-level C := connectionOf(...) with a call to inspectEdge among
// connectionOf's own arguments. It returns C's name and where the statement
// starts, or "" when the function has no such statement.
func probedTasks(body *ast.BlockStmt) (string, token.Pos) {
	for _, stmt := range body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			continue
		}
		if name, ok := assign.Lhs[0].(*ast.Ident); ok && buildsFromProbe(assign.Rhs[0]) {
			return name.Name, assign.Pos()
		}
	}
	return "", token.NoPos
}

// buildsFromProbe reports whether e is a call to connectionOf that takes a call
// to inspectEdge as one of its own arguments.
func buildsFromProbe(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || calleeName(call) != tasksBuilder {
		return false
	}
	for _, arg := range call.Args {
		if inner, ok := arg.(*ast.CallExpr); ok && calleeName(inner) == siteProbe {
			return true
		}
	}
	return false
}

// calleeName is the name a call is made by: a function's or a method's.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}

// writesTo counts the writes to name anywhere in body.
func writesTo(body *ast.BlockStmt, name string) int {
	n := 0
	ast.Inspect(body, func(node ast.Node) bool {
		n += writes(node, name)
		return true
	})
	return n
}

// writes counts the writes to name that node itself makes: an assignment to
// name or to a part of it (a field, an element), a declaration of name, name++
// or name--, and &name, which hands out a way to write it.
func writes(node ast.Node, name string) int {
	n := 0
	switch x := node.(type) {
	case *ast.AssignStmt:
		for _, lhs := range x.Lhs {
			if rootIdent(lhs) == name {
				n++
			}
		}
	case *ast.IncDecStmt:
		if rootIdent(x.X) == name {
			n++
		}
	case *ast.ValueSpec:
		for _, id := range x.Names {
			if id.Name == name {
				n++
			}
		}
	case *ast.UnaryExpr:
		if x.Op == token.AND && rootIdent(x.X) == name {
			n++
		}
	}
	return n
}

// rootIdent is the variable at the root of e -- conn for conn,
// conn.Package.Passed, conn[i] and *conn -- or "" when e is rooted in none.
func rootIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x.Name
		case *ast.SelectorExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		default:
			return ""
		}
	}
}

// exitsUnlessReady reports whether body has a statement of its own, after from
// and ending before to, that returns when !name.Ready().
func exitsUnlessReady(body *ast.BlockStmt, name string, from, to token.Pos) bool {
	for _, stmt := range body.List {
		if stmt.Pos() > from && stmt.End() <= to && returnsUnlessReady(stmt, name) {
			return true
		}
	}
	return false
}

// returnsUnlessReady reports whether stmt is if !name.Ready() { ...; return }
// with no init statement and no else.
func returnsUnlessReady(stmt ast.Stmt, name string) bool {
	branch, ok := stmt.(*ast.IfStmt)
	if !ok || branch.Init != nil || branch.Else != nil || len(branch.Body.List) == 0 {
		return false
	}
	if _, ok := branch.Body.List[len(branch.Body.List)-1].(*ast.ReturnStmt); !ok {
		return false
	}
	not, ok := branch.Cond.(*ast.UnaryExpr)
	return ok && not.Op == token.NOT && isReadyOf(not.X, name)
}

// isReadyOf reports whether e is name.Ready(), called with no arguments.
func isReadyOf(e ast.Expr, name string) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != readyGate {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	return ok && recv.Name == name
}

// --- meta-tests: the guard fires on each broken shape, and not on the good one --

// The sanctioned shape, in parts that mirror checkConnection, so each mutant
// below is the same function with one part replaced or moved.
const (
	verifyHead = "package wizard\n\n" +
		"func (s *Service) checkConnection(ctx context.Context, reg Registration) (Connection, bool, error) {\n" +
		"\tpreflight, err := s.registry.PreflightDomain(ctx, reg.Domain)\n" +
		"\tif err != nil {\n\t\treturn Connection{}, false, err\n\t}\n"
	verifyProbe = "\tconn := connectionOf(preflight, s.inspectEdge(ctx, reg.Domain, reg.ContentPathScope))\n"
	verifyGate  = "\tif !conn.Ready() {\n\t\treturn conn, false, nil\n\t}\n"
	verifyAsk   = "\tchecked, err := s.registry.CheckDomain(ctx, reg.Domain)\n" +
		"\tif err != nil {\n\t\treturn conn, false, nil\n\t}\n"
	verifyTail = "\treturn conn, checked.Passed, nil\n}\n"

	sanctionedVerify = verifyHead + verifyProbe + verifyGate + verifyAsk + verifyTail
)

// The guard's message for each rule, as the mutants below expect to read it.
const (
	aboutProbe = "does not build its tasks from the site"
	aboutOrder = "asks the site only after"
	aboutWrite = "writes conn again"
	aboutGate  = "does not return when !conn.Ready()"
)

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
	f := parseSnippet(t, "package wizard\n\nfunc (s *Service) checkConnection() bool { return false }\n")
	if got := verifyPlacementProblems(consoleVerifyHome, f); len(got) != 1 {
		t.Fatalf("the guard did not catch a sanctioned file that verifies nothing: %v", got)
	}
}

func TestMeta_VerifyGuardCatchesAMissingHome(t *testing.T) {
	t.Parallel()
	if got := verifyHomeProblems([]string{"src/console/internal/wizard/steps.go"}); len(got) != 1 {
		t.Fatalf("the guard did not catch a sanctioned file that is gone: %v", got)
	}
	if got := verifyHomeProblems([]string{consoleVerifyHome}); len(got) != 0 {
		t.Fatalf("the guard objected to a tree that has its sanctioned file: %v", got)
	}
}

// TestMeta_VerifyGuardCatchesEachWayAroundAskingTheSite breaks the sanctioned
// shape once per way a verify could go ahead on tasks that are not the site's
// answer in this same call, and requires the guard to name the rule broken.
func TestMeta_VerifyGuardCatchesEachWayAroundAskingTheSite(t *testing.T) {
	t.Parallel()
	notReturning := "\tif !conn.Ready() {\n\t\ts.note()\n\t}\n"
	for _, tc := range []struct {
		name string
		// src is a whole shape, for a mutant that moves parts; edits are
		// replacements in the sanctioned shape, for one that changes a part.
		src   string
		edits [][2]string
		about string
	}{
		{name: "the tasks passed in", about: aboutProbe, edits: [][2]string{
			{verifyProbe, ""}, {"reg Registration)", "reg Registration, conn Connection)"},
		}},
		{name: "the tasks built from a report passed in", about: aboutProbe, edits: [][2]string{
			{"s.inspectEdge(ctx, reg.Domain, reg.ContentPathScope)", "report"},
		}},
		{name: "the tasks made up", about: aboutProbe, edits: [][2]string{
			{verifyProbe, "\tconn := Connection{}\n"},
		}},
		{
			name: "the site asked after the verify", about: aboutOrder,
			src: verifyHead + verifyAsk + verifyProbe + verifyGate + verifyTail,
		},
		{
			name: "the verify asked before the gate", about: aboutGate,
			src: verifyHead + verifyProbe + verifyAsk + verifyGate + verifyTail,
		},
		{name: "the tasks assigned again", about: aboutWrite, edits: [][2]string{
			{verifyGate, "\tconn = Connection{}\n" + verifyGate},
		}},
		{name: "a task set by hand", about: aboutWrite, edits: [][2]string{
			{verifyGate, "\tconn.Package.Passed = true\n" + verifyGate},
		}},
		{name: "the tasks declared again", about: aboutWrite, edits: [][2]string{
			{verifyGate, verifyGate + "\t{\n\t\tconn := Connection{}\n\t\ts.keep(conn)\n\t}\n"},
		}},
		{name: "the tasks handed out", about: aboutWrite, edits: [][2]string{
			{verifyGate, "\ts.keep(&conn)\n" + verifyGate},
		}},
		{name: "the gate on another value", about: aboutGate, edits: [][2]string{
			{verifyGate, "\tother := conn\n\tif !other.Ready() {\n\t\treturn conn, false, nil\n\t}\n"},
		}},
		{name: "a gate that does not return", about: aboutGate, edits: [][2]string{
			{verifyGate, notReturning},
		}},
		{name: "the gate's answer dropped", about: aboutGate, edits: [][2]string{
			{verifyGate, "\t_ = conn.Ready()\n"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := tc.src
			if src == "" {
				src = verifyMutant(t, sanctionedVerify, tc.edits...)
			}
			requireOneVerifyProblem(t, verifyPlacementProblems(consoleVerifyHome, parseSnippet(t, src)), tc.about)
		})
	}
}

// TestMeta_VerifyGuardCatchesTheRealProbeReplaced breaks the real
// checkConnection the way a refactor most easily could -- building the tasks
// from a report that was not asked of the site in this call -- and requires the
// guard to say so, so the guard is seen to fail on the shape production has,
// not only on the snippets above. It also holds the real file to the guard
// unchanged.
func TestMeta_VerifyGuardCatchesTheRealProbeReplaced(t *testing.T) {
	t.Parallel()
	const probe = "s.inspectEdge(ctx, reg.Domain, reg.ContentPathScope)"
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), consoleVerifyHome))
	if err != nil {
		t.Fatalf("read %s: %v", consoleVerifyHome, err)
	}
	src := string(raw)
	if !strings.Contains(src, probe) {
		t.Fatalf("%s no longer asks the site as %s; repoint this test at the probe it has now", consoleVerifyHome, probe)
	}
	if got := verifyPlacementProblems(consoleVerifyHome, parseSnippet(t, src)); len(got) != 0 {
		t.Fatalf("the guard objected to the real %s: %v", consoleVerifyHome, got)
	}
	broken := parseSnippet(t, strings.Replace(src, probe, "report", 1))
	requireOneVerifyProblem(t, verifyPlacementProblems(consoleVerifyHome, broken), aboutProbe)
}

// verifyMutant is src with each edit's first text replaced by its second. An
// edit whose text src does not contain fails the test, so a mutant cannot
// quietly come out the same as the shape it was meant to break.
func verifyMutant(t *testing.T, src string, edits ...[2]string) string {
	t.Helper()
	for _, e := range edits {
		if !strings.Contains(src, e[0]) {
			t.Fatalf("the mutant's edit %q is not in the shape it edits", e[0])
		}
		src = strings.Replace(src, e[0], e[1], 1)
	}
	return src
}

// requireOneVerifyProblem fails the test unless the guard reported exactly one
// problem, and that problem says about.
func requireOneVerifyProblem(t *testing.T, got []string, about string) {
	t.Helper()
	if len(got) != 1 || !strings.Contains(got[0], about) {
		t.Fatalf("the guard reported %v; want one problem saying %q", got, about)
	}
}
