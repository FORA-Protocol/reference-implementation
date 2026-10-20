//go:build integration

package transport_test

import (
	"context"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// What the budget gate counts. The Broker's budget pre-flight (costFixed),
// ranking (selection.unitCost) and audit (candidatesToInfo) all read one
// per-unit charge: Pricing.unit_cost when present, else Pricing.rate, which is
// what the Exchange bills. The gate projects that charge times
// estimated_quantity.
//
// Which charge a budget compares is the Broker's own behaviour: the pinned
// protocol does not specify it, so no e2e obligation grades the offers a real
// Exchange issues. What stays here is this Broker's choice for the one offer
// shape no real Exchange issues, an offer with no unit_cost at all: the
// Exchange always projects unit_cost, falling back to the rate itself.
//
// Round-trip: signed Connect client -> RFC 9421 httpsig middleware -> Broker
// Resolve handler -> resolve core (DiscoverResources against the mock Exchange
// -> selection -> checkBudget) -> typed DiscoveryResponse, observed through that
// same Resolve RPC.

// TestResolve_BudgetGate_UnitCostAbsent_ProjectsRate pins the fallback parity:
// when an offer omits unit_cost, the canonical charge falls back to rate — the
// same fallback the Exchange's catalog_pricing applies (unit_cost<-rate) and the
// same fallback selection.unitCost already uses for ranking. With unit_cost
// absent and rate ($0.20) ABOVE the limit ($0.10), the gate must DENY. This case
// stays green on HEAD (costFixed already reads rate); after the fix it stays
// green because OfferCharge falls back to rate when unit_cost is unset — pinning
// that the source-of-truth split does NOT regress the absent-unit_cost path.
//
// Needs an e2e test: a real offer with no unit_cost, metered by the budget gate on its rate.
func TestResolve_BudgetGate_UnitCostAbsent_ProjectsRate(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})
	// The mock's offer carries the price as unit_cost and rate alike; the
	// absent-unit_cost case is its rate fallback. $0.20 is above the $0.10 cap.
	fx.exchange.offerUnitCost = 0.20
	// Pin estimated_quantity = 1 to isolate the rate-fallback field selection from
	// the quantity multiply (fixture default is 42; the gate now projects unit_cost * qty).
	fx.exchange.offerEstimatedQuantity = 1

	_, out := resolveOverConnect(t, fx, reqOpts{
		uri:         "https://acme.example/article-42",
		budgetMinor: 10, // $0.10 cap (cents) < $0.20 charge
	})

	if got := out.GetAbsenceReason(); got != forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_AUTHORIZED {
		t.Fatalf("rate-projected charge ($0.20) exceeds the $0.10 limit but resolve was allowed: "+
			"absence_reason = %v, want NOT_AUTHORIZED", got)
	}
}
