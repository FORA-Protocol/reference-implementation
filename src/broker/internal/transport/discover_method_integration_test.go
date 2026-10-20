//go:build integration

package transport_test

import (
	"context"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// The method on groups for URIs the agent named (EXCHANGE, on an offer group and
// an absence group alike) and for URIs the Broker's catalog search chose
// (SEARCH, although the Exchange said EXCHANGE) is tested against real
// Exchanges by tests/e2e/harness/test_broker_discovery_method.py. The two tests
// here need an Exchange behaviour no real one has, or a Broker configured
// without catalog search.

// TestResolve_QueryOnlyRequestYieldsNoGroups is what the query path does in
// production when the Broker's catalog search is not enabled, which is the
// default. The enabled path is covered in query_discovery_integration_test.go.
//
// ResourceQuery has no free-text field, so the Broker's broadcast path relays a
// query carrying zero URIs, and a compliant Exchange refuses exactly that with
// "at least one uri required". Every routed exchange therefore fails, which is
// the allUpstreamFailed condition, and the batch answers TEMPORARILY_UNAVAILABLE
// with no groups. No agent receives a discovery method on this path at all.
//
// The mock is switched to the compliant behaviour for this test only; it answers
// a URI-less query by default, and sixteen pre-existing query suites depend on
// that.
//
// Needs an e2e test: a free-text Resolve with catalog search disabled, refused by every real Exchange.
func TestResolve_QueryOnlyRequestYieldsNoGroups(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})
	fx.exchange.rejectURILessQuery = true

	_, out := resolveOverConnect(t, fx, reqOpts{
		query:       "FORA intro",
		budgetMinor: 10000,
	})

	if groups := out.GetOfferGroups(); len(groups) != 0 {
		t.Errorf("offer_groups = %d, want 0 — a compliant Exchange refuses the URI-less query "+
			"the broadcast path sends, so no group reaches the agent", len(groups))
	}
	want := forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE
	if got := out.GetAbsenceReason(); got != want {
		t.Errorf("absence_reason = %v, want %v — every upstream refused", got, want)
	}
}

// TestResolve_BroadcastGroupsAreStampedSearch pins the Broker's stamping rule for
// the branch it takes when the agent names no URI: whatever that branch returns
// is labelled SEARCH, never EXCHANGE. Swap the constant in discover and this
// fails; that is the regression it guards, and the only one.
//
// It does NOT show the search-provider path working. Reaching the stamp needs
// an Exchange that answers a URI-less query, which no compliant Exchange does —
// the test above is the production behaviour. This one uses the mock's
// default, non-compliant answer purely to get a group in front of the stamping
// code, so treat it as a unit test of that rule wearing an integration test's
// clothes.
//
// Needs an e2e test: the SEARCH stamp on the broadcast branch; impossible while no real Exchange answers a URI-less query.
func TestResolve_BroadcastGroupsAreStampedSearch(t *testing.T) {
	ctx := context.Background()
	fx := newFixture(t, ctx, fixtureOpts{providerDomain: "acme.example"})

	_, out := resolveOverConnect(t, fx, reqOpts{
		query:       "FORA intro",
		budgetMinor: 10000,
	})

	groups := out.GetOfferGroups()
	if len(groups) == 0 {
		t.Fatalf("offer_groups is empty — the fixture produced no group to read the stamp off")
	}
	want := forav1.DiscoveryMethod_DISCOVERY_METHOD_SEARCH
	for i, group := range groups {
		if got := group.GetDiscoveryMethod(); got != want {
			t.Errorf("group %d (%s): discovery_method = %v, want %v — the Broker chose this URI, "+
				"the agent never named it", i, group.GetUri(), got, want)
		}
	}
}
