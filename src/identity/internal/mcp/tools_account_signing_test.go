//go:build integration

package mcp_test

import (
	"strings"
	"testing"
)

// TestRegister_IsSignedWhenTheEndpointCarriesAPathPrefix drives the one endpoint
// shape that once took the account calls out of the signed set.
//
// The hand-written leg picked its signing profile from the request path, and its
// rule read the path's PREFIX. A bare origin gives
// "/fora.v1.ExchangeService/Register" and the request was signed. An Exchange
// advertising ".../api" gives "/api/fora.v1.ExchangeService/Register", which that
// rule did not claim, so the registration carrying the operator's legal entity
// and tax identifiers left unsigned and the visible symptom was an Exchange
// behind a path prefix that could never be registered at.
//
// Such an endpoint is conformant. The endpoint rule constrains host, port and
// userinfo and says nothing about a path, and every other case in this package
// resolves to a bare httptest URL with no path at all — so none of them can see
// this.
//
// The property survived the move to the SDK, and it survives for a different
// reason than before: nothing selects by path any more, because the SDK's signing
// transport signs every bodied request when no predicate is set and this client
// sets none. That is a property of the pinned module rather than of anything in
// this tree, which is exactly why it needs a test here. A re-pin that introduced
// a path predicate would send this registration unsigned and every other test in
// the repository would still pass.
//
// The assertion is the peer's recorded call, not a header read off the request.
// Calls are appended from the middleware's OnVerified hook, so an entry exists
// only if the production RFC 9421 verifier accepted the signature — which covers
// Signature-Input and Signature together, and covers them the way a real Exchange
// would rather than the way a test remembered to.
func TestRegister_IsSignedWhenTheEndpointCarriesAPathPrefix(t *testing.T) {
	f := newFixture(t)
	a := f.provision(t, "dev-one")
	session := f.connect(t, a.Token)

	// The manifest advertises the peer's own origin plus a path. The peer serves
	// its ExchangeService there as well as at the bare path, so this changes
	// where the call lands without changing who answers it.
	prefixed := f.exchange.URL() + "/api"
	f.exchange.setManifestEndpoint(prefixed)

	out := callTool[registerResult](t, session, "fora_register",
		registerArgsAt(f.exchange.Domain(t)))
	if out.Exchange != f.exchange.Domain(t) {
		t.Fatalf("register answered for %q, want the peer's own domain", out.Exchange)
	}

	calls := f.exchange.Calls()
	if len(calls) != 1 {
		t.Fatalf("the peer verified %d calls, want 1; a registration that arrived "+
			"unsigned is never recorded, because the record is written after the "+
			"signature passes", len(calls))
	}
	if !strings.HasPrefix(calls[0].Path, "/api/fora.") {
		t.Errorf("the verified call landed at %q, so the prefixed endpoint was not "+
			"the one dialled and this test proves nothing about it", calls[0].Path)
	}
	if calls[0].KeyID == "" {
		t.Error("the verified call carries no keyid, so nothing ties the registration " +
			"to the agent that asked for it")
	}
	assertNoBearerLeaked(t, f.exchange)
}
