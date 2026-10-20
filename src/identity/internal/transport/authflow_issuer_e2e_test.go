//go:build integration

package transport_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/oauthserver"
)

// The authorization server names itself on every authorization response (RFC
// 9207). A client that reads authorization_response_iss_parameter_supported
// from the metadata document rejects any authorization response without iss,
// and it compares a present iss with the issuer it recorded from that document,
// character for character. These tests drive both halves through the public
// routes. Each refusal redirect is checked where its own test drives it,
// through clientRedirectQuery.

// authServerMetadata is the part of the RFC 8414 document these tests read. The
// flag decodes into a bool, so a quoted "true" fails to decode instead of
// passing as a truthy string, and an absent flag reads as false.
type authServerMetadata struct {
	Issuer                string `json:"issuer"`
	IssParameterSupported bool   `json:"authorization_response_iss_parameter_supported"`
}

// metadata fetches the authorization-server metadata document from its public
// route, as a client does before it starts an authorization request.
func (f *authFixture) metadata(t *testing.T) authServerMetadata {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, f.srv.URL+oauthserver.MetadataPath, nil)
	r := f.do(t, req)
	if r.status != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", oauthserver.MetadataPath, r.status)
	}
	var md authServerMetadata
	if err := json.Unmarshal(r.body, &md); err != nil {
		t.Fatalf("decode metadata %s: %v", r.body, err)
	}
	return md
}

// TestAuthFlow_MetadataAdvertisesIssParameter holds the metadata document to the
// part of RFC 9207 a client reads before it redirects anyone: the issuer it will
// record, and the flag that makes it reject a response without iss.
func TestAuthFlow_MetadataAdvertisesIssParameter(t *testing.T) {
	f := newAuthFixture(t)
	md := f.metadata(t)
	if md.Issuer != flowIssuer {
		t.Errorf("metadata issuer = %q, want the configured issuer %q", md.Issuer, flowIssuer)
	}
	if !md.IssParameterSupported {
		t.Error("metadata does not advertise authorization_response_iss_parameter_supported: true")
	}
}

// TestAuthFlow_ApprovedRedirectIdentifiesTheIssuer drives an approval the way a
// client that follows the MCP authorization spec does. It records the issuer
// from the metadata document, runs the flow to an approved consent, and compares
// the iss on the redirect with the recorded issuer before it redeems the code.
//
// Every leg goes through a public route: metadata, register, authorize,
// callback, consent, the redirect back to the client, and token. The upstream
// identity provider is the fake at the oidcup port, as everywhere on this tier.
func TestAuthFlow_ApprovedRedirectIdentifiesTheIssuer(t *testing.T) {
	f := newAuthFixture(t)
	recorded := f.metadata(t).Issuer
	clientID, verifier := f.driveToConsent(t)

	q := f.approvedRedirect(t)
	if got := q.Get("iss"); got != recorded {
		t.Fatalf("iss = %q, want the issuer the metadata document advertised (%q)", got, recorded)
	}
	// A response the client accepted must still be one it can use.
	if tok := f.exchangeToken(t, q.Get("code"), verifier, clientID); tok.status != http.StatusOK {
		t.Fatalf("token exchange = %d body %s, want 200", tok.status, tok.body)
	}
}

// TestAuthFlow_RegisteredRedirectCannotSeedIss registers a redirect URI that
// already carries an iss naming another authorization server. Client
// registration is open, so anyone can do this. If the server repeated that
// value, a self-registered client could make this server claim to be a
// different authorization server, and iss exists to prove exactly which server
// answered. A refusal and an approval must each carry exactly one iss, and it
// must be this server's.
//
// The URI also carries a parameter of its own, keep, which must survive. RFC 6749
// §3.1.2 requires the server to keep the registered query when it adds its
// parameters, so replacing iss must not be done by dropping that query.
func TestAuthFlow_RegisteredRedirectCannotSeedIss(t *testing.T) {
	const foreignIssuer = "https://other-as.example"
	seeded := flowClientURI + "?keep=1&iss=" + url.QueryEscape(foreignIssuer)
	f := newAuthFixture(t)
	requireKept := func(t *testing.T, q url.Values) {
		t.Helper()
		if got := q.Get("keep"); got != "1" {
			t.Errorf("registered query parameter keep = %q, want \"1\" kept (RFC 6749 §3.1.2)", got)
		}
	}

	t.Run("refusal", func(t *testing.T) {
		clientID := f.register(t, seeded)
		q := clientRedirectQuery(t, f.getAuthorize(t, url.Values{
			"response_type": {"token"}, "client_id": {clientID},
			"redirect_uri": {seeded}, "state": {flowState},
		}))
		if got := q.Get("error"); got != "unsupported_response_type" {
			t.Errorf("error = %q, want unsupported_response_type", got)
		}
		requireKept(t, q)
	})
	t.Run("approval", func(t *testing.T) {
		f.driveToConsentVia(t, seeded)
		requireKept(t, f.approvedRedirect(t))
	})
}
