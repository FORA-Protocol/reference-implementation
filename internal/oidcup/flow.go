package oidcup

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// tokenBytes is the entropy behind every value this package mints: the state,
// the nonce and the PKCE verifier. 32 bytes base64url-encodes to 43
// characters, the shortest verifier RFC 7636 §4.1 allows.
const tokenBytes = 32

// Leg is the secret set one sign-in needs on its way out to the provider and
// back: the state that binds the callback to this browser, the nonce that
// binds the ID token to this sign-in, and the PKCE verifier that binds the code
// to this client.
type Leg struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
}

// NewLeg mints a fresh state, nonce and verifier.
func NewLeg() (Leg, error) {
	var leg Leg
	for _, dst := range []*string{&leg.State, &leg.Nonce, &leg.Verifier} {
		v, err := RandomToken(tokenBytes)
		if err != nil {
			return Leg{}, err
		}
		*dst = v
	}
	return leg, nil
}

// Challenge derives the S256 PKCE challenge from a verifier (RFC 7636 §4.2):
// base64url(SHA-256(verifier)), unpadded.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomToken returns n bytes of base64url-encoded randomness.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oidcup: entropy: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SameToken compares two opaque tokens in constant time. A comparison that
// returns at the first differing byte tells an attacker how much of a guess was
// right, which over enough attempts is the token. An empty expected value never
// matches: a token nobody minted is not one anybody can present.
func SameToken(want, got string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

// Jar seals a value into a cookie and reads it back. Each service keeps its own
// implementation, because the cookie attributes -- and the secure-transport
// guarantee behind them -- are the service's decision, not this package's.
type Jar interface {
	Set(w http.ResponseWriter, name string, payload any, ttl time.Duration) error
	Read(r *http.Request, name string, dst any) error
	Clear(w http.ResponseWriter, name string)
}

// Errors Finish reports. Each one is a distinct answer a service gives the
// person, so each is its own sentinel.
var (
	// ErrNoFlow means the flow cookie was absent, expired or not this key's:
	// a callback nobody started in this browser, or one that took too long.
	ErrNoFlow = errors.New("oidcup: no sign-in in progress")
	// ErrStateMismatch means the callback carried a state this browser did not
	// mint -- a callback a third party caused.
	ErrStateMismatch = errors.New("oidcup: sign-in state mismatch")
	// ErrDenied means the provider answered with an error instead of a code.
	ErrDenied = errors.New("oidcup: the provider refused the sign-in")
)

// FlowConfig wires a Flow.
type FlowConfig struct {
	Upstream Authenticator
	Jar      Jar
	// Cookie is the name the in-flight sign-in is sealed under.
	Cookie string
	// TTL bounds how long a person may take at the provider.
	TTL time.Duration
}

// Flow is the client half of one OIDC sign-in: Start seals the leg and the
// caller's own context into a cookie and returns the provider's address; Finish
// reads it back at the callback and trades the code for verified claims.
//
// T is whatever the caller needs carried across the trip -- the console's
// return path, the identity registry's downstream authorization request. It is
// sealed with the leg, so the browser can neither read nor forge it.
type Flow[T any] struct {
	upstream Authenticator
	jar      Jar
	cookie   string
	ttl      time.Duration
}

// sealedFlow is what the flow cookie carries.
type sealedFlow[T any] struct {
	Leg  Leg `json:"leg"`
	Data T   `json:"data"`
}

// NewFlow builds a Flow, refusing a configuration that cannot work.
func NewFlow[T any](cfg FlowConfig) (*Flow[T], error) {
	switch {
	case cfg.Upstream == nil:
		return nil, errors.New("oidcup: an upstream authenticator is required")
	case cfg.Jar == nil:
		return nil, errors.New("oidcup: a cookie jar is required")
	case cfg.Cookie == "":
		return nil, errors.New("oidcup: a flow cookie name is required")
	case cfg.TTL <= 0:
		return nil, errors.New("oidcup: a positive flow lifetime is required")
	}
	return &Flow[T]{upstream: cfg.Upstream, jar: cfg.Jar, cookie: cfg.Cookie, ttl: cfg.TTL}, nil
}

// Start mints a leg, seals it with data, and returns where to send the browser.
func (f *Flow[T]) Start(w http.ResponseWriter, data T) (string, error) {
	leg, err := NewLeg()
	if err != nil {
		return "", err
	}
	if err := f.jar.Set(w, f.cookie, sealedFlow[T]{Leg: leg, Data: data}, f.ttl); err != nil {
		return "", err
	}
	return f.upstream.AuthCodeURL(leg.State, leg.Nonce, Challenge(leg.Verifier)), nil
}

// Finish completes a sign-in at the callback.
//
// The sequence is fixed and is the reason this is shared: the flow cookie is
// read and cleared first, so a spent sign-in cannot be replayed; the state is
// compared, in constant time, before anything else in the callback is trusted,
// because a callback a third party caused carries a state this browser never
// minted, and exchanging its code would sign the person in as whoever that
// party authenticated; only then is a provider error or a code looked at.
//
// The caller's data is returned with ErrDenied and with an exchange failure,
// because a service may need it to answer (the identity registry redirects a
// denial back to its own client). It is not returned with ErrNoFlow or
// ErrStateMismatch: in both cases nothing in it can be trusted to be this
// browser's.
func (f *Flow[T]) Finish(w http.ResponseWriter, r *http.Request) (T, Claims, error) {
	var zero T
	var sealed sealedFlow[T]
	if err := f.jar.Read(r, f.cookie, &sealed); err != nil {
		return zero, Claims{}, fmt.Errorf("%w: %w", ErrNoFlow, err)
	}
	f.jar.Clear(w, f.cookie)
	q := r.URL.Query()
	if !SameToken(sealed.Leg.State, q.Get("state")) {
		return zero, Claims{}, ErrStateMismatch
	}
	if q.Get("error") != "" {
		return sealed.Data, Claims{}, ErrDenied
	}
	claims, err := f.upstream.Exchange(r.Context(), q.Get("code"), sealed.Leg.Verifier, sealed.Leg.Nonce)
	if err != nil {
		return sealed.Data, Claims{}, err
	}
	return sealed.Data, claims, nil
}
