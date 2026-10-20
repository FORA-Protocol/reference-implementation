//go:build integration

package transport_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// TestDiscover_EveryGroupCarriesExchangeDiscoveryMethod pins that every
// OfferGroup the Exchange emits says how the resource was found. The Exchange
// answers only out of its own catalog, so the answer is always
// DISCOVERY_METHOD_EXCHANGE — including on the two branches that carry no
// offer, which is where an "only set it on the happy path" implementation
// leaves the field at UNSPECIFIED.
//
// The absent-field case is not a cosmetic gap. An agent that queries this
// Exchange directly reads the method from this ResourceResponse, so a field
// missing here is a field that agent never sees. The Broker's Resolve path
// does not read this value at all — it states how IT found the URI — so this
// RPC is the only place the Exchange's own answer is decided.
//
// Round-trip: pushed through the public CatalogService.PushResources surface,
// read back through the public ExchangeService.DiscoverResources surface. No
// DB, no sqlc, no service call — one harness, all three groupFor branches.
func TestDiscover_EveryGroupCarriesExchangeDiscoveryMethod(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	client := setupTermContributor(t, h, callerID)

	// A term nothing gates: any requester is entitled to it, so discovery
	// yields an offer.
	pushTerms(t, h, client, callerID, "/method/open", scopedTerm("open"))
	// A term gated on a scope the requester does not hold: the entry is in the
	// catalog, but no term survives Select, so there is no priced term and
	// therefore no offer. The group states no reason, so the scope is not
	// named (catalog_scope_integration_test.go pins that behaviour). The group
	// is still returned, bound to its URI.
	pushTerms(t, h, client, callerID, "/method/gated", scopedTerm("gated", "dist:US"))

	cases := []struct {
		name       string
		uri        string
		spec       requesterSpec
		wantOffers int
		wantReason forav1.OfferAbsenceReason
	}{
		{
			name:       "catalog hit: the group carries an offer",
			uri:        "https://" + h.publisherDom + "/method/open",
			spec:       requesterWithScopes("agent-method-open"),
			wantOffers: 1,
		},
		{
			name:       "catalog hit, scope covers no term: the group is present, empty and states no reason",
			uri:        "https://" + h.publisherDom + "/method/gated",
			spec:       requesterWithScopes("agent-method-gated"),
			wantOffers: 0,
		},
		{
			name:       "catalog miss: the group carries the typed absence reason",
			uri:        "https://" + h.publisherDom + "/method/never-pushed",
			spec:       requesterWithScopes("agent-method-miss"),
			wantOffers: 0,
			wantReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups := discoverAs(t, h, tc.uri, tc.spec).GetOfferGroups()
			if len(groups) != 1 {
				t.Fatalf("offer_groups = %d, want 1 (one requested URI)", len(groups))
			}
			group := groups[0]
			// Assert the branch first: a case that silently stopped exercising
			// the branch it names would still pass the method assertion.
			if got := len(group.GetOffers()); got != tc.wantOffers {
				t.Fatalf("offers = %d, want %d — wrong branch of groupFor", got, tc.wantOffers)
			}
			if got := group.GetAbsenceReason(); got != tc.wantReason {
				t.Fatalf("absence_reason = %v, want %v", got, tc.wantReason)
			}
			want := forav1.DiscoveryMethod_DISCOVERY_METHOD_EXCHANGE
			if got := group.GetDiscoveryMethod(); got != want {
				t.Errorf("discovery_method = %v, want %v", got, want)
			}
		})
	}
}
