// Structural guard: THIS SERVICE never writes the registration details an agent
// submits to a log line. The other half of the same promise — that the local
// note of where it registered has no column that could hold them — is the
// sibling file, split off because the two are independent rules over different
// inputs and because the linter caps a file at 500 lines.
//
// "This service" is the load-bearing part, and the promise is worded that way in
// both places it is made. What a THIRD PARTY writes is a separate question: an
// Exchange's own refusal text is logged, bounded to 300 bytes, so an Exchange
// that quotes a submitted value back into its refusal puts a short value on an
// operator line. That is out of this guard's reach by design — it reads this
// repository's source, and the echoing peer is not in it — and it is pinned by
// TestRegister_BoundsAnExchangeThatEchoesThePayloadBack instead.
//
// The rule is a promise this repository makes to an operator in ONE place — the
// migration that creates the table — and it is the kind of promise that breaks by
// addition rather than by edit. Nobody deletes it; somebody adds "fields" to a log
// line while debugging a refusal, or adds a payload column while making the hint
// list more useful, and the sentence stays there being false.
//
// ONE PLACE, and the count has now been wrong twice. It first said three, naming
// the fora_register tool description; that description says nothing about logging
// or storage, and it is the right place for that silence, because it tells an
// AGENT what to send while what an operator's own service records is an
// operator's question. The correction to two named a runbook instead, and no
// runbook makes this promise either — nothing under docs/ names this table at
// all. Counting a document that never made the promise is how a guard comes to
// believe it covers more than it does, which is why the number is now tied to a
// file a reader can open.
//
// TWO HALVES, and neither is sufficient. This file is the structural half: it
// reads the diff's own source, so a violation fails in review rather than in
// production. Its limit is stated rather than hidden — it matches the payload by
// the names the layers it reads bind it to, and by the type of the request the
// payload travels inside, so a value copied into a third variable first is
// invisible to it. The behavioural half covers exactly that: an integration test
// registers with a distinctive value and asserts it appears in no captured log
// line, on the success path and the refusal path both.
//
// The behavioural half does not make the file set a detail. It drives the paths
// a test happens to exercise, and the local refusals inside the service are
// driven by tests that use no distinctive value — so a leak added to one of those
// branches is caught by the structural half or by nothing.
package guards

import (
	"go/ast"
	"path/filepath"
	"strings"
	"testing"
)

// payloadPackages are the three packages the submitted payload is in scope in,
// and the guard reads every non-test file in each.
//
// The payload crosses three layers. The tool decodes it off the request and
// hands it on; the service holds it as a parameter, checks it against the
// protocol's bounds and the Exchange's schema, and packs it into a protobuf
// value; the outbound leg holds the built request and sends it. Each is
// somewhere a log line could be added while debugging a refusal, and the service
// is the layer with most reason to add one — it is where every local refusal is
// decided. A guard reading only the tool would pass a leak written one call
// deeper.
//
// The outbound leg was OUT of scope until the account calls moved onto the SDK
// client, and it should not have been: the payload crossed the hand-written
// outbound client before that too, inside a built RegisterRequest. It was
// invisible for the reason the header describes — the promise breaks by
// addition, and there the addition was a whole package rather than a log line.
// It is bound to a request there rather than to any of the names below, which is
// why the type rule beside them exists.
//
// PACKAGES rather than a list of files, because this promise breaks by addition.
// A named list is checked when a file is deleted — parsing fails loudly — and
// silent when one is added, which is the direction the header says the promise
// breaks in. The mcp package has already been split once; a second split that
// moved the register handler into a new file would have carried the payload out
// of a named list with nothing to say so.
var payloadPackages = []string{
	"src/identity/internal/mcp",
	"src/identity/internal/exchacct",
	"src/identity/internal/forasdk",
}

// payloadNames are what the tool and the service bind the submitted fields to:
// the tool input's own member, the service's parameter, and the protobuf value
// built from it. Each is the payload, so each is forbidden as a logging argument.
//
// Named rather than counted. The list above grew a third package and this
// sentence went on saying "those two layers", which is the kind of drift a count
// invites and a name does not -- and the third layer is not missing here anyway,
// it is covered by payloadRequestType below, which matches a TYPE rather than a
// name.
//
// Both spellings of the name are here on purpose. The tool binds the exported
// member and the service binds a lowercase parameter, so a set carrying one of
// them can only catch a leak in one of the files.
var payloadNames = map[string]bool{"Fields": true, "fields": true, "data": true}

// payloadRequestType is the message the payload travels inside once it has been
// packed and addressed: RegisterRequest carries it as registration_data.
//
// Matched by TYPE and not by name, and the difference is the whole point. The
// outbound leg binds it to `req`, which is a name a dozen unrelated things in
// these packages are also called — adding it to payloadNames above would make
// the guard fire on requests that carry no payload at all, and a guard that
// cries wolf gets the offending line rewritten to dodge it rather than removed.
// The type is unambiguous, and a parameter's declared type is readable straight
// off the syntax tree without resolving anything.
const payloadRequestType = "RegisterRequest"

// logMethods are the slog calls a leak would travel through. With and With Group
// are included because an attribute attached there reaches every later line from
// that logger, which is the version of this mistake that leaks the most.
var logMethods = map[string]bool{
	"InfoContext": true, "WarnContext": true, "ErrorContext": true, "DebugContext": true,
	"Info": true, "Warn": true, "Error": true, "Debug": true,
	"With": true, "WithGroup": true, "Log": true, "LogAttrs": true,
}

// TestSubmittedRegistrationFieldsAreNeverLogged fails when the register payload
// is passed to a logging call.
func TestSubmittedRegistrationFieldsAreNeverLogged(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, path := range payloadPackageFiles(t, root) {
		f := parseGoFile(t, root, path)
		for _, name := range loggedPayloadNames(f) {
			t.Errorf("%s passes the submitted registration fields (%s) to a logging call — "+
				"they are the operator's business details, and this service neither logs nor "+
				"stores them. Log the exchange and the outcome instead.", path, name)
		}
	}
}

// payloadPackageFiles lists every non-test Go file in payloadPackages, as
// repo-root-relative paths.
//
// It fails when a package yields nothing. A glob that matches no file is not an
// error to filepath.Glob, so a package renamed or moved would otherwise turn
// this guard into a loop over an empty list that reports nothing and passes.
func payloadPackageFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	for _, pkg := range payloadPackages {
		matches, err := filepath.Glob(filepath.Join(root, pkg, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", pkg, err)
		}
		found := 0
		for _, abs := range matches {
			if strings.HasSuffix(abs, "_test.go") {
				continue
			}
			files = append(files, filepath.Join(pkg, filepath.Base(abs)))
			found++
		}
		if found == 0 {
			t.Fatalf("%s holds no non-test Go file — the guard is reading nowhere; "+
				"the package moved and payloadPackages was not updated with it", pkg)
		}
	}
	return files
}

// loggedPayloadNames returns the payload names f hands to a logging call.
//
// It matches an argument that IS one of the payload names, or a selector ending
// in one (in.Fields). A name reached through a third variable is not matched,
// which is the limit the file header names and the behavioural test covers.
func loggedPayloadNames(f *ast.File) []string {
	found := make([]string, 0, len(f.Decls))
	for _, decl := range f.Decls {
		// EVERY top-level declaration, not only the functions. A slog call can sit
		// in a package-level var initialiser or in a function literal assigned to
		// one, and a walk that skipped those was blind to a whole shape of leak --
		// on a promise whose own header says it breaks by ADDITION rather than by
		// edit.
		//
		// Names bound to a RegisterRequest come from a function's parameters, and
		// a declaration has them only if it IS a function. A literal inside it has
		// its own, which loggedIn picks up on the way down. Scoped per declaration
		// rather than per file so a `req` in the next function over is not tarred
		// by this one.
		var carriers map[string]bool
		if fn, ok := decl.(*ast.FuncDecl); ok {
			carriers = requestCarriers(fn.Type)
		}
		found = append(found, loggedIn(decl, carriers)...)
	}
	return found
}

// loggedIn walks n and returns the payload names handed to a logging call,
// tracking which names are bound to a registration request as it descends.
//
// A function literal declares parameters the same way a declaration does --
// ast.FuncLit carries a FuncType and a FuncType carries Params -- so entering one
// widens the set for its body and only its body. Go scoping is why the sets are
// unioned rather than replaced: a literal sees its enclosing function's
// parameters too.
//
// The literal's body is walked by the recursive call, so the outer walk stops
// there; without that it would visit the same calls again under the narrower set.
func loggedIn(n ast.Node, carriers map[string]bool) []string {
	var found []string
	ast.Inspect(n, func(node ast.Node) bool {
		if lit, ok := node.(*ast.FuncLit); ok {
			found = append(found, loggedIn(lit.Body, withCarriers(carriers, lit.Type))...)
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !logMethods[sel.Sel.Name] {
			return true
		}
		for _, arg := range call.Args {
			if name := payloadNameOf(arg, carriers); name != "" {
				found = append(found, name)
			}
		}
		return true
	})
	return found
}

// withCarriers returns outer widened by the request parameters fn declares,
// without mutating outer -- a sibling literal must not inherit this one's names.
func withCarriers(outer map[string]bool, fn *ast.FuncType) map[string]bool {
	own := requestCarriers(fn)
	if len(own) == 0 {
		return outer
	}
	merged := make(map[string]bool, len(outer)+len(own))
	for name := range outer {
		merged[name] = true
	}
	for name := range own {
		merged[name] = true
	}
	return merged
}

// requestCarriers returns the parameter names fn declares as a registration
// request, so the payload inside one is watched under whatever the author called
// it.
//
// It takes a FuncType rather than a FuncDecl because a function LITERAL declares
// parameters through the same node, and reading only declarations left a whole
// shape of leak unwatched -- a package-level handler, or a literal handed to a
// helper, either of which carries the payload under a name the spelled list does
// not hold.
//
// Parameters only. A request built inside a function body is bound by an
// assignment this does not read, which is the same limit payloadNameOf already
// states for a value copied into a third variable: the behavioural half of this
// guard is what covers it.
func requestCarriers(fn *ast.FuncType) map[string]bool {
	carriers := map[string]bool{}
	if fn.Params == nil {
		return carriers
	}
	for _, field := range fn.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != payloadRequestType {
			continue
		}
		for _, name := range field.Names {
			carriers[name.Name] = true
		}
	}
	return carriers
}

// payloadNameOf reports the payload name an expression denotes, or "".
//
// carriers are the names this function binds a registration request to, and they
// are payload for the same reason the spelled names are: the submitted fields
// travel inside one as registration_data.
func payloadNameOf(expr ast.Expr, carriers map[string]bool) string {
	switch e := expr.(type) {
	case *ast.Ident:
		if payloadNames[e.Name] || carriers[e.Name] {
			return e.Name
		}
	case *ast.SelectorExpr:
		if payloadNames[e.Sel.Name] {
			return e.Sel.Name
		}
		// req.RegistrationData and any other member reached off a request are
		// the payload as much as the request is.
		if ident, ok := e.X.(*ast.Ident); ok && carriers[ident.Name] {
			return ident.Name + "." + e.Sel.Name
		}
	}
	return ""
}

// TestMeta_PayloadLogDetectorFires is the positive meta-test: each shape the
// detector claims to catch must be caught.
func TestMeta_PayloadLogDetectorFires(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"the tool input's member":   `log.InfoContext(ctx, "registered", "fields", in.Fields)`,
		"the service's parameter":   `log.WarnContext(ctx, "refused", "fields", fields)`,
		"the protobuf value":        `log.InfoContext(ctx, "registered", "payload", data)`,
		"attached to the logger":    `log.With("fields", in.Fields).Info("registered")`,
		"a plain slog call":         `log.Warn("refused", "fields", in.Fields)`,
		"the parameter on a logger": `log.With("fields", fields).Info("registered")`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := parseSnippet(t, "package x\n\nfunc leak() { "+body+" }\n")
			if got := loggedPayloadNames(f); len(got) == 0 {
				t.Fatalf("detector passed a leak: %s", body)
			}
		})
	}
}

// TestMeta_PayloadLogDetectorSeesOutsideAFunctionBody is the meta-test the other
// five could not be.
//
// Every snippet they parse is wrapped in a func declaration, so all of them pass
// against a detector that walks only functions -- which is exactly what this one
// did until it was widened. A package-level initialiser is where the gap was:
// slog is happy to be called from one, the promise this file guards breaks by
// ADDITION, and adding a var is an addition no reviewer reads as a logging
// change.
//
// Two shapes, because the first is the obvious one and the second is how it
// actually tends to appear -- a logger built once at package scope with fields
// already attached, which then leaks on every later line from that logger.
func TestMeta_PayloadLogDetectorSeesOutsideAFunctionBody(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a package-level logger with the payload attached": `var logger = base.With("fields", fields)`,
		"a function literal assigned at package scope": `var report = func() { ` +
			`slog.Info("registered", "fields", fields) }`,
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := parseSnippet(t, "package x\n\n"+decl+"\n")
			if got := loggedPayloadNames(f); len(got) == 0 {
				t.Fatalf("detector passed a leak outside any function body: %s", decl)
			}
		})
	}
}

// TestMeta_PayloadLogDetectorFiresOnARequestCarrier is the positive meta-test
// for the type rule, which is the half the spelled names cannot cover.
//
// The outbound leg holds the payload inside a built request under whatever name
// its author chose, so the two shapes here are the whole request logged and one
// member reached off it. Both are the submitted fields.
func TestMeta_PayloadLogDetectorFiresOnARequestCarrier(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"the whole request":      `log.InfoContext(ctx, "sending", "req", req)`,
		"a member off it":        `log.WarnContext(ctx, "refused", "payload", req.RegistrationData)`,
		"attached to the logger": `log.With("req", req).Info("sending")`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := parseSnippet(t, "package x\n\nfunc leak(ctx context.Context, req *forav1.RegisterRequest) { "+
				body+" }\n")
			if got := loggedPayloadNames(f); len(got) == 0 {
				t.Fatalf("detector passed a leak: %s", body)
			}
		})
	}
}

// TestMeta_PayloadLogDetectorFiresOnALiteralsOwnCarrier is the case the carrier
// rule could not see.
//
// A function literal declares parameters exactly the way a function declaration
// does -- ast.FuncLit carries a FuncType, and a FuncType carries Params. The
// detector read them off *ast.FuncDecl only, so a literal's own request
// parameter was not a carrier and the payload inside it went unwatched. Both
// shapes below are ordinary Go and neither reads as a logging change in review:
// a package-level handler, and a literal passed straight to a helper.
//
// The other carrier meta-test cannot stand in for this. Its snippets are wrapped
// in a func DECLARATION, so they pass whether or not literals are read.
func TestMeta_PayloadLogDetectorFiresOnALiteralsOwnCarrier(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"a package-level handler": `var logRegister = func(ctx context.Context, ` +
			`req *forav1.RegisterRequest) { slog.InfoContext(ctx, "sending", "req", req) }`,
		"a literal handed to a helper": `func send() { with(func(req *forav1.RegisterRequest) { ` +
			`slog.Info("sending", "payload", req.RegistrationData) }) }`,
	}
	for name, decl := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := parseSnippet(t, "package x\n\n"+decl+"\n")
			if got := loggedPayloadNames(f); len(got) == 0 {
				t.Fatalf("detector passed a payload logged from a literal's own parameter: %s", decl)
			}
		})
	}
}

// TestMeta_PayloadLogDetectorIgnoresAnUnrelatedReq is the negative that makes
// the type rule worth having rather than a wider name list.
//
// `req` names something in most of these packages, and almost none of them carry
// a registration payload. A guard that fired on the name would object to lines
// that leak nothing, and a guard that cries wolf gets the line rewritten to dodge
// it rather than removed.
func TestMeta_PayloadLogDetectorIgnoresAnUnrelatedReq(t *testing.T) {
	t.Parallel()
	clean := `log.InfoContext(ctx, "status", "exchange", req.GetExchange())`
	f := parseSnippet(t, "package x\n\nfunc fine(ctx context.Context, "+
		"req *forav1.GetAccountStatusRequest) { "+clean+" }\n")
	if got := loggedPayloadNames(f); len(got) != 0 {
		t.Fatalf("detector objected to %v on a request that carries no registration payload", got)
	}
}

// TestMeta_PayloadLogDetectorPassesTheLinesWeWant is the negative meta-test. The
// register path logs the Exchange and the outcome on purpose, and a guard that
// objected to those would be removed rather than obeyed.
func TestMeta_PayloadLogDetectorPassesTheLinesWeWant(t *testing.T) {
	t.Parallel()
	clean := `log.InfoContext(ctx, "identity.mcp.register", "subdomain", who.subdomain, ` +
		`"exchange", in.Exchange, "active", resp.GetActive())`
	f := parseSnippet(t, "package x\n\nfunc fine() { "+clean+" }\n")
	if got := loggedPayloadNames(f); len(got) != 0 {
		t.Fatalf("detector objected to %v on a line carrying only the routing and the outcome", got)
	}
}

// TestMeta_PayloadLogDetectorIgnoresNonLoggingCalls pins the other half of the
// negative: the payload is PASSED AROUND on this path — to the bounds check, to
// the validator, onto the request — and none of that is a leak.
func TestMeta_PayloadLogDetectorIgnoresNonLoggingCalls(t *testing.T) {
	t.Parallel()
	used := `helpers.CheckRegistrationDataStruct(in.Data); schema.Validate(in.Fields); send(data)`
	f := parseSnippet(t, "package x\n\nfunc uses() { "+used+" }\n")
	if got := loggedPayloadNames(f); len(got) != 0 {
		t.Fatalf("detector objected to %v where the payload is used rather than logged", got)
	}
}
