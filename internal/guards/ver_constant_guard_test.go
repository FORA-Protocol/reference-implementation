// Third half of the FORA protocol-version guards -- the one the other two, and
// every round-trip assertion, are blind to.
//
// The sibling file bans a bare literal and a missing field. Neither says anything
// about WHICH constant a builder stamps, and the case that matters shares a value
// with the right one: internal/forawellknown.Version reads "1.0" today, exactly
// like helpers.ProtocolVersion. Stamp the manifest-schema version on an RPC
// envelope and the bytes are identical, so the integration assertions pass, the
// literal check passes, and the omission check passes. It was verified to pass
// all of them before this check existed.
//
// It is not harmless. The contract -- github.com/FORA-Protocol/protocol, pinned in
// the root go.mod -- keeps the two as separate namespaces, says they must not be
// coupled, and enforces them in opposite directions. The /.well-known/fora.json
// document version is a rejection gate: a reader accepts a recognised MAJOR
// whatever the MINOR, and refuses an unrecognised MAJOR, a value that is not
// MAJOR.MINOR, and an absent one. That document sits at a fixed path and is read
// before any signature is checked, so a layout the reader cannot classify must not
// supply the endpoint a signed call is then sent to. The envelope version is the
// reverse: advisory, never a rejection gate, and a receiver that checks it MUST NOT
// reject an unrecognised minor. They agree today and would silently diverge the day
// either moves, with nothing failing at the moment the coupling was introduced.
//
// That describes the CONTRACT, and this tree implements it rather than diverging
// from it.
//
// An earlier version of this paragraph said otherwise -- that internal/forawellknown
// pinned ver with a schema const, refusing a conformant "1.1" document the
// contract's rule accepts -- and told the next reader to treat that divergence as
// live and unexplained. There is nothing to treat. The embedded manifest schema
// types ver as a plain required string and carries no const at all; one there
// would fail the check that keeps that schema to wire shape, which lists const
// among the value-level keywords it forbids. The major-version rule runs in code
// instead, through the SDK's own receive-side check, so a recognised major is
// accepted whatever its minor and there is ONE implementation of the rule rather
// than a schema pattern and a code path that can drift apart.
//
// Per ADR-025 D3, which settled it: exact matching is the REJECTED alternative
// there, because it refuses the additive minor revisions the version scheme
// promises will keep working. This is named here only so the header is not read
// as a description of a strictness this tree does not have.
//
// The coupling has two directions and this check reads both. An envelope stamped
// from the manifest is caught by requiring an echo's receiver to be an envelope
// message the enclosing function actually declares -- a bare `X.GetVer()` sanctions
// whatever X happens to be, and a manifest is exactly what would be in scope on the
// paths that fetch one. A manifest stamped from helpers.ProtocolVersion is caught by
// holding manifest constructions to forawellknown.Version, under both spellings:
// the generated forav1.WellKnownManifest and the forawellknown.Manifest alias most
// callers use. Only the second spelling is common in this tree, and the alias
// qualifier is not a generated package, so a check scoped to the envelope message
// set could not see it at all.
//
// Only source can see any of this, which is why it is a structural check.
package guards

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"testing"
)

// forawellknownPath is the package that owns the manifest document-schema version
// and the Manifest alias for the generated message.
const forawellknownPath = "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"

// manifestAlias is what forawellknown re-exports forav1.WellKnownManifest as. Most
// constructions in this tree use it rather than the generated name.
const manifestAlias = "Manifest"

// manifestVersionConst is the only value a manifest may stamp.
const manifestVersionConst = "Version"

// verScope is what one file supplies to the value check: which identifiers resolve
// to which package, which message names carry an envelope `ver`, and the file's own
// package, so a construction inside forawellknown may name its constant unqualified.
type verScope struct {
	proto     map[string]bool
	manifest  map[string]bool
	messages  map[string]bool
	inPackage string
}

// verConstantOffence is one `Ver:` key set from a value its namespace does not own.
type verConstantOffence struct {
	Pos      token.Pos
	Value    string
	Manifest bool
}

// manifestQualifiers maps each identifier in file that refers to forawellknown
// onto true, resolving whatever alias the file chose.
func manifestQualifiers(file *ast.File) map[string]bool {
	return qualifiersFor(file, forawellknownPath)
}

// manifestSelector reports whether expr names the well-known manifest, under
// either spelling: the generated forav1.WellKnownManifest or the forawellknown.Manifest
// alias.
func manifestSelector(expr ast.Expr, sc verScope) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return (sc.proto[pkg.Name] && sel.Sel.Name == manifestMessage) ||
		(sc.manifest[pkg.Name] && sel.Sel.Name == manifestAlias)
}

// sanctionedManifestValue reports whether v is forawellknown.Version -- qualified,
// or bare inside the package that declares it.
func sanctionedManifestValue(v ast.Expr, sc verScope) bool {
	switch e := v.(type) {
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		return ok && sc.manifest[pkg.Name] && e.Sel.Name == manifestVersionConst
	case *ast.Ident:
		return sc.inPackage == "forawellknown" && e.Name == manifestVersionConst
	}
	return false
}

// sanctionedVerValue reports whether an envelope `Ver:` value is one this project
// may stamp: the SDK constant, or an echo relaying an inbound peer's own version.
// Everything else -- another package's constant, a local one, a concatenation --
// is a second owner of a value the protocol repo owns.
//
// The echo arm checks the RECEIVER, not just the method name. `GetVer` is generated
// on every ver-bearing message including the manifest, so sanctioning the call by
// name alone lets `Ver: manifest.GetVer()` couple the two namespaces on an envelope.
// envelopes holds the names the enclosing function declares as envelope messages;
// anything else relaying a version is not an echo of the caller's own contract.
func sanctionedVerValue(v ast.Expr, envelopes map[string]bool) bool {
	switch e := v.(type) {
	case *ast.SelectorExpr: // helpers.ProtocolVersion
		pkg, ok := e.X.(*ast.Ident)
		return ok && pkg.Name == "helpers" && e.Sel.Name == "ProtocolVersion"
	case *ast.CallExpr: // req.GetVer() — relaying a peer's own version
		fn, ok := e.Fun.(*ast.SelectorExpr)
		if !ok || fn.Sel.Name != "GetVer" {
			return false
		}
		recv, ok := fn.X.(*ast.Ident)
		return ok && envelopes[recv.Name]
	}
	return false
}

// envelopeType reports whether expr is a (possibly pointer) reference to a
// ver-bearing envelope message.
func envelopeType(expr ast.Expr, sc verScope) bool {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	_, ok := verBearingSelector(expr, sc.proto, sc.messages)
	return ok
}

// envelopeTypedNames returns the identifiers fn declares whose type is a ver-bearing
// envelope message: its receiver, its parameters, and any local `var x forav1.X`.
// Those are the only receivers an echo may legitimately read a version from.
func envelopeTypedNames(fn *ast.FuncDecl, sc verScope) map[string]bool {
	out := map[string]bool{}
	fields := []*ast.Field{}
	if fn.Recv != nil {
		fields = append(fields, fn.Recv.List...)
	}
	if fn.Type.Params != nil {
		fields = append(fields, fn.Type.Params.List...)
	}
	for _, f := range fields {
		if !envelopeType(f.Type, sc) {
			continue
		}
		for _, name := range f.Names {
			out[name.Name] = true
		}
	}
	ast.Inspect(fn, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || spec.Type == nil || !envelopeType(spec.Type, sc) {
			return true
		}
		for _, name := range spec.Names {
			out[name.Name] = true
		}
		return true
	})
	return out
}

// verValueOf returns the expression a composite literal binds to Ver, and whether
// the key is present at all. A quoted string is reported as absent: that edit
// belongs to the literal check, so one mistake is never reported twice.
func verValueOf(lit *ast.CompositeLit) (ast.Expr, bool) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Ver" {
			continue
		}
		if basic, isLit := kv.Value.(*ast.BasicLit); isLit && basic.Kind == token.STRING {
			return nil, false
		}
		return kv.Value, true
	}
	return nil, false
}

// verOffencesIn reports every `Ver:` key inside node whose value its namespace does
// not own. envelopes names the identifiers an echo may read from, which is empty
// outside a function body.
func verOffencesIn(node ast.Node, sc verScope, envelopes map[string]bool) []verConstantOffence {
	var out []verConstantOffence
	ast.Inspect(node, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		isManifest := manifestSelector(lit.Type, sc)
		_, isEnvelope := verBearingSelector(lit.Type, sc.proto, sc.messages)
		if !isManifest && !isEnvelope {
			return true
		}
		value, present := verValueOf(lit)
		if !present {
			return true
		}
		sanctioned := sanctionedVerValue(value, envelopes)
		if isManifest {
			sanctioned = sanctionedManifestValue(value, sc)
		}
		if !sanctioned {
			out = append(out, verConstantOffence{
				Pos: value.Pos(), Value: types.ExprString(value), Manifest: isManifest,
			})
		}
		return true
	})
	return out
}

// verWrongConstantSites returns every `Ver:` key on a message this guard owns whose
// value belongs to the other namespace, or to no owner at all.
func verWrongConstantSites(file *ast.File, messages map[string]bool) []verConstantOffence {
	sc := verScope{
		proto:     protoQualifiers(file),
		manifest:  manifestQualifiers(file),
		messages:  messages,
		inPackage: file.Name.Name,
	}
	if len(sc.proto) == 0 && len(sc.manifest) == 0 {
		return nil
	}
	var out []verConstantOffence
	for _, decl := range file.Decls {
		envelopes := map[string]bool{}
		if fn, ok := decl.(*ast.FuncDecl); ok {
			envelopes = envelopeTypedNames(fn, sc)
		}
		out = append(out, verOffencesIn(decl, sc, envelopes)...)
	}
	return out
}

// TestVerStampsTheSDKConstant fails when a builder sets a version from the wrong
// namespace's constant: an envelope from anything but helpers.ProtocolVersion or an
// echo of an inbound envelope, or a manifest from anything but forawellknown.Version.
func TestVerStampsTheSDKConstant(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	messages := verBearingMessages(t)
	fset := token.NewFileSet()

	var envelopes, manifests []string
	for _, rel := range appSourceAndTestFiles(t, root) {
		if _, exempt := literalAllowlist[rel]; exempt {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		for _, site := range verWrongConstantSites(file, messages) {
			line := fmt.Sprintf("%s:%d Ver: %s", rel, fset.Position(site.Pos).Line, site.Value)
			if site.Manifest {
				manifests = append(manifests, line)
			} else {
				envelopes = append(envelopes, line)
			}
		}
	}
	if len(envelopes) > 0 {
		t.Errorf("builder(s) stamp an RPC envelope Ver from something other than "+
			"helpers.ProtocolVersion: %v — the envelope version has one owner, and the "+
			"/.well-known/fora.json document version is a separate namespace the proto "+
			"says must not be coupled to it", envelopes)
	}
	if len(manifests) > 0 {
		t.Errorf("builder(s) stamp a well-known manifest Ver from something other than "+
			"forawellknown.Version: %v — the manifest versions the document SCHEMA, and "+
			"the RPC envelope version is a separate namespace; the two read the same "+
			"value today and would diverge silently the day either moves", manifests)
	}
}

// metaSrc wraps a `Ver:` field in a minimal envelope-message construction, so
// each case below states only the value under test.
func metaSrc(field string) string {
	return "package p\n" +
		"import forav1 \"github.com/FORA-Protocol/protocol/gen/go/fora/v1\"\n" +
		"var _ = &forav1.UsageReport{" + field + "}"
}

// metaEchoSrc wraps the same construction in a function taking recvType, so the
// echo arm can be exercised against a receiver whose type the file declares.
func metaEchoSrc(recvType, field string) string {
	return "package p\n" +
		"import forav1 \"github.com/FORA-Protocol/protocol/gen/go/fora/v1\"\n" +
		"import \"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown\"\n" +
		"func f(src " + recvType + ") { _ = &forav1.UsageReport{" + field + "} }"
}

// metaManifestSrc constructs a manifest under the given spelling.
func metaManifestSrc(typeName, field string) string {
	return "package p\n" +
		"import forav1 \"github.com/FORA-Protocol/protocol/gen/go/fora/v1\"\n" +
		"import \"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown\"\n" +
		"var _ = &" + typeName + "{" + field + "}"
}

// TestVerConstantMatcher_MetaTests pins the matcher, including the two crossings
// that motivate the whole check: a constant from the wrong namespace carrying the
// right value, in each direction.
func TestVerConstantMatcher_MetaTests(t *testing.T) {
	t.Parallel()
	messages := verBearingMessages(t)
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"negative_sdk_constant", metaSrc(`Ver: helpers.ProtocolVersion`), 0},
		{"negative_literal_belongs_to_the_other_check", metaSrc(`Ver: "1.0"`), 0},
		{"positive_wrong_namespace_same_value", metaSrc(`Ver: forawellknown.Version`), 1},
		{"positive_local_constant", metaSrc(`Ver: myVer`), 1},
		{"positive_concatenation", metaSrc(`Ver: helpers.ProtocolVersion + "-dev"`), 1},

		// The echo arm. A request parameter is the shape both production echo sites
		// have; a manifest parameter is the crossing the receiver check exists for.
		{
			"negative_echo_from_a_request_parameter",
			metaEchoSrc("*forav1.TransactionRequest", `Ver: src.GetVer()`), 0,
		},
		{
			"positive_echo_from_a_manifest_parameter",
			metaEchoSrc("*forawellknown.Manifest", `Ver: src.GetVer()`), 1,
		},
		{
			"positive_echo_from_an_undeclared_receiver",
			metaSrc(`Ver: req.GetVer()`), 1,
		},

		// The manifest direction, under both spellings.
		{
			"negative_manifest_keeps_its_own_constant",
			metaManifestSrc("forav1.WellKnownManifest", `Ver: forawellknown.Version`), 0,
		},
		{
			"negative_manifest_alias_keeps_its_own_constant",
			metaManifestSrc("forawellknown.Manifest", `Ver: forawellknown.Version`), 0,
		},
		{
			"positive_manifest_stamped_from_the_sdk_constant",
			metaManifestSrc("forav1.WellKnownManifest", `Ver: helpers.ProtocolVersion`), 1,
		},
		{
			"positive_manifest_alias_stamped_from_the_sdk_constant",
			metaManifestSrc("forawellknown.Manifest", `Ver: helpers.ProtocolVersion`), 1,
		},
		{
			"negative_manifest_literal_belongs_to_the_other_check",
			metaManifestSrc("forawellknown.Manifest", `Ver: "1.0"`), 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := len(verWrongConstantSites(parseSnippet(t, c.src), messages)); got != c.want {
				t.Fatalf("wrong-constant matches = %d, want %d", got, c.want)
			}
		})
	}
}
