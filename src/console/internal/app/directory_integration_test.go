//go:build integration

package app_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/oidcup"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/session"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/app"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/db/sqlc"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/exchangeclient"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/repo"
)

// noLogin is a login provider nobody signs in through: the key directory is
// open, so these tests never reach the sign-in routes.
type noLogin struct{}

func (noLogin) AuthCodeURL(_, _, _ string) string { return testIssuer + "/authorize" }

func (noLogin) Exchange(context.Context, string, string, string) (oidcup.Claims, error) {
	return oidcup.Claims{}, nil
}

func (noLogin) LogoutURL(string) string { return "" }

func (noLogin) Issuer() string { return testIssuer }

// startConsoleAt builds the console on the given clock and serves it. The
// Exchange client points at an address nothing listens on, because the key
// directory never calls the Exchange.
func startConsoleAt(t *testing.T, clk clock.Clock) *httptest.Server {
	t.Helper()
	pool := acquireTestDB(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("console ed25519 gen: %v", err)
	}
	client, err := exchangeclient.New(exchangeclient.Config{
		BaseURL:    "https://exchange.example",
		Directory:  "https://console.example",
		SigningKey: priv,
	})
	if err != nil {
		t.Fatalf("exchange client: %v", err)
	}
	key := make([]byte, session.KeyLen)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("session key: %v", err)
	}
	handler, err := app.Build(app.Config{
		Logger:        testutil.DiscardLogger(),
		Accounts:      repo.NewAccountRepo(sqlc.New(pool)),
		Upstream:      noLogin{},
		PublicOrigin:  "https://console.example",
		SecureCookies: true,
		SessionKey:    key,
		Clk:           clk,
		Exchange:      client,
		SigningKey:    priv,
	})
	if err != nil {
		t.Fatalf("build console: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// fetchDirectory reads the console's key directory over HTTP and parses it the
// way a verifier does.
func fetchDirectory(t *testing.T, base string) *forawellknown.WBAFile {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+app.DirectoryPath, http.NoBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET directory: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET directory: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read directory: %v", err)
	}
	f, err := forawellknown.ParseWBA(raw)
	if err != nil {
		t.Fatalf("parse directory: %v", err)
	}
	return f
}

// TestKeyDirectory_WindowMovesWithTheClock checks what the Exchange depends on
// to keep trusting a long-running console: the published key is active at
// every instant the console is up, not only in the ninety days after start-up.
// Both fetches go through the console's public route; the clock is the only
// thing the test moves.
func TestKeyDirectory_WindowMovesWithTheClock(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewDeterministic(start)
	srv := startConsoleAt(t, clk)

	if got := forawellknown.ActiveKeys(fetchDirectory(t, srv.URL), clk.Now()); len(got) != 1 {
		t.Fatalf("at start-up: %d active keys, want 1", len(got))
	}

	// Past the ninety-day window the start-up document declared. A document
	// stamped once would now carry only a lapsed key.
	clk.Advance(91 * 24 * time.Hour)
	f := fetchDirectory(t, srv.URL)
	if got := forawellknown.ActiveKeys(f, clk.Now()); len(got) != 1 {
		t.Fatalf("after 91 days: %d active keys, want 1 (not_after %q, now %s)",
			len(got), f.GetKeys()[0].GetNotAfter(), clk.Now().Format(time.RFC3339))
	}
	notAfter, err := time.Parse(time.RFC3339, f.GetKeys()[0].GetNotAfter())
	if err != nil {
		t.Fatalf("not_after: %v", err)
	}
	if !notAfter.After(clk.Now()) {
		t.Fatalf("not_after %s is not after now %s", notAfter, clk.Now())
	}
}
