//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
	"go.uber.org/goleak"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/forawellknown/testutil"
	sharedtestutil "gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// brokerRelaySeed is the deterministic seed label the broker-relay key is
// derived from across every scenario. After the WBA split the RFC 9421 keyid is
// the key's RFC 7638 thumbprint, not this label.
const brokerRelaySeed = "broker-relay.v1"

// TestWellKnownAwareResolver_RevokesThroughMiddleware drives the production
// composition end to end: the real wellKnownAwareResolver (revocation Loader +
// started poller + CompositeResolver) wired into a RFC 9421 verify handler. A
// request signed by a thumbprint the Broker's WBA directory publishes verifies;
// once the Broker revokes that thumbprint, the poller picks it up within a couple
// of intervals and the same signature is rejected. This is the consumer half of
// the revocation feature — the Broker producer side is covered in
// src/broker/internal/transport.
func TestWellKnownAwareResolver_RevokesThroughMiddleware(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	// The broker's WBA directory carries the relay key + a directory-level
	// revocation_url the Loader polls.
	f := testutil.WBAFile(key)
	f.RevocationUrl = testutil.Ptr(origin.RevocationURL())
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(priv)
	// Initial snapshot: nothing revoked, oldest possible as_of.
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(1000, 0)))

	t.Setenv("EXCHANGE_REVOCATION_POLL_INTERVAL", "150ms")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// The origin's own TLS client, not the guarded client: the origin is a
	// loopback httptest server the production guard would (correctly) refuse.
	resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), helpers.NewStaticKeyResolver(nil),
		origin.HTTPClient(), slog.Default())

	srv := newHTTPSigServer(t, resolver)

	// Active, unrevoked thumbprint → the signature verifies (200).
	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusOK {
		t.Fatalf("active key: status = %d, want 200", status)
	}

	// Revoke the thumbprint with a strictly-newer snapshot; the poller refreshes
	// and the same signed call is rejected within a few poll intervals.
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(2000, 0), tp))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status == http.StatusUnauthorized {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked key still verifying after the poll deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWellKnownAwareResolver_RevokedKeyOverridesFallthroughDelegate pins the
// revocation-vs-fallthrough PRECEDENCE explicitly: the revoked thumbprint is
// ALSO resolvable by the composite's fall-through delegate (in production the
// per-agent well-known resolver; here an injected fixed-map test resolver), and
// the revocation verdict must win — the composite halts on the authoritative
// REVOKED answer before the later delegate can re-admit the key. Without this
// case the precedence is only inferred from construction order; a resolver
// reordering that consulted the fall-through delegate first would keep every
// still-published key alive forever after revocation and no other test would
// notice.
func TestWellKnownAwareResolver_RevokedKeyOverridesFallthroughDelegate(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)
	pub, err := forawellknown.PublicKey(key)
	if err != nil {
		t.Fatalf("derive pubkey: %v", err)
	}
	// The SAME thumbprint the broker will revoke is resolvable by the
	// fall-through delegate — the delegate that must NOT get the last word.
	fallthroughDelegate := helpers.NewStaticKeyResolver(map[string]ed25519.PublicKey{tp: pub})

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	f := testutil.WBAFile(key)
	f.RevocationUrl = testutil.Ptr(origin.RevocationURL())
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(priv)
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(1000, 0)))

	t.Setenv("EXCHANGE_REVOCATION_POLL_INTERVAL", "150ms")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), fallthroughDelegate, origin.HTTPClient(), slog.Default())
	srv := newHTTPSigServer(t, resolver)

	// Active + resolvable by the fall-through delegate: verifies.
	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusOK {
		t.Fatalf("active fall-through-resolvable key: status = %d, want 200", status)
	}

	// Revoke the thumbprint: the rejection must land even though the
	// fall-through delegate still resolves it (401, no re-admission).
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(2000, 0), tp))
	deadline := time.Now().Add(5 * time.Second)
	for {
		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status == http.StatusUnauthorized {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("revoked fall-through-resolvable key still verifying after the poll deadline")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWellKnownAwareResolver_ExpiredWindowRejectsDespiteFallthroughDelegate
// pins the second authoritative negative beside revocation: a key whose
// published validity window has ENDED. The directory still lists the key and
// the fall-through delegate still resolves the same thumbprint, so the only
// thing standing between a lapsed key and a 200 is the window verdict —
// resolvers.ErrKeyExpired halting the composite (keyresolver.go maps it as
// authoritative, never a fall-through miss). A refactor that translated the
// expired verdict into a plain unknown-key miss would let the fall-through
// delegate re-admit the key, and before this test nothing would have noticed.
// The published window is the only automatic expiry a verifier gets on a
// rotated-out key it still holds in a stale cached directory.
func TestWellKnownAwareResolver_ExpiredWindowRejectsDespiteFallthroughDelegate(t *testing.T) {
	now := time.Now().UTC()
	// The window ended an hour ago: [now - 1000h, now - 1h).
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-1000*time.Hour), now.Add(-time.Hour))
	tp := testutil.MustThumbprintKey(t, key)
	pub, err := forawellknown.PublicKey(key)
	if err != nil {
		t.Fatalf("derive pubkey: %v", err)
	}
	// The SAME thumbprint stays resolvable by the fall-through delegate — the
	// delegate that must not get the last word on a lapsed key.
	fallthroughDelegate := helpers.NewStaticKeyResolver(map[string]ed25519.PublicKey{tp: pub})

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	f := testutil.WBAFile(key)
	f.RevocationUrl = testutil.Ptr(origin.RevocationURL())
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(priv)
	// Nothing is revoked: the rejection below can come only from the window.
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(1000, 0)))

	t.Setenv("EXCHANGE_REVOCATION_POLL_INTERVAL", "150ms")

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), fallthroughDelegate, origin.HTTPClient(), slog.Default())
	srv := newHTTPSigServer(t, resolver)

	// The window verdict is synchronous at resolve time — no poller wait.
	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusUnauthorized {
		t.Fatalf("expired-window key resolvable by the fall-through delegate: status = %d, want 401", status)
	}
}

// TestWellKnownAwareResolver_FailsClosedOnFetchFailure pins the fail-closed
// boundary for a signature that names the Broker: when the Broker's directory
// is unreachable/malformed, the request must NOT verify — we cannot prove the
// key is un-revoked — however a later delegate would resolve the key, and a key
// the fetched directory simply lacks is refused too. The contrast case is the
// rule that each signature is resolved in the directory its own member names: a
// request an agent signed is resolved by the per-agent delegate and verifies
// while the Broker's directory is down, without ever fetching it.
func TestWellKnownAwareResolver_FailsClosedOnFetchFailure(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)
	pub, err := forawellknown.PublicKey(key)
	if err != nil {
		t.Fatalf("derive pubkey: %v", err)
	}
	// The fall-through delegate resolves the thumbprint in both sub-cases — the
	// question is only whether the revocation channel's verdict lets it through.
	fallthroughDelegate := helpers.NewStaticKeyResolver(map[string]ed25519.PublicKey{tp: pub})

	t.Run("fetch failure fails closed despite a resolving fall-through delegate", func(t *testing.T) {
		origin := testutil.NewTLSOrigin(nil)
		t.Cleanup(origin.Close)
		origin.SetWBAStatus(http.StatusInternalServerError) // every WBA lookup → ErrFetch

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		logs, logger := sharedtestutil.NewLogCapture()
		resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), fallthroughDelegate, origin.HTTPClient(), logger)
		srv := newHTTPSigServer(t, resolver)

		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusUnauthorized {
			t.Fatalf("unreachable WBA directory: status = %d, want 401 (fail closed)", status)
		}
		// The runbook wakes someone on this event, so its level is pinned: WARN
		// keeps it visible at the default level and at WARN. It is a handled
		// problem, like identity.directory.unavailable: the refusal is the safe
		// answer, and it clears once the directory answers again.
		records := logs.Records(t, "exchange.httpsig.broker_wellknown_unavailable")
		if len(records) == 0 {
			t.Fatal("the fail-closed refusal left no exchange.httpsig.broker_wellknown_unavailable record")
		}
		for _, rec := range records {
			if rec["level"] != "WARN" {
				t.Errorf("broker_wellknown_unavailable logged at %v, want WARN: %v", rec["level"], rec)
			}
		}
	})

	t.Run("an agent's request never depends on the Broker's directory", func(t *testing.T) {
		origin := testutil.NewTLSOrigin(nil)
		t.Cleanup(origin.Close)
		origin.SetWBAStatus(http.StatusInternalServerError)

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), fallthroughDelegate, origin.HTTPClient(), slog.Default())
		srv := newHTTPSigServer(t, resolver)

		// Signed by an agent naming its own directory: resolved by the per-agent
		// delegate, so the Broker's outage does not refuse it.
		if status := signedDiscoverStatus(t, srv, agentOrigin, tp, priv); status != http.StatusOK {
			t.Fatalf("agent-signed request during a Broker directory outage: status = %d, want 200", status)
		}
		if hits := origin.WBAHits(); hits != 0 {
			t.Fatalf("an agent-signed request fetched the Broker's directory %d times", hits)
		}
	})

	t.Run("a Broker signature whose key the Broker's directory lacks is refused", func(t *testing.T) {
		origin := testutil.NewTLSOrigin(nil)
		t.Cleanup(origin.Close)
		// A valid WBA directory that lists a DIFFERENT key: the signature named
		// the Broker, so no other directory may answer for it, however a later
		// delegate would.
		otherPriv, otherKey := testutil.NewSigningKey("other.v1", now.Add(-time.Hour), now.Add(1000*time.Hour))
		origin.SetWBA(testutil.MarshalWBA(testutil.WBAFile(otherKey)))
		origin.SetWBASigners(otherPriv)

		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), fallthroughDelegate, origin.HTTPClient(), slog.Default())
		srv := newHTTPSigServer(t, resolver)

		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusUnauthorized {
			t.Fatalf("Broker-signed key absent from the Broker's directory: status = %d, want 401", status)
		}
	})
}

// TestWellKnownAwareResolver_ACallerThatLeftCannotAbortTheBrokerLookup pins
// that the lookup of the Broker's directory does not depend on the request that
// happened to start it. One fetch serves every request waiting on the
// directory, so a caller that has gone must not cut it short. When it could, the
// fetch failed with the departed caller's context, every request coalesced onto
// it was refused, and the Exchange logged
// exchange.httpsig.broker_wellknown_unavailable, a Wake-on-call alert, about a
// Broker that was answering.
//
// The requests travel over real HTTP to the verify handler the other tests in
// this file use, in front of the production composite. The only arrangement is
// the one a disconnect makes: the server hands the handler a request whose
// context has already ended. Each leg starts a fresh resolver, so nothing is
// cached and the lookup has to fetch the directory.
func TestWellKnownAwareResolver_ACallerThatLeftCannotAbortTheBrokerLookup(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)
	const event = "exchange.httpsig.broker_wellknown_unavailable"

	t.Run("a healthy Broker is looked up to the end and raises no alert", func(t *testing.T) {
		origin := testutil.NewTLSOrigin(nil)
		t.Cleanup(origin.Close)
		origin.SetWBA(testutil.MarshalWBA(testutil.WBAFile(key)))
		origin.SetWBASigners(priv)
		logs, resolver := brokerLookupUnderTest(t, origin)

		srv := sharedtestutil.CallerGoneServer(t, httpSigHandler(resolver))
		// Only the Broker's directory can resolve this key, so a 200 is the lookup
		// having run to the end although the request had already ended.
		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusOK {
			t.Errorf("status = %d, want 200 from a lookup that ran to the end", status)
		}
		if records := logs.Records(t, event); len(records) != 0 {
			t.Errorf("a caller that left made the Exchange report a healthy Broker unreachable: %v", records)
		}
	})

	t.Run("a Broker that fails is still reported, with its own error", func(t *testing.T) {
		origin := testutil.NewTLSOrigin(nil)
		t.Cleanup(origin.Close)
		origin.SetWBAStatus(http.StatusInternalServerError)
		logs, resolver := brokerLookupUnderTest(t, origin)

		srv := sharedtestutil.CallerGoneServer(t, httpSigHandler(resolver))
		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (fail closed)", status)
		}
		// The record carries the Broker's own answer rather than the caller's
		// context: the lookup reached the Broker although the request had ended.
		rec := logs.AssertLoggedOnce(t, event, "WARN")
		if errText, _ := rec["err"].(string); !strings.Contains(errText, "status 500") {
			t.Errorf("err = %q, want the Broker's own answer (status 500)", errText)
		}
	})
}

// brokerLookupUnderTest builds the production composite over the Broker
// directory at url, with a fall-through delegate that resolves nothing, so only
// the Broker's directory can verify a key. It returns the capture the
// composite logs to alongside it.
func brokerLookupUnderTest(t *testing.T, origin *testutil.Origin) (*sharedtestutil.LogCapture, helpers.KeyResolver) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logs, logger := sharedtestutil.NewLogCapture()
	return logs, wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), helpers.NewStaticKeyResolver(nil),
		origin.HTTPClient(), logger)
}

// TestWellKnownAwareResolver_RevokedFallthroughOnlyKeyRejected pins the
// directory-absent revocation case: a thumbprint the broker directory does NOT
// list but that IS present in the broker's revocation list must be rejected, and
// the refusal must name the revocation — even though the composite's
// fall-through delegate still resolves that thumbprint. A Broker-signed request
// is resolved only in the Broker's directory, so a key that directory does not
// list is refused either way; the status alone cannot tell a revoked key from an
// unknown one, so the test also reads the composite's own verdict. That verdict
// comes from the SDK's revocation-set membership accessor, which answers from
// the Broker directory's own list only. The contrast case (thumbprint absent AND
// not revoked) is covered by TestWellKnownAwareResolver_FailsClosedOnFetchFailure.
func TestWellKnownAwareResolver_RevokedFallthroughOnlyKeyRejected(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey("directory-absent.v1", now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)
	pub, err := forawellknown.PublicKey(key)
	if err != nil {
		t.Fatalf("derive pubkey: %v", err)
	}
	// The fall-through delegate resolves the thumbprint — the question is only
	// whether the revocation channel still rejects it.
	fallthroughDelegate := helpers.NewStaticKeyResolver(map[string]ed25519.PublicKey{tp: pub})

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	// The broker directory lists a DIFFERENT key (so tp is ErrKeyUnknown at the
	// directory) but its revocation list revokes tp.
	otherPriv, otherKey := testutil.NewSigningKey("other.v1", now.Add(-time.Hour), now.Add(1000*time.Hour))
	f := testutil.WBAFile(otherKey)
	f.RevocationUrl = testutil.Ptr(origin.RevocationURL())
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(otherPriv)
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(2000, 0), tp))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resolver := wellKnownAwareResolver(ctx, origin.DirectoryOrigin(), fallthroughDelegate, origin.HTTPClient(), slog.Default())
	srv := newHTTPSigServer(t, resolver)

	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusUnauthorized {
		t.Fatalf("revoked directory-absent key: status = %d, want 401 (revocation must gate the fall-through delegate)", status)
	}
	if _, err := resolver.Resolve(helpers.WithSignatureAgent(ctx, origin.DirectoryOrigin()), tp); !errors.Is(err, resolvers.ErrKeyRevoked) {
		t.Fatalf("revoked directory-absent key: err = %v, want resolvers.ErrKeyRevoked from the Broker's own list", err)
	}
	// Revocation is scoped to the signer. The SAME key, signed by an agent that
	// names its own directory, is resolved there and never against the Broker's
	// list: the Broker cannot revoke a key another party publishes.
	if status := signedDiscoverStatus(t, srv, agentOrigin, tp, priv); status != http.StatusOK {
		t.Fatalf("agent-signed request: status = %d, want 200 (the Broker's list must not decide an agent's key)", status)
	}
}

// agentOrigin is the Signature-Agent member of a request an agent signed. It is
// not the Broker's origin, so the request's key is resolved by the per-agent
// delegate and never by the Broker's directory.
const agentOrigin = "https://agent.example"

// The four cases below are SDK-parity guards: the revocation/SSRF
// anti-amplification controls now live wholly inside the pinned SDK
// resolvers.WBAKeyResolver, and these tests prove each control survives the app
// wiring (wellKnownAwareResolver → RFC 9421 verify), not only inside the SDK's
// own unit tests. There is NO production change behind them — each is expected to
// PASS on the current pin and would fail only if a future SDK bump silently
// dropped that control.

// TestWellKnownAwareResolver_DebouncesUnknownThumbprintBurst pins control (1),
// the per-host sync-debounce. The key resolver runs BEFORE the ed25519 check and
// the directory host is the caller-supplied Signature-Agent, so an
// unauthenticated caller presenting unknown thumbprints could otherwise drive one
// outbound directory GET per thumbprint (reflection/amplification + self-DoS). A
// burst of distinct unknown thumbprints for one host inside a single SyncDebounce
// window must be rejected AND must trigger at most one forced directory refresh.
func TestWellKnownAwareResolver_DebouncesUnknownThumbprintBurst(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	origin.SetWBA(testutil.MarshalWBA(testutil.WBAFile(key)))
	origin.SetWBASigners(priv)

	srv := startRevocationResolverServer(t, origin)

	// Warm the TTL directory cache with one KNOWN lookup: this fetch fills the
	// SDK's dirCache (WBAHits == 1) WITHOUT touching the unknown-thumbprint sync
	// debounce, so every later WBAHits delta is attributable only to the
	// force-refresh path the debounce gates.
	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusOK {
		t.Fatalf("warm known key: status = %d, want 200", status)
	}
	base := origin.WBAHits()

	// Burst of DISTINCT unknown thumbprints for the same directory host, all
	// inside one SyncDebounce window (SDK default 5s; the burst runs in ms). Each
	// is rejected because it is not in the directory.
	const burst = 4
	for i := range burst {
		uPriv, uKey := testutil.NewSigningKey(
			fmt.Sprintf("unknown-%d.v1", i), now.Add(-time.Hour), now.Add(1000*time.Hour))
		utp := testutil.MustThumbprintKey(t, uKey)
		if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), utp, uPriv); status != http.StatusUnauthorized {
			t.Fatalf("unknown thumbprint #%d: status = %d, want 401", i, status)
		}
	}

	// The debounce admits at most ONE forced directory refresh per window per
	// host: N unknown lookups drive <= 1 additional WBA fetch, not N.
	if delta := origin.WBAHits() - base; delta > 1 {
		t.Fatalf("WBA directory fetched %d extra times across the unknown burst, want <= 1 (sync debounce)", delta)
	}
}

// TestWellKnownAwareResolver_SkipsCrossHostRevocationURL pins control (2), the
// host-anchored revocation_url gate. A WBA directory that names a revocation_url
// on a DIFFERENT host would otherwise steer the poller at an arbitrary target
// every cadence (cross-host polling + SSRF amplification). The cross-host URL
// must be skipped, so a thumbprint the cross-host list "revokes" keeps verifying.
func TestWellKnownAwareResolver_SkipsCrossHostRevocationURL(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)

	// The revocation authority lives on a DIFFERENT host (a second origin, so a
	// different port ⇒ a different Host) than the directory.
	revOrigin := testutil.NewTLSOrigin(nil)
	t.Cleanup(revOrigin.Close)
	revOrigin.SetRevocation(testutil.MarshalRevocation(time.Unix(2000, 0), tp))

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	f := testutil.WBAFile(key)
	f.RevocationUrl = testutil.Ptr(revOrigin.RevocationURL()) // cross-host
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(priv)

	srv := startRevocationResolverServer(t, origin)

	// The cross-host revocation_url is not anchored to the directory host, so the
	// poller never fetches it: the thumbprint the cross-host list "revokes" keeps
	// verifying across every poll cycle. Were the host-anchor gate dropped, the
	// poller would fetch revOrigin and the key would flip to 401.
	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusOK {
		t.Fatalf("cross-host revocation: status = %d, want 200", status)
	}
	requireStableStatus(t, srv, origin.DirectoryOrigin(), tp, priv, http.StatusOK, time.Second)
}

// TestWellKnownAwareResolver_ClampsFutureAsOfSkew pins control (3), the 300s
// as_of future-skew clamp. A compromised or misconfigured origin could otherwise
// stamp a far-future as_of that becomes an unreachable baseline, freezing every
// subsequent (legitimately earlier) snapshot under the monotonic guard. The clamp
// pins each snapshot's effective as_of to now+300s, so a later revoking snapshot
// still applies even when both carry the same far-future raw as_of.
func TestWellKnownAwareResolver_ClampsFutureAsOfSkew(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)
	farFuture := now.Add(365 * 24 * time.Hour) // way past the 300s skew ceiling

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	f := testutil.WBAFile(key)
	f.RevocationUrl = testutil.Ptr(origin.RevocationURL())
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(priv)
	// First snapshot: empty, stamped far in the future.
	origin.SetRevocation(testutil.MarshalRevocation(farFuture))

	srv := startRevocationResolverServer(t, origin)

	if status := signedDiscoverStatus(t, srv, origin.DirectoryOrigin(), tp, priv); status != http.StatusOK {
		t.Fatalf("pre-revocation: status = %d, want 200", status)
	}

	// A later snapshot — same far-future raw as_of — revokes the key. Because the
	// clamp pins each snapshot's effective as_of to now+300s, the later poll's
	// snapshot is strictly newer and IS applied. Without the clamp the equal
	// far-future as_of would read as a rollback and this revocation would never
	// take effect (the key would stay 200 and this test would fail).
	origin.SetRevocation(testutil.MarshalRevocation(farFuture, tp))
	pollForStatus(t, srv, origin.DirectoryOrigin(), tp, priv, http.StatusUnauthorized, 5*time.Second)
}

// TestWellKnownAwareResolver_IgnoresRolledBackAsOf pins control (4), the
// monotonic rollback guard. After a newer as_of snapshot revokes a key, a fetched
// snapshot whose as_of is not strictly newer (a stale cache, a replayed prior
// snapshot, a regressed document) must be ignored — a revoked key must never be
// silently un-revoked.
func TestWellKnownAwareResolver_IgnoresRolledBackAsOf(t *testing.T) {
	now := time.Now().UTC()
	priv, key := testutil.NewSigningKey(brokerRelaySeed, now.Add(-time.Hour), now.Add(1000*time.Hour))
	tp := testutil.MustThumbprintKey(t, key)

	origin := testutil.NewTLSOrigin(nil)
	t.Cleanup(origin.Close)
	f := testutil.WBAFile(key)
	f.RevocationUrl = testutil.Ptr(origin.RevocationURL())
	origin.SetWBA(testutil.MarshalWBA(f))
	origin.SetWBASigners(priv)
	// Newer snapshot revokes the key.
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(2000, 0), tp))

	srv := startRevocationResolverServer(t, origin)

	pollForStatus(t, srv, origin.DirectoryOrigin(), tp, priv, http.StatusUnauthorized, 5*time.Second)

	// Roll back to an OLDER as_of that no longer lists the key. The monotonic
	// guard ignores it, so the key stays rejected across every subsequent poll
	// cycle. Were the guard dropped, the older empty snapshot would un-revoke the
	// key and it would flip back to 200.
	origin.SetRevocation(testutil.MarshalRevocation(time.Unix(1000, 0)))
	requireStableStatus(t, srv, origin.DirectoryOrigin(), tp, priv, http.StatusUnauthorized, time.Second)
}

// startRevocationResolverServer wires the production wellKnownAwareResolver
// (broker revocation channel ahead of an empty fall-through delegate) against
// brokerURL and mounts it behind the RFC 9421 verify handler — the same
// composition buildHTTPSigDeps exports. The loopback origin is read through its
// own TLS client (not the guarded client the production default would install)
// because the SSRF guard would correctly refuse a loopback target.
func startRevocationResolverServer(t *testing.T, broker *testutil.Origin) *httptest.Server {
	t.Helper()
	t.Setenv("EXCHANGE_REVOCATION_POLL_INTERVAL", "150ms")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	resolver := wellKnownAwareResolver(ctx, broker.DirectoryOrigin(), helpers.NewStaticKeyResolver(nil),
		broker.HTTPClient(), slog.Default())
	return newHTTPSigServer(t, resolver)
}

// pollForStatus signs a fresh DiscoverResources call as (tp, priv) until the
// verify handler returns want or the deadline passes (fatal). It waits for a
// revocation poller cycle to take effect.
func pollForStatus(
	t *testing.T, srv *httptest.Server, agent, tp string, priv ed25519.PrivateKey, want int, within time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		if signedDiscoverStatus(t, srv, agent, tp, priv) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("status never reached %d within %s", want, within)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// requireStableStatus asserts every signed call over window returns want. It
// proves a control PREVENTS a status change (host-anchor skip, monotonic
// rollback ignore) that would otherwise land once a poller cycle ran.
func requireStableStatus(
	t *testing.T, srv *httptest.Server, agent, tp string, priv ed25519.PrivateKey, want int, window time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if got := signedDiscoverStatus(t, srv, agent, tp, priv); got != want {
			t.Fatalf("status = %d, want stable %d", got, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// newHTTPSigServer mounts resolver behind an inline RFC 9421 verify handler
// (using helpers.VerifyRequestResolved) over a 200-OK handler and returns the
// test server. This replaces the deleted httpsig.Middleware, exercising the
// same resolver contract without the removed internal package.
func newHTTPSigServer(t *testing.T, resolver helpers.KeyResolver) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httpSigHandler(resolver))
	t.Cleanup(srv.Close)
	return srv
}

// httpSigHandler is the verify handler newHTTPSigServer serves, for a test that
// has to put its own server in front of it.
func httpSigHandler(resolver helpers.KeyResolver) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := helpers.VerifyRequestResolved(r.Context(), r, body, resolver, helpers.VerifyOptions{}); err != nil {
			http.Error(w, "httpsig: "+err.Error(), http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// signedDiscoverStatus signs a fresh DiscoverResources call with keyid + priv,
// naming agent as the signature's Signature-Agent member, and returns the HTTP
// status the verify handler assigns it.
func signedDiscoverStatus(t *testing.T, srv *httptest.Server, agent, keyid string, priv ed25519.PrivateKey) int {
	t.Helper()
	const body = `{}`
	target := srv.URL + "/fora.v1.ExchangeService/DiscoverResources"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Host = req.URL.Host
	created := time.Now().Unix()
	signer, err := helpers.NewEd25519Signer(keyid, priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	opts := helpers.SignOptions{Created: created, Expires: created + 30, SignatureAgent: agent}
	if err := helpers.SignRequest(req.Context(), req, []byte(body), signer, opts); err != nil {
		t.Fatalf("sign: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestBuildHTTPSigDeps_FailsClosedWithoutRevocationURL pins fail-closed boot:
// with EXCHANGE_BROKER_WELLKNOWN_URL unset, the Exchange MUST refuse to boot
// rather than proceed fail-open with only the per-agent well-known path (a key
// the Broker revoked would otherwise keep verifying). The success path (URL
// set) is covered by the other tests in this file that drive
// wellKnownAwareResolver.
// TestBuildHTTPSigDeps_RefusesABrokerDirectoryThatIsNotHTTPS pins the boot
// refusal of a Broker named by a URL that is not https or names no origin: a key
// directory is only fetched over https, and such a Broker could match no
// Signature-Agent member, so every relay would be refused while the Exchange
// looked healthy.
func TestBuildHTTPSigDeps_RefusesABrokerDirectoryThatIsNotHTTPS(t *testing.T) {
	for _, raw := range []string{"http://broker/.well-known/fora.json", "broker", "https:///.well-known/fora.json"} {
		t.Setenv("EXCHANGE_BROKER_WELLKNOWN_URL", raw)
		ctx, cancel := context.WithCancel(context.Background())
		_, _, _, err := buildHTTPSigDeps(ctx, slog.Default(), http.DefaultClient)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "EXCHANGE_BROKER_WELLKNOWN_URL") {
			t.Errorf("EXCHANGE_BROKER_WELLKNOWN_URL=%q: err = %v, want a boot refusal naming it", raw, err)
		}
	}
}

func TestBuildHTTPSigDeps_FailsClosedWithoutRevocationURL(t *testing.T) {
	// EXCHANGE_BROKER_WELLKNOWN_URL is the revocation authority; unset it so the
	// fail-closed guard is what the composition hits. os.Unsetenv (not t.Setenv,
	// which cannot clear a variable) is safe here because this test does not run
	// in parallel and the variable is unset in the default test environment.
	if err := os.Unsetenv("EXCHANGE_BROKER_WELLKNOWN_URL"); err != nil {
		t.Fatalf("unset well-known url: %v", err)
	}

	// The guard must refuse BEFORE the per-agent resolver starts its poller
	// goroutine: goleak snapshots the goroutines alive at this instant and
	// fails the test if the refused boot left a new one behind. Swapping the
	// guard back below the poller start turns exactly that leak — the cancel
	// in Cleanup runs only after the check, so it cannot hide the regression,
	// while still reaping the goroutine afterwards so a failing run does not
	// poison later tests.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	_, _, _, err := buildHTTPSigDeps(ctx, slog.Default(), http.DefaultClient)
	if err == nil {
		t.Fatal("buildHTTPSigDeps: expected a non-nil error when EXCHANGE_BROKER_WELLKNOWN_URL is unset, got nil (fail-open boot)")
	}
	if !strings.Contains(err.Error(), "EXCHANGE_BROKER_WELLKNOWN_URL") {
		t.Fatalf("buildHTTPSigDeps error = %q, want it to name EXCHANGE_BROKER_WELLKNOWN_URL", err)
	}
}
