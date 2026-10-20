package offerkeys

// Integration-shaped test: the resolver fetches a REAL signed WBA directory
// served by a TLS httptest server (the adapter boundary — the exchange is the
// external system here), selects the active key, and caches it for the TTL.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/clock"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
)

func wbaBodyWindow(t *testing.T, pub ed25519.PublicKey, notBefore, notAfter string) []byte {
	t.Helper()
	doc := map[string]any{
		"keys": []map[string]string{{
			"kty":        "OKP",
			"crv":        "Ed25519",
			"use":        "sig",
			"alg":        "EdDSA",
			"x":          base64.RawURLEncoding.EncodeToString(pub),
			"not_before": notBefore,
			"not_after":  notAfter,
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal wba doc: %v", err)
	}
	return raw
}

func wbaBody(t *testing.T, pub ed25519.PublicKey) []byte {
	t.Helper()
	return wbaBodyWindow(t, pub, "2000-01-01T00:00:00Z", "2100-01-01T00:00:00Z")
}

// exchangeDirectory serves body as an exchange's key directory over TLS, signed
// by priv, counting fetches. Its domain is the origin's host:port.
func exchangeDirectory(t *testing.T, priv ed25519.PrivateKey, body []byte) *testutil.Origin {
	t.Helper()
	o := testutil.NewTLSOrigin(nil)
	t.Cleanup(o.Close)
	o.SetWBA(body)
	o.SetWBASigners(priv)
	return o
}

func TestResolve_FetchesActiveKeyAndCaches(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	o := exchangeDirectory(t, priv, wbaBody(t, pub))
	r := New(Config{Client: o.HTTPClient()})

	got, err := r.Resolve(context.Background(), o.Host())
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !got.Equal(pub) {
		t.Fatalf("resolved key mismatch")
	}
	// Second resolve within TTL hits the cache — no second fetch.
	if _, err := r.Resolve(context.Background(), o.Host()); err != nil {
		t.Fatalf("cached resolve: %v", err)
	}
	if n := o.WBAHits(); n != 1 {
		t.Fatalf("directory fetched %d times; want 1 (TTL cache)", n)
	}
}

// TestResolve_UnsignedDirectoryResolvesNoKey pins that an exchange directory
// whose response its key did not sign yields no offer key, so every offer it
// signed fails verification.
func TestResolve_UnsignedDirectoryResolvesNoKey(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	o := exchangeDirectory(t, priv, wbaBody(t, pub))
	o.SetWBASigners()
	if _, err := New(Config{Client: o.HTTPClient()}).Resolve(context.Background(), o.Host()); !errors.Is(err, helpers.ErrUnknownKey) {
		t.Fatalf("want the unknown-key sentinel for an unsigned directory, got %v", err)
	}
}

func TestResolve_ClampsCacheToKeyNotAfter(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	start := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	notAfter := start.Add(30 * time.Second)
	o := exchangeDirectory(t, priv, wbaBodyWindow(t, pub, "2000-01-01T00:00:00Z", notAfter.Format(time.RFC3339)))
	clk := clock.NewDeterministic(start)
	// TTL far exceeds the key's remaining validity window, so the clamp — not
	// the TTL — must decide when the cache entry expires.
	r := New(Config{Client: o.HTTPClient(), TTL: time.Hour, Clk: clk})

	if _, err := r.Resolve(context.Background(), o.Host()); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	// Advance past not_after but far inside the raw TTL. Without the clamp the
	// cache would still serve the now-lapsed key; with it the entry has expired.
	clk.Advance(45 * time.Second)
	if _, err := r.Resolve(context.Background(), o.Host()); err == nil {
		t.Fatal("resolve after not_after must fail closed, not serve the lapsed key from cache")
	}
	if n := o.WBAHits(); n != 2 {
		t.Fatalf("directory fetched %d times; want 2 (entry expired at not_after, re-fetched)", n)
	}
}

// TestResolve_NilClientFailsClosed pins the Config.Client REQUIRED contract: a
// nil Client is never a fall-open to some in-package default — it surfaces
// forawellknown.ErrNoClient the first time the resolver reaches for the
// directory. This keeps the docstring honest (the guarded client must be
// injected at the composition root; there is no fallback).
func TestResolve_NilClientFailsClosed(t *testing.T) {
	t.Parallel()
	r := New(Config{})
	_, err := r.Resolve(context.Background(), "exchange.example")
	if !errors.Is(err, forawellknown.ErrNoClient) {
		t.Fatalf("want ErrNoClient when Client is omitted, got %v", err)
	}
}

func TestResolve_FailsClosedOnMissingDirectory(t *testing.T) {
	t.Parallel()
	o := testutil.NewTLSOrigin(nil)
	t.Cleanup(o.Close)
	o.SetWBAStatus(http.StatusNotFound)
	r := New(Config{Client: o.HTTPClient(), TTL: time.Minute})
	if _, err := r.Resolve(context.Background(), o.Host()); err == nil {
		t.Fatal("resolve against a keyless exchange must error (fail-closed)")
	}
}

// TestResolve_DirectoryBehindARedirectResolvesNoKey pins that a directory answer
// redirecting elsewhere yields no offer key, even through a client whose own
// policy follows redirects, as the production guarded client does. The target
// serves a genuine signed directory and is never read.
func TestResolve_DirectoryBehindARedirectResolvesNoKey(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	target := exchangeDirectory(t, priv, wbaBody(t, pub))
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirector.Close)

	_, err = New(Config{Client: redirector.Client()}).Resolve(context.Background(), redirector.Listener.Addr().String())
	if !errors.Is(err, helpers.ErrUnknownKey) || !errors.Is(err, resolvers.ErrRedirectRefused) {
		t.Fatalf("want the unknown-key sentinel over a refused redirect, got %v", err)
	}
	if n := target.WBAHits(); n != 0 {
		t.Fatalf("the redirect target was read %d times, want 0", n)
	}
}
