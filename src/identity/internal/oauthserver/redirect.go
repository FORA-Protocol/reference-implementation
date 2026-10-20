package oauthserver

import (
	"net/http"
	"net/url"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/identity/internal/oauth"
)

// validRedirectURI reports whether raw is a redirect target this server will
// register: an https URL, or an http URL on a loopback host (the shape MCP clients
// like Claude Code use for their local listener). Anything else — a custom scheme, a
// non-loopback http host, a URL carrying a fragment — is refused, which is the first
// line of defence against an open redirect.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return u.Host != ""
	case "http":
		switch u.Hostname() {
		case "127.0.0.1", "::1", "localhost":
			return true
		}
	}
	return false
}

// allowedRedirect reports whether uri exactly matches one of the client's registered
// redirect URIs. Exact-match (not prefix) is deliberate — a prefix rule is an open
// redirect waiting to happen.
func allowedRedirect(client oauth.Client, uri string) bool {
	for _, registered := range client.RedirectURIs {
		if registered == uri {
			return true
		}
	}
	return false
}

// redirectSuccess sends the browser back to the client with the authorization code
// (and the client's state, when it supplied one). redirectWith adds the issuer.
func (s *Server) redirectSuccess(w http.ResponseWriter, r *http.Request, base, code, state string) {
	params := url.Values{"code": {code}}
	if state != "" {
		params.Set("state", state)
	}
	s.redirectWith(w, r, base, params)
}

// oauthErr is an RFC 6749 error pair (a code and an optional human description)
// returned to a client via a redirect. Bundling the two keeps redirectError within
// the argument-count limit and makes the call sites read as one error value.
type oauthErr struct {
	code string
	desc string
}

// redirectError sends the browser back to the client with an OAuth error (RFC 6749
// §4.1.2.1), used once the redirect target is trusted. redirectWith adds the issuer.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, base, state string, oe oauthErr) {
	params := url.Values{"error": {oe.code}}
	if oe.desc != "" {
		params.Set("error_description", oe.desc)
	}
	if state != "" {
		params.Set("state", state)
	}
	s.redirectWith(w, r, base, params)
}

// redirectWith is the one exit for an authorization response to the client: it
// merges the response parameters onto the client's registered redirect URI, names
// this server as the issuer, and 302s there.
//
// The iss parameter (RFC 9207 §2) goes on every response, approval and refusal
// alike. handleMetadata advertises authorization_response_iss_parameter_supported,
// and a client that has read that flag rejects any authorization response without
// iss. The value is the configured Issuer, the same string the metadata document
// publishes as "issuer", because clients compare the two without normalizing
// either one.
//
// Every response parameter is written with Set, and iss is written last. A
// parameter of the same name already in the registered redirect URI is therefore
// replaced, never repeated, so a client that registered itself cannot make this
// server name a different issuer.
func (s *Server) redirectWith(w http.ResponseWriter, r *http.Request, base string, extra url.Values) {
	u, err := url.Parse(base)
	if err != nil {
		http.Error(w, "invalid redirect target", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for key, values := range extra {
		for _, v := range values {
			q.Set(key, v)
		}
	}
	q.Set("iss", s.cfg.Issuer)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// userError writes a plain browser-facing error for the flow legs that cannot safely
// redirect (no trusted redirect target yet, or a lost session cookie).
func userError(w http.ResponseWriter, status int, msg string) {
	http.Error(w, msg, status)
}
