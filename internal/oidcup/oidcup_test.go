package oidcup_test

import (
	"context"
	"net/url"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// New's discovery leg needs a live issuer, so it is exercised by the Tier-B E2E
// against a real Zitadel. What is unit-testable without a network is that New
// rejects an incomplete config before attempting discovery.
func TestNew_RejectsIncompleteConfig(t *testing.T) {
	cases := map[string]oidcup.Config{
		"no issuer":       {ClientID: "c", RedirectURL: "https://auth.example/callback"},
		"no client id":    {Issuer: "https://idp.example", RedirectURL: "https://auth.example/callback"},
		"no redirect":     {Issuer: "https://idp.example", ClientID: "c"},
		"all three unset": {},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := oidcup.New(context.Background(), cfg); err == nil {
				t.Fatalf("New(%+v) = nil error, want a validation failure", cfg)
			}
		})
	}
}

// The logout address is the provider's end_session_endpoint with this client
// and the post-logout address the person goes back to; a provider that
// advertises no endpoint yields none, and the caller ends only its own session.
func TestZitadel_LogoutURL(t *testing.T) {
	cfg := oidcup.Config{ClientID: "console", ClientSecret: "s", RedirectURL: "https://console.example/auth/callback"}

	cfg.Issuer = testutil.StartOIDCProvider(t, map[string]string{"end_session_endpoint": "https://idp.example/oidc/v1/end_session"}).URL
	z, err := oidcup.New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := url.Parse(z.LogoutURL("https://console.example/signed-out"))
	if err != nil {
		t.Fatalf("logout url: %v", err)
	}
	q := got.Query()
	if got.Scheme+"://"+got.Host+got.Path != "https://idp.example/oidc/v1/end_session" ||
		q.Get("client_id") != "console" || q.Get("post_logout_redirect_uri") != "https://console.example/signed-out" {
		t.Fatalf("logout url = %s", got)
	}

	cfg.Issuer = testutil.StartOIDCProvider(t, nil).URL
	z, err = oidcup.New(t.Context(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if u := z.LogoutURL("https://console.example/signed-out"); u != "" {
		t.Fatalf("a provider with no end_session_endpoint gave %q", u)
	}
}
