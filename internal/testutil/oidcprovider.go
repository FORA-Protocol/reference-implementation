package testutil

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

// OIDCProvider is a login provider for tests that run the real OIDC client
// (internal/oidcup over go-oidc) against it. It serves what a sign-in touches:
// the discovery document, the signing key set, an authorize endpoint that
// sends the browser straight back to the client with a code, as a provider
// does once a person has signed in, and a token endpoint that answers that
// code with the ID token the test chose. Like a real provider, it sends the
// browser back only to a redirect address registered with it (Register).
//
// The test decides every claim and the key that signs it, so it can hand the
// client a token from another issuer, for another audience or under a key the
// provider never published, and watch the client refuse it. The token's times
// are the test's too: nothing here reads the wall clock.
type OIDCProvider struct {
	// URL is the provider's issuer: the address its discovery document names.
	URL string
	// Key and KeyID are the provider's own signing key, the one its key set
	// publishes.
	Key   *rsa.PrivateKey
	KeyID string

	mu sync.Mutex
	// redirects are the registered redirect addresses, keyed by themselves.
	redirects map[string]string
	// nonce is the one the last authorize request carried.
	nonce string
	// answer builds the ID token the token endpoint returns, from the nonce
	// of the sign-in it ends.
	answer func(nonce string) string
}

// StartOIDCProvider starts a provider whose discovery document also carries
// the extra members given, such as an end_session_endpoint. Until Answer is
// called, its token endpoint refuses every code.
func StartOIDCProvider(tb testing.TB, extra map[string]string) *OIDCProvider {
	tb.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		tb.Fatalf("oidc provider key: %v", err)
	}
	p := &OIDCProvider{Key: key, KeyID: "provider-key", redirects: map[string]string{}}
	keys := &oidctest.Server{PublicKeys: []oidctest.PublicKey{
		{PublicKey: key.Public(), KeyID: p.KeyID, Algorithm: oidc.RS256},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer":                 p.URL,
			"authorization_endpoint": p.URL + "/authorize",
			"token_endpoint":         p.URL + "/token",
			"jwks_uri":               p.URL + "/keys",
		}
		maps.Copy(doc, extra)
		writeJSON(w, doc)
	})
	mux.Handle("GET /keys", keys)
	mux.HandleFunc("GET /authorize", p.authorize)
	mux.HandleFunc("POST /token", p.token)
	srv := httptest.NewServer(mux)
	tb.Cleanup(srv.Close)
	p.URL = srv.URL
	keys.SetIssuer(srv.URL)
	return p
}

// Register makes redirectURI an address the authorize endpoint sends the
// browser back to. Any other is refused, as a real provider refuses an
// address the client never registered.
func (p *OIDCProvider) Register(redirectURI string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.redirects[redirectURI] = redirectURI
}

// Answer sets the ID token the next code is exchanged for.
func (p *OIDCProvider) Answer(idToken func(nonce string) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answer = idToken
}

// IDTokenClaims are the members of an ID token a sign-in reads.
type IDTokenClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Subject       string `json:"sub"`
	Nonce         string `json:"nonce"`
	IssuedAt      int64  `json:"iat"`
	Expiry        int64  `json:"exp"`
	Email         string `json:"email,omitempty"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name,omitempty"`
}

// Claims are the claims this provider issues at now for subject, at
// audience, with the nonce given: its own issuer and five minutes of life. A
// test changes a member to make a token the client must refuse.
func (p *OIDCProvider) Claims(now time.Time, audience, subject, nonce string) IDTokenClaims {
	return IDTokenClaims{
		Issuer:   p.URL,
		Audience: audience,
		Subject:  subject,
		Nonce:    nonce,
		IssuedAt: now.Unix(),
		Expiry:   now.Add(5 * time.Minute).Unix(),
	}
}

// Sign returns claims as an ID token signed with key under keyID: the
// provider's own key, or one it never published.
//
// It takes no testing.TB because its natural caller is an Answer callback,
// which runs on the server's goroutine, where a test may not stop. Claims hold
// strings, numbers and a bool only, so marshalling them cannot fail; like
// oidctest.SignIDToken, it panics if it ever does, and net/http turns the
// panic into a failed token exchange.
func (p *OIDCProvider) Sign(key *rsa.PrivateKey, keyID string, claims IDTokenClaims) string {
	raw, err := json.Marshal(claims)
	if err != nil {
		panic("testutil: id token claims: " + err.Error())
	}
	return oidctest.SignIDToken(key, keyID, oidc.RS256, string(raw))
}

// authorize remembers the request's nonce and sends the browser back to the
// registered address it names, with a code and its state.
func (p *OIDCProvider) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p.mu.Lock()
	p.nonce = q.Get("nonce")
	registered, ok := p.redirects[q.Get("redirect_uri")]
	p.mu.Unlock()
	back, err := url.Parse(registered)
	if !ok || err != nil {
		http.Error(w, "redirect_uri is not registered", http.StatusBadRequest)
		return
	}
	v := back.Query()
	v.Set("code", "provider-code")
	v.Set("state", q.Get("state"))
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token answers the code with the chosen ID token.
func (p *OIDCProvider) token(w http.ResponseWriter, _ *http.Request) {
	p.mu.Lock()
	answer, nonce := p.answer, p.nonce
	p.mu.Unlock()
	if answer == nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	writeJSON(w, struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		IDToken     string `json:"id_token"`
	}{"provider-access-token", "Bearer", 300, answer(nonce)})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}
