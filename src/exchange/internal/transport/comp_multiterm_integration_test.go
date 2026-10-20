//go:build integration

package transport_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/shopspring/decimal"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/comptest"
)

// TestComp_MultitermTermIndex pins the "one offer = one term = one CoMP
// package" invariant under scope-gated multi-term resources, observed
// END-TO-END through DiscoverResources only (no DB / internal-state access —
// Testing Doctrine pt 9). Every leg pushes terms via
// CatalogService.PushResources and reads the rendered comp packages back
// through the public discovery RPC, asserting that each offer's comp.id carries
// the ORIGINAL stored publisher index of the term that offer sells (matrix Q8:
// "<resource_id>#<term-index>") and that its comp.scope pricing is that same
// term's price.
//
// Two terms ride on each resource in a fixed publisher (stored) order; the
// requester's scopes drive which subset selectTerms keeps. Each surviving term
// becomes its own signed offer, in stored order, and its ORIGINAL stored index
// — not a per-offer ordinal, never a hardcoded 0 — appears in that offer's
// comp.id. Eligibility filtering therefore leaves gaps in the indexes an answer
// carries, which is the point: the index names the publisher's term, not the
// position of the offer in the response.
func TestComp_MultitermTermIndex(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	h.publisher.setContributors(callerID)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	h.publishAgent(t, callerID, priv)
	client := h.signedCat(callerID, priv)

	const premiumScope = "subscription:premium"

	// LEG 1 — a filtered-out first term leaves the index gap visible. Resource
	// stored [PREMIUM(index 0, scopes=[subscription:premium], $0.50),
	//  PUBLIC(index 1, empty scopes, $0.05)]. A DEFAULT requester (no scopes)
	// WITH SupportedProfiles=[fora-comp-v1]: Select drops PREMIUM (uncovered)
	// and keeps PUBLIC, so the single offer sells the term stored at index 1.
	//
	// Round-trip: write goes RPC(PushResources)->service->repo->DB; read goes
	// RPC(DiscoverResources)->service->Select->comp render->RPC response, then
	// the parity leg re-drives RPC(ExecuteTransaction), which verifies the
	// presented signed offer bytes. Both legs traverse the full protocol
	// surface; no leg observes state past a layer.
	t.Run("filtered_first_term_leaves_stored_index_1", func(t *testing.T) {
		const path = "/articles/comp-premium-first"
		premium := premiumPricedTerm(premiumScope, 0.50)
		public := seedPricedTerm() // empty scopes, PER_UNIT $0.05
		pushTerms(t, h, client, callerID, path, premium, public)

		uri := "https://" + h.publisherDom + path
		// DEFAULT requester: discoverCompOffer sends no requester scopes.
		offer := discoverCompOffer(t, h, uri)

		// The only eligible term is PUBLIC at ORIGINAL stored index 1.
		assertCompPackage(t, offer, compPackageID(h, uri, 1), 0.05)
		assertTransactParity(t, h, offer)
	})

	// LEG 2 — both terms eligible, premium stored first. SAME storage order as
	// LEG 1 [PREMIUM(0), PUBLIC(1)]. A PREMIUM requester
	// (scopes=[subscription:premium]) WITH the profile: Select keeps BOTH in
	// stored order, so discovery answers with TWO offers — premium at stored
	// index 0 priced $0.50, public at stored index 1 priced $0.05 — and each
	// one executes on its own.
	t.Run("both_terms_eligible_premium_stored_first", func(t *testing.T) {
		const path = "/articles/comp-premium-first-prem-req"
		premium := premiumPricedTerm(premiumScope, 0.50)
		public := seedPricedTerm()
		pushTerms(t, h, client, callerID, path, premium, public)

		uri := "https://" + h.publisherDom + path
		offers := discoverCompOffers(t, h, uri, premiumScope)
		if len(offers) != 2 {
			t.Fatalf("discover (comp profile) %s: got %d offers, want 2 (one per eligible term)", uri, len(offers))
		}
		offerTerms(t, uri, offers)

		assertCompPackage(t, offers[0], compPackageID(h, uri, 0), 0.50)
		assertCompPackage(t, offers[1], compPackageID(h, uri, 1), 0.05)
		for _, o := range offers {
			assertTransactParityScoped(t, h, o, premiumScope)
		}
	})

	// LEG 3 — both terms eligible, public stored first. Resource stored
	// [PUBLIC(index 0, empty scopes, $0.05),
	//  PREMIUM(index 1, scopes=[subscription:premium], $0.50)]. A PREMIUM
	// requester keeps BOTH, so the two offers come back in stored order with
	// the indexes and prices swapped relative to LEG 2. This pins that no
	// comp package bleeds across terms: each offer's package reflects the term
	// that offer sells and nothing about its sibling.
	t.Run("both_terms_eligible_public_stored_first", func(t *testing.T) {
		const path = "/articles/comp-public-first"
		public := seedPricedTerm() // stored index 0
		premium := premiumPricedTerm(premiumScope, 0.50)
		pushTerms(t, h, client, callerID, path, public, premium)

		uri := "https://" + h.publisherDom + path
		offers := discoverCompOffers(t, h, uri, premiumScope)
		if len(offers) != 2 {
			t.Fatalf("discover (comp profile) %s: got %d offers, want 2 (one per eligible term)", uri, len(offers))
		}
		offerTerms(t, uri, offers)

		assertCompPackage(t, offers[0], compPackageID(h, uri, 0), 0.05)
		assertCompPackage(t, offers[1], compPackageID(h, uri, 1), 0.50)
		for _, o := range offers {
			assertTransactParityScoped(t, h, o, premiumScope)
		}
	})
}

// premiumPricedTerm builds a scope-gated ENUMERATED term carrying the given
// entitlement scope and a DISTINCT PER_UNIT rate (so the premium offer's comp
// pricing is observably different from the public term's $0.05). It reuses
// the shared scopedTerm builder (single home for scope-gated terms) and only
// overrides the rate — keeping one term-construction path (jscpd=0) while making
// the price discriminating for the per-offer assertions.
func premiumPricedTerm(scope string, rate float64) *forav1.LicenseTerm {
	term := scopedTerm("premium", scope)
	// Rate crosses the wire as a canonical decimal STRING (exact-decimal money
	// spine), not a float64. Render the test's float rate through the spine's
	// FormatMoney so the term carries the same canonical form the production
	// ingest/sign path emits.
	wire, err := helpers.FormatMoney(decimal.NewFromFloat(rate))
	if err != nil {
		panic("premiumPricedTerm: format rate: " + err.Error())
	}
	term.GetPricing().Rate = wire
	return term
}

// assertCompPackage verifies the comp package one offer carries reflects the
// term that offer sells: comp.id == wantID ("<resource_id>#<index>" with the
// term's ORIGINAL stored publisher index, matrix Q8, derived by the caller via
// compPackageID — the package id is resource-intrinsic, not the offer's random
// per-offer offer_id) and comp.scope.unitprice == wantRate (that term's price).
// It also asserts the emitted comp Struct passes comptest.Validate (a real CoMP
// parser accepts it).
func assertCompPackage(t *testing.T, o *forav1.Offer, wantID string, wantRate float64) {
	t.Helper()
	compVal, ok := o.GetExt().GetFields()["comp"]
	if !ok || compVal == nil {
		t.Fatalf("offer.ext has no %q key under fora-comp-v1: ext=%v", "comp", o.GetExt())
	}
	comp := compVal.GetStructValue()
	if comp == nil {
		t.Fatalf("offer.ext[comp] is not a Struct: %v", compVal)
	}
	fields := comp.GetFields()

	if got := fields["id"].GetStringValue(); got != wantID {
		t.Errorf("comp.id = %q, want %q (the sold term's ORIGINAL stored index, matrix Q8)", got, wantID)
	}

	scope := fields["scope"].GetStructValue()
	if scope == nil {
		t.Fatalf("comp.scope missing or not a Struct: %v", fields["scope"])
	}
	if got := scope.GetFields()["unitprice"].GetNumberValue(); got != wantRate {
		t.Errorf("comp.scope.unitprice = %v, want %v (the sold term's price)", got, wantRate)
	}

	if err := comptest.Validate(comp); err != nil {
		t.Errorf("comptest.Validate(offer.ext[comp]) = %v, want nil", err)
	}
}
