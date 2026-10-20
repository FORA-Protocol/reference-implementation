// Package oidcup is the upstream-OIDC leg of a sign-in flow: a service acts as
// an OAuth client of the deployment's login provider to authenticate a person,
// and does whatever it does next with the resulting claims. It exposes a narrow
// Authenticator port so handlers depend on an interface a test can fake --
// standing up a real provider is an end-to-end concern, not a unit-test one. The
// production implementation wraps go-oidc (discovery and ID-token verification)
// and x/oauth2 (authorize URL and code exchange).
//
// Two services are clients of it: the agent-side identity registry, which turns
// a sign-in into its own downstream code and token, and the publisher console,
// which turns one into a session cookie. That is why it sits in the shared tree,
// and why the sign-in sequence both run -- minting the state, nonce and PKCE
// verifier, sealing them for the trip, and checking them at the callback -- is
// here too, as Flow, rather than written once per service.
package oidcup

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// Claims is the subset of the verified upstream ID token a sign-in consumes. The
// (Issuer, Subject) pair is the person's durable identity: an email address is
// reassignable and a subject is not, so nothing keys on the address.
//
// EmailVerified is the provider's answer to whether it checked the address,
// taken from the standard email_verified claim. It is false when the claim is
// absent, which is the answer a caller that gates on it needs: a provider that
// says nothing has not verified anything. A caller that does not gate on it can
// ignore it.
type Claims struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

// Authenticator is the upstream port the auth-server handlers depend on.
type Authenticator interface {
	// AuthCodeURL builds the upstream authorize URL carrying state, an OIDC nonce,
	// and the S256 PKCE challenge for the upstream leg. The caller owns the PKCE
	// pair (it stashes the verifier in a cookie) and passes the challenge here.
	AuthCodeURL(state, nonce, challenge string) string
	// Exchange trades code for tokens against the upstream, verifies the ID token's
	// signature/issuer/audience, checks the nonce matches, and returns the claims.
	Exchange(ctx context.Context, code, verifier, nonce string) (Claims, error)
}

// Logouter ends a person's session at the provider, the OpenID Connect
// RP-Initiated Logout step. A service that clears only its own cookie leaves
// the provider's session standing, and the next visit to a protected page
// signs the person straight back in without asking them anything.
type Logouter interface {
	// LogoutURL is the provider's address that ends the person's session there
	// and then sends the browser to returnTo. returnTo must be registered with
	// the provider as a post-logout redirect address for this client, or the
	// provider refuses it. Empty when the provider advertises no
	// end_session_endpoint; the caller then ends only its own session.
	LogoutURL(returnTo string) string
}

// ErrExchange marks any failure of the upstream leg — a rejected code, an ID token
// that will not verify, or a nonce mismatch (a replayed or cross-session token).
var ErrExchange = errors.New("oidcup: upstream exchange failed")

// Config wires a Zitadel authenticator.
type Config struct {
	// Issuer is the OIDC issuer URL; go-oidc discovers endpoints and the JWKS from
	// its /.well-known/openid-configuration.
	Issuer string
	// ClientID and ClientSecret are the STATIC confidential client the operator
	// pre-registers in Zitadel (Zitadel v3 has no Dynamic Client Registration, so
	// this leg cannot self-register the way our downstream clients do).
	ClientID     string
	ClientSecret string
	// RedirectURL is this service's own /callback.
	RedirectURL string
	// Scopes defaults to openid+profile+email when empty.
	Scopes []string
}

// Zitadel is the production Authenticator over go-oidc + oauth2.
type Zitadel struct {
	issuer   string
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	// endSession is the provider's end_session_endpoint from discovery, empty
	// when it advertises none.
	endSession string
}

var (
	_ Authenticator = (*Zitadel)(nil)
	_ Logouter      = (*Zitadel)(nil)
)

// New builds a Zitadel authenticator, performing OIDC discovery against the issuer.
func New(ctx context.Context, cfg Config) (*Zitadel, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.RedirectURL == "" {
		return nil, errors.New("oidcup: Issuer, ClientID, and RedirectURL are required")
	}
	// The upstream client is a static confidential client (Zitadel v3 has no DCR);
	// a missing secret otherwise fails only at the first real code exchange, so
	// require it up front rather than degrade to a public-client posture silently.
	if cfg.ClientSecret == "" {
		return nil, errors.New("oidcup: ClientSecret is required for the confidential upstream client")
	}
	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidcup: discover %q: %w", cfg.Issuer, err)
	}
	// go-oidc reads the endpoints a sign-in needs and leaves the rest of the
	// discovery document to Claims.
	var discovery struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&discovery); err != nil {
		return nil, fmt.Errorf("oidcup: read %q discovery: %w", cfg.Issuer, err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}
	return &Zitadel{
		issuer:   cfg.Issuer,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       scopes,
		},
		endSession: discovery.EndSession,
	}, nil
}

// Issuer is the issuer this authenticator was configured with. It is the only
// one whose ID tokens Exchange accepts: go-oidc refuses a token whose iss
// differs, so every Claims.Issuer it returns is this value.
func (z *Zitadel) Issuer() string { return z.issuer }

// LogoutURL implements Logouter. The request names the client rather than
// carrying the ID token as a hint: the console keeps no token after a sign-in,
// and client_id is what RP-Initiated Logout asks for in its place when a
// post-logout address is given.
func (z *Zitadel) LogoutURL(returnTo string) string {
	return logoutURL(z.endSession, z.oauth.ClientID, returnTo)
}

// logoutURL builds the end-session request. An endpoint that is missing or
// does not parse yields no URL, so the caller ends only its own session.
func logoutURL(endpoint, clientID, returnTo string) string {
	if endpoint == "" {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", clientID)
	q.Set("post_logout_redirect_uri", returnTo)
	u.RawQuery = q.Encode()
	return u.String()
}

// AuthCodeURL implements Authenticator.
func (z *Zitadel) AuthCodeURL(state, nonce, challenge string) string {
	return z.oauth.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Exchange implements Authenticator.
func (z *Zitadel) Exchange(ctx context.Context, code, verifier, nonce string) (Claims, error) {
	tok, err := z.oauth.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return Claims{}, fmt.Errorf("%w: code exchange: %w", ErrExchange, err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return Claims{}, fmt.Errorf("%w: token response carried no id_token", ErrExchange)
	}
	idt, err := z.verifier.Verify(ctx, rawID)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: verify id_token: %w", ErrExchange, err)
	}
	if idt.Nonce != nonce {
		return Claims{}, fmt.Errorf("%w: nonce mismatch", ErrExchange)
	}
	return claimsFrom(idt)
}

// claimsFrom pulls the profile members off the verified token, tolerating their
// absence: an optional field simply stays empty, and an absent email_verified
// reads as false, which is what an unanswered question means here.
func claimsFrom(idt *oidc.IDToken) (Claims, error) {
	var profile struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
	}
	if err := idt.Claims(&profile); err != nil {
		return Claims{}, fmt.Errorf("%w: decode claims: %w", ErrExchange, err)
	}
	return Claims{
		Issuer:        idt.Issuer,
		Subject:       idt.Subject,
		Email:         profile.Email,
		EmailVerified: profile.EmailVerified,
		Name:          profile.Name,
	}, nil
}
