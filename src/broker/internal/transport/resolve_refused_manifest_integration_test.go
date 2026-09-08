//go:build integration

package transport_test

// A registered Exchange whose manifest this build refuses, driven through
// fora.v1.BrokerService/Resolve across a real health-refresher pass.
//
// The refusal is a VERDICT: the resolver read the document and decided about
// it, and the next attempt reads the same document. The routing gate classifies
// it correctly when it resolves the endpoint itself — but it reads the registry
// row's admission first, and the refresher used to record a verdict as ordinary
// unhealthiness. So one probe pass turned a settled refusal into
// TEMPORARILY_UNAVAILABLE and an agent was told to retry a manifest that will
// never change.
//
// Round-trip honesty: the write leg is real all the way down — the production
// Refresher, the production registry repository, the shared Postgres — and the
// read leg is the real signed Resolve RPC through the real routing gate. The one
// staged part is the manifest fetch itself, at the endpoint-resolver seam this
// fixture already substitutes for every test in the suite (fakeEndpointResolver).
// The same verdict over a REAL well-known fetch is covered by the relay suite,
// which wires the production resolver.

import (
	"net/http"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/resolvers"
)

// TestResolve_RefusedExchangeManifestIsSettledAndRecovers walks the whole life
// of a refusal through the public RPC: the Exchange serves, its manifest starts
// being refused, a probe pass records that, the URL answers with the SETTLED
// reason, the Exchange republishes, and routing resumes.
//
// Leg 3 is the regression. NOT_IN_CATALOG and TEMPORARILY_UNAVAILABLE are both
// refusals and both carry no offers, so a test that only checked "no offers"
// would pass either way. The reason is the whole behaviour: one tells the agent
// to look elsewhere, the other tells it to come back, and coming back finds the
// same document.
//
// Leg 5 pins the other half. A settled state must not be a one-way ratchet: the
// refresher keeps probing REFUSED rows, so an Exchange that starts publishing a
// version this build accepts routes again with no operator intervention.
func TestResolve_RefusedExchangeManifestIsSettledAndRecovers(t *testing.T) {
	ctx := t.Context()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example", captureLogs: true})
	refresher := newRefresher(fx)

	// Leg 1: baseline. Everything after this is about the manifest verdict, not
	// about a fixture that could never serve the URL.
	assertServed(t, resolveCataloged(t, fx), "baseline, before the manifest was refused")
	servedBefore := fx.exchange.discoverCalls

	// Leg 2: the Exchange's manifest starts carrying a document version this
	// build does not accept. One refresher pass reads it and records the verdict
	// on the row, which is the sequence production runs on its ticker.
	fx.endpoints.refuseDomain(mockExchangeDomain, resolvers.ErrManifestVersionRefused)
	refreshOnce(t, refresher, ctx)

	// Leg 3: the URL answers with the SETTLED refusal, the Exchange is not
	// dialled, and the operator can read which exchange dropped out and why.
	assertUnroutable(t, resolveCataloged(t, fx),
		forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
		"while the exchange's manifest is refused")
	if fx.exchange.discoverCalls != servedBefore {
		t.Errorf("discover calls = %d, want %d — an exchange whose manifest was refused "+
			"must not be dialled", fx.exchange.discoverCalls, servedBefore)
	}
	// Its own reason token, not the outage one: waiting fixes an outage, and only
	// the exchange republishing fixes this, so an operator reading the two the
	// same way would wait for something that is not going to happen.
	assertSkipLogged(t, fx, mockExchangeDomain, "refused")

	// Leg 4: the Exchange republishes a manifest this build accepts. The next
	// pass must still reach it — a REFUSED row stays on the list the refresher
	// reads, or the state is a one-way ratchet.
	fx.endpoints.stopRefusing(mockExchangeDomain)
	refreshOnce(t, refresher, ctx)

	// Leg 5: routing resumes on its own.
	assertServed(t, resolveCataloged(t, fx), "after the exchange republished its manifest")
	if fx.exchange.discoverCalls <= servedBefore {
		t.Errorf("discover calls = %d, want more than %d — the recovered exchange was never dialled",
			fx.exchange.discoverCalls, servedBefore)
	}
}

// TestResolve_RefusedManifestOutranksAnUnreachableSibling covers the mixed
// batch, which is where the two classifications actually collide.
//
// One publisher names an exchange whose manifest is refused; the other names one
// that is merely down. The refused URL owes NOT_IN_CATALOG and the down one owes
// TEMPORARILY_UNAVAILABLE, and the request-level reason must not soften the
// first into the second. Without the settled/transient split the whole batch
// reported as retryable, because a single transient decline anywhere in the scan
// decided the answer for every URL in it.
func TestResolve_RefusedManifestOutranksAnUnreachableSibling(t *testing.T) {
	ctx := t.Context()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example", captureLogs: true})
	refresher := newRefresher(fx)

	assertServed(t, resolveCataloged(t, fx), "baseline, before either fault")

	// Both faults at once, each through its own production lever: the manifest
	// verdict at the resolver, the outage at /healthz.
	fx.endpoints.refuseDomain(mockExchangeDomain, resolvers.ErrManifestVersionRefused)
	fx.exchange.setHealthz(http.StatusServiceUnavailable)
	refreshOnce(t, refresher, ctx)

	// Trust is read before the probe state and the manifest verdict before the
	// outage, so the row lands REFUSED and the answer is settled. A pass that
	// recorded either fault as plain unhealthiness would answer
	// TEMPORARILY_UNAVAILABLE here.
	assertUnroutable(t, resolveCataloged(t, fx),
		forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
		"while the exchange is both refused and unreachable")
	assertSkipLogged(t, fx, mockExchangeDomain, "refused")
}
