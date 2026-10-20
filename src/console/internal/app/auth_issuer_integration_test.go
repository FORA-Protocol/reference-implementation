//go:build integration

package app_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/auth"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/repo"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// WHAT THESE TESTS COVER.
//
// The console trusts one login provider, the publishers' own. Here its real
// provider client (internal/oidcup over go-oidc) signs people in against
// testutil.OIDCProvider, whose claims and signing key the test chooses. So a
// test can hand the console a token another provider issued -- the developers'
// instance, in a deployment that runs both -- and watch it refuse. A refused
// sign-in shows the console's error page, logs console.auth_failed, records no
// account, sets no session, and leaves every page sending the person to sign
// in.
//
// A session the console sealed before its provider changed is covered too: a
// console that now trusts another issuer treats it as no session.

const (
	consoleClient    = "console-client"
	identityClient   = "identity-client"
	developersIssuer = "https://login.developers.example"
	publishersIssuer = "https://login.console.example"
	signInFailed     = "Something went wrong on our side. Please try signing in again."
)

// recordingAccounts is the real account repository, counting the accounts the
// console records. It is how a test shows that a refused sign-in recorded none:
// the console has no read of its accounts, and none is added for a test.
type recordingAccounts struct {
	repo.AccountRepo
	upserts atomic.Int32
}

func (r *recordingAccounts) Upsert(ctx context.Context, a repo.Account) (repo.Account, error) {
	r.upserts.Add(1)
	return r.AccountRepo.Upsert(ctx, a)
}

// throughProvider makes the console sign people in through provider with the
// real client, and records its accounts and its log.
func throughProvider(
	t *testing.T, provider *testutil.OIDCProvider, accounts *recordingAccounts, logger *slog.Logger,
) option {
	return func(c *app.Config, _ *exchangetest.Exchange) {
		callback := c.PublicOrigin + auth.CallbackPath
		provider.Register(callback)
		upstream, err := oidcup.New(t.Context(), oidcup.Config{
			Issuer: provider.URL, ClientID: consoleClient, ClientSecret: "console-secret",
			RedirectURL: callback,
		})
		if err != nil {
			t.Fatalf("provider client: %v", err)
		}
		accounts.AccountRepo = c.Accounts
		c.Upstream, c.Accounts, c.Logger = upstream, accounts, logger
	}
}

// TestConsole_SignsInOnlyThroughItsOwnProvider hands the console's callback
// tokens its provider client must refuse, and one it must accept.
func TestConsole_SignsInOnlyThroughItsOwnProvider(t *testing.T) {
	provider := testutil.StartOIDCProvider(t, nil)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("other key: %v", err)
	}
	// token is the provider's answer to a sign-in: the claims it issues to the
	// console for the person, changed by change, signed with key.
	token := func(change func(*testutil.IDTokenClaims), key *rsa.PrivateKey) func(string) string {
		return func(nonce string) string {
			claims := provider.Claims(time.Now(), consoleClient, "publisher-1", nonce)
			claims.Email, claims.EmailVerified = "editor@publisher.example", true
			change(&claims)
			return provider.Sign(key, provider.KeyID, claims)
		}
	}
	refused := []struct {
		name  string
		token func(string) string
	}{
		{"signed with a key the provider never published", token(func(*testutil.IDTokenClaims) {}, otherKey)},
		{"issued by the developers' instance", token(func(c *testutil.IDTokenClaims) { c.Issuer = developersIssuer }, provider.Key)},
		{"issued to the identity service", token(func(c *testutil.IDTokenClaims) { c.Audience = identityClient }, provider.Key)},
		{"carrying another sign-in's nonce", token(func(c *testutil.IDTokenClaims) { c.Nonce = "another-sign-in" }, provider.Key)},
		{"the developers' instance's token for the identity service", token(func(c *testutil.IDTokenClaims) {
			c.Issuer, c.Audience = developersIssuer, identityClient
		}, otherKey)},
	}
	for _, tc := range refused {
		t.Run(tc.name, func(t *testing.T) {
			logs, logger := testutil.NewLogCapture()
			accounts := &recordingAccounts{}
			f := startConsole(t, verifiedClaims(), throughProvider(t, provider, accounts, logger))
			provider.Answer(tc.token)

			status, body := f.get(t, auth.LoginPath)
			if status != http.StatusInternalServerError || !strings.Contains(body, signInFailed) {
				t.Fatalf("the sign-in ended with %d: %s", status, head(body))
			}
			logs.AssertLoggedOnce(t, "console.auth_failed", "ERROR")
			if n := accounts.upserts.Load(); n != 0 {
				t.Fatalf("a refused sign-in recorded %d accounts", n)
			}
			f.requireNoSession(t)
		})
	}
	t.Run("the provider's own token for the console", func(t *testing.T) {
		accounts := &recordingAccounts{}
		f := startConsole(t, verifiedClaims(), throughProvider(t, provider, accounts, testutil.DiscardLogger()))
		provider.Answer(token(func(*testutil.IDTokenClaims) {}, provider.Key))
		if status, body := f.get(t, auth.LoginPath); status != http.StatusOK || !strings.Contains(body, "editor@publisher.example") {
			t.Fatalf("the sign-in ended with %d: %s", status, head(body))
		}
		if n := accounts.upserts.Load(); n != 1 {
			t.Fatalf("the sign-in recorded %d accounts, want 1", n)
		}
	})
}

// TestConsole_RefusesClaimsFromAnotherIssuer covers the console's own check
// behind its provider client: claims naming an issuer the console does not
// trust end in its error page and record nothing, rather than in a session its
// pages would refuse, which would send the person around the sign-in forever.
func TestConsole_RefusesClaimsFromAnotherIssuer(t *testing.T) {
	claims := verifiedClaims()
	claims.Issuer = developersIssuer
	logs, logger := testutil.NewLogCapture()
	accounts := &recordingAccounts{}
	f := startConsole(t, claims, func(c *app.Config, _ *exchangetest.Exchange) {
		accounts.AccountRepo = c.Accounts
		c.Accounts, c.Logger = accounts, logger
	})
	status, body := f.get(t, auth.LoginPath)
	if status != http.StatusInternalServerError || !strings.Contains(body, signInFailed) {
		t.Fatalf("the sign-in ended with %d: %s", status, head(body))
	}
	logs.AssertLoggedOnce(t, "console.auth_failed", "ERROR")
	if n := accounts.upserts.Load(); n != 0 {
		t.Fatalf("claims from another issuer recorded %d accounts", n)
	}
	f.requireNoSession(t)
}

// TestConsole_DropsASessionSealedForAnotherIssuer moves a console to another
// provider under the same session key, as a deployment does when it gives its
// publishers an instance of their own. The session sealed before the move ends;
// the same cookie still opens a console that kept the old provider, so it is
// the issuer that ends it, not the key or the address.
func TestConsole_DropsASessionSealedForAnotherIssuer(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	cookie := f.sessionCookie(t)

	movedConfig := f.config
	movedConfig.Upstream = &fakeUpstream{issuer: publishersIssuer}
	moved := consoleAt(t, movedConfig, cookie)
	if to := redirectTo(t, moved.step(t, moved.home(t))); to.Path != auth.LoginPath {
		t.Fatalf("the old session opened the moved console, which sent it to %s", to)
	}
	// The answer cleared the cookie, so the browser no longer holds it.
	moved.requireNoSession(t)

	kept := consoleAt(t, f.config, cookie)
	if a := kept.step(t, kept.home(t)); a.status != http.StatusOK {
		t.Fatalf("the same session at a console that kept its provider = %d, want 200", a.status)
	}
}

// consoleAt serves another console built from cfg, over the fixture's
// database and session key, to a fresh browser that holds cookie: the
// console a deployment's next release runs, as the person's browser meets it.
func consoleAt(t *testing.T, cfg app.Config, cookie *http.Cookie) *consoleFixture {
	t.Helper()
	handler, err := app.Build(cfg)
	if err != nil {
		t.Fatalf("build console: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	browser := newBrowser(t)
	browser.Jar.SetCookies(mustParse(t, srv.URL), []*http.Cookie{{Name: cookie.Name, Value: cookie.Value}})
	return &consoleFixture{server: srv, client: browser, config: cfg}
}

// home is a request for the console's home page.
func (f *consoleFixture) home(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.server.URL+web.HomePath, http.NoBody)
	if err != nil {
		t.Fatalf("build home: %v", err)
	}
	return req
}

// requireNoSession fails the test if the browser holds a console session, or
// if the home page does not send it to sign in.
func (f *consoleFixture) requireNoSession(t *testing.T) {
	t.Helper()
	for _, c := range f.client.Jar.Cookies(mustParse(t, f.server.URL)) {
		if c.Name == auth.SessionCookie && c.Value != "" {
			t.Fatal("the browser holds a console session")
		}
	}
	if to := redirectTo(t, f.step(t, f.home(t))); to.Path != auth.LoginPath {
		t.Fatalf("home sent the browser to %s, want the sign-in", to)
	}
}

// sessionCookie is the browser's console session cookie.
func (f *consoleFixture) sessionCookie(t *testing.T) *http.Cookie {
	t.Helper()
	for _, c := range f.client.Jar.Cookies(mustParse(t, f.server.URL)) {
		if c.Name == auth.SessionCookie {
			return c
		}
	}
	t.Fatal("the browser holds no console session")
	return nil
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}
