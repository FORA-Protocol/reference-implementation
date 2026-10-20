//go:build integration

package transport_test

import (
	"net/http"
	"net/url"
	"testing"
)

// How the auth-flow tests read the redirects the server sends. An authorization
// response goes back to the downstream client and is read through
// clientRedirectQuery. The start of a sign-in goes on to the upstream identity
// provider instead, and startAuthorize in the fixture tells the two apart with
// sameEndpoint.

// clientRedirectQuery reads an authorization response the way the downstream
// client receives it: a 302 to flowClientURI, the redirect every client in this
// package registers (some with a query of their own appended). It requires
// exactly one iss, equal to the issuer the fixture configured. The equality is
// the check an RFC 9207 client runs before it acts on a response (RFC 9207
// §2.4). The single value is the server's own obligation, because a response
// parameter must not be included more than once (RFC 6749 §3.1). It returns the
// query for what the leg carries: approvedRedirect checks code and state, and a
// refusal test checks error. It is the one place a test parses a redirect to the
// client, so every redirect a test here reads is held to the iss check.
//
// The target is compared by scheme, host and path, not as a string prefix. A
// prefix check also accepts a different path that only starts the same way, and
// it cannot hold a registered URI that carries a query of its own, because the
// response re-encodes that query together with its own parameters.
//
// One iss is required, not merely a matching first one. A second value is what
// a registered redirect URI that pre-seeds its own iss would produce, and a
// client that reads the first value would then see the foreign issuer.
func clientRedirectQuery(t *testing.T, r flowResp) url.Values {
	t.Helper()
	if r.status != http.StatusFound {
		t.Fatalf("expected a 302 back to the client, got %d (body %s)", r.status, r.body)
	}
	loc := mustParseURL(t, r.location)
	if !sameEndpoint(loc, mustParseURL(t, flowClientURI)) {
		t.Fatalf("authorization response went to %q, want the registered redirect %q", r.location, flowClientURI)
	}
	q := loc.Query()
	if got := q["iss"]; len(got) != 1 || got[0] != flowIssuer {
		t.Fatalf("authorization response iss = %q, want exactly [%q] "+
			"(one value per RFC 6749 §3.1, the issuer per RFC 9207 §2)", got, flowIssuer)
	}
	return q
}

// sameEndpoint reports whether two URLs name the same endpoint: the same scheme,
// host and path, whatever their queries carry.
func sameEndpoint(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && a.Host == b.Host && a.Path == b.Path
}

// mustParseURL parses raw or fails the test.
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}
