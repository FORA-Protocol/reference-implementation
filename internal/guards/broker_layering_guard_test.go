// Broker layering guards: pin the target topology of the broker layering
// pass ("shared SHAPE, not shared PACKAGE" — mirror the Exchange
// thin-adapter→service exemplar locally in the broker).
//
// Their behavioural coverage is the Broker transport integration suite (the
// purchase, resolve, registry-health and selection-audit tests). These guards
// pin only the structural target that suite cannot see:
//
//  1. The purchase service is the business half of
//     BrokerService.ExecuteTransaction and lives in internal/purchase; the
//     transport package declares none of its names.
//  2. internal/purchase never imports transport or touches
//     http.ResponseWriter (the sink is inverted to typed returns).
//  3. Neither service composes the well-known manifest+WBA handler pair
//     inline; the compose lives once below both services (root internal/).
//
// The depguard slice (broker-purchase-no-transport + fixing the negation-only
// no-circular-internal files list) is lint config, not a test; the
// implementer must prove each new rule bites with a throwaway forbidden
// import before trusting it.
package guards

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// purchasePackageDir is the service layer behind BrokerService.ExecuteTransaction.
const purchasePackageDir = "src/broker/internal/purchase"

// brokerTransportDir is the thin-adapter layer the fan-out moves OUT of.
const brokerTransportDir = "src/broker/internal/transport"

// brokerTransportImport is the import path internal/purchase must never name.
const brokerTransportImport = "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/" + brokerTransportDir

// purchaseSharedNames are the purchase package's declarations that are generic
// enough for a transport adapter to declare legitimately too: the constructor and
// the port names, the audit sink with its outcome type and level rule, and plain
// words. Every OTHER top-level name the purchase package declares is its own.
var purchaseSharedNames = map[string]bool{
	"New": true, "Service": true, "Execute": true, "Registry": true,
	"EndpointResolver": true, "ExchangeCaller": true,
	"audit": true, "auditMessage": true, "outcome": true, "level": true,
	"group": true, "answer": true, "send": true, "firstErrorDetail": true,
}

// purchaseOnlyNames is every top-level name (methods included) that the purchase
// package's non-test files declare, less purchaseSharedNames. It is read from the
// package itself, so a name the package gains or renames later is covered with
// no edit here, and a list kept by hand cannot go stale.
func purchaseOnlyNames(t *testing.T, root string) map[string]bool {
	t.Helper()
	files := goSourceFilesUnder(t, root, purchasePackageDir, false)
	if len(files) == 0 {
		t.Fatalf("no source files under %s; the guard would check nothing", purchasePackageDir)
	}
	names := map[string]bool{}
	for _, rel := range files {
		for name := range topLevelDeclNames(parseGoFile(t, root, rel)) {
			if !purchaseSharedNames[name] {
				names[name] = true
			}
		}
	}
	if len(names) == 0 {
		t.Fatalf("%s declares no name of its own; the guard would check nothing", purchasePackageDir)
	}
	return names
}

// goSourceFilesUnder yields every non-generated .go file under rel
// (repo-root-relative), as repo-root-relative paths. Test files are included
// only when includeTests is set: most guards here bind production code and would
// misfire on fixtures, while the ver guard has to read fixtures because that is
// where its defect lived. A missing directory yields nil without failing —
// existence is asserted separately.
func goSourceFilesUnder(t *testing.T, root, rel string, includeTests bool) []string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".go") ||
			(!includeTests && strings.HasSuffix(name, "_test.go")) ||
			strings.HasSuffix(name, ".pb.go") || strings.HasSuffix(name, "connect.go") ||
			strings.Contains(path, string(filepath.Separator)+"sqlc"+string(filepath.Separator)) {
			return nil
		}
		relPath, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(relPath))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", rel, err)
	}
	return files
}

// parseGoFile parses a repo-root-relative Go file.
func parseGoFile(t *testing.T, root, rel string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return f
}

// topLevelDeclNames collects every top-level func/method, type, const, and
// var name declared in the file. Method names are included (the pipeline
// substance is mostly methods).
func topLevelDeclNames(f *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, d := range f.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			names[decl.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					names[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, n := range s.Names {
						names[n.Name] = true
					}
				}
			}
		}
	}
	return names
}

// astReferencesIdent reports whether any identifier in the file has the
// given name. Ident-anchored (not import-alias-anchored), so an aliased
// import (nh.ResponseWriter, srv.NewWBAHandler) or a dot-import cannot
// slip it.
func astReferencesIdent(f *ast.File, name string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == name {
			found = true
			return false
		}
		return !found
	})
	return found
}

// TestPurchaseFanOutSymbolsLeftTransport fails when the transport package
// declares a name the purchase service owns (purchaseOnlyNames): the business
// half of BrokerService.ExecuteTransaction lives in internal/purchase, and
// transport keeps only Connect adapters and write sinks.
func TestPurchaseFanOutSymbolsLeftTransport(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	owned := purchaseOnlyNames(t, root)
	for _, rel := range goSourceFilesUnder(t, root, brokerTransportDir, false) {
		for sym := range topLevelDeclNames(parseGoFile(t, root, rel)) {
			if owned[sym] {
				t.Errorf("%s declares %s, which %s owns; "+
					"transport keeps only Connect adapters and write sinks",
					rel, sym, purchasePackageDir)
			}
		}
	}
}

// TestPurchasePackagePurity pins the target invariants of internal/purchase
// for every source file it gains: it never imports the transport package (the
// dependency points transport→service only) and never names
// http.ResponseWriter (the write sink is inverted to typed
// result/*broker.Error returns; transport owns every write).
func TestPurchasePackagePurity(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	files := goSourceFilesUnder(t, root, purchasePackageDir, false)
	if len(files) == 0 {
		t.Fatal("no service-layer source files found; the guard would check nothing")
	}
	for _, rel := range files {
		f := parseGoFile(t, root, rel)
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == brokerTransportImport {
				t.Errorf("%s imports the broker transport package — a service layer must never "+
					"import transport (ports are locally-owned structural interfaces; "+
					"types crossing the boundary move down)", rel)
			}
		}
		if astReferencesIdent(f, "ResponseWriter") {
			t.Errorf("%s references ResponseWriter — a service layer receives NO "+
				"http.ResponseWriter; return typed results/*broker.Error and let the "+
				"transport adapter own every write", rel)
		}
	}
}

// TestWellKnownComposeLivesBelowServices fails while either service still
// composes the well-known manifest+WBA handler pair inline (the
// server.NewHandler+server.NewWBAHandler sequence). The compose is written
// ONCE below both services — under root internal/ (a wellknownbuild
// builder, or folded into internal/forawellknown/server) — and the
// services call the builder. Anchored on NewWBAHandler: the inline compose
// always names it, the builder call never does.
func TestWellKnownComposeLivesBelowServices(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, top := range []string{"src/broker", "src/exchange"} {
		for _, rel := range goSourceFilesUnder(t, root, top, false) {
			if astReferencesIdent(parseGoFile(t, root, rel), "NewWBAHandler") {
				t.Errorf("%s composes the well-known WBA handler inline — compose "+
					"manifest+WBA once in the shared builder under root internal/ "+
					"(wellknownbuild) and call that from both services", rel)
			}
		}
	}
}

// --- meta-tests: prove the detectors themselves work -----------------------

// parseSnippet parses an in-memory Go source snippet.
func parseSnippet(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "snippet.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	return f
}

// TestMeta_ResponseWriterDetectorCatchesAliasedImport is the positive
// meta-test: an aliased net/http import must not slip the ResponseWriter
// detector (it is ident-anchored, not alias-anchored).
func TestMeta_ResponseWriterDetectorCatchesAliasedImport(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package x\n\nimport nh \"net/http\"\n\nfunc sink(w nh.ResponseWriter) {}\n")
	if !astReferencesIdent(f, "ResponseWriter") {
		t.Fatal("detector missed an aliased nh.ResponseWriter parameter — alias-slip regression")
	}
}

// TestMeta_ResponseWriterDetectorPassesTypedReturns is the negative
// meta-test: the sanctioned inverted shape (typed result/error returns,
// plain net/http request usage) must NOT match.
func TestMeta_ResponseWriterDetectorPassesTypedReturns(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package x\n\nimport \"net/http\"\n\n"+
		"func preflight(r *http.Request) ([]byte, error) { return nil, nil }\n")
	if astReferencesIdent(f, "ResponseWriter") {
		t.Fatal("detector wrongly flagged the sanctioned typed-return shape")
	}
}

// TestMeta_WBAComposeDetectorCatchesAliasedCall is the positive meta-test
// for the well-known compose detector: an aliased server import must still
// match.
func TestMeta_WBAComposeDetectorCatchesAliasedCall(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package x\n\nimport srv \"example.com/server\"\n\n"+
		"func compose() { _, _ = srv.NewWBAHandler(srv.WBAConfig{}) }\n")
	if !astReferencesIdent(f, "NewWBAHandler") {
		t.Fatal("detector missed an aliased srv.NewWBAHandler call — alias-slip regression")
	}
}

// TestMeta_WBAComposeDetectorPassesBuilderCall is the negative meta-test:
// calling the shared builder must NOT match.
func TestMeta_WBAComposeDetectorPassesBuilderCall(t *testing.T) {
	t.Parallel()
	f := parseSnippet(t, "package x\n\nimport \"example.com/wellknownbuild\"\n\n"+
		"func compose() { _, _ = wellknownbuild.Build(nil, nil, \"\") }\n")
	if astReferencesIdent(f, "NewWBAHandler") {
		t.Fatal("detector wrongly flagged the sanctioned shared-builder call")
	}
}
