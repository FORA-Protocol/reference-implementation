//go:build integration

package transport_test

import (
	"fmt"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// scopedTerm builds an ENUMERATED term whose only gating is scope coverage: no
// machine restrictions, a minimal valid Pricing, and the given term scopes. The
// scope helpers live here (the single home for the scope family) and reuse the
// shared selectPricing/labels/assertStrs helpers per the epic Design (Doctrine
// pt7) — promote them only when a second file needs them.
func scopedTerm(label string, scopes ...string) *forav1.LicenseTerm {
	l := label
	return &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing:   selectPricing(),
		PartLabel: &l,
		Scopes:    scopes,
	}
}

// requesterWithScopes builds a Requester carrying the Biscuit-authority scopes
// selectTerms reads from the native Requester.scopes field (distinct from the
// ext facets requesterWithExt populates). No user_type/geography facets are set, so
// a scope-only term — which carries no restrictions — is gated purely on scope
// coverage.
func requesterWithScopes(id string, scopes ...string) requesterSpec {
	return requesterSpec{requester: newRequester(id, "agent.example", scopes...)}
}

// TestDiscover_SelectByScope pins scope/subscription gating
// (service/termselect.go: selectTerms -> scopesCovered/scopeCovered) through
// the public DiscoverResources RPC, the single observation surface. Each row
// pushes ONE scope-gated term and discovers it as a requester with a given
// Biscuit-authority scope set; a kept term projects on a single offer with the
// exact part_label, an uncovered term yields NO offer (all-terms-filtered =>
// no eligible priced term => no offer — never an empty-terms stub).
//
// The matrix exercises every branch of scopeCovered: exact equality, the global
// "*", the hierarchical "prefix:*", exclusion of an uncovered scope, and the
// AND-coverage requirement of scopesCovered (every term scope must be covered).
func TestDiscover_SelectByScope(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	client := setupTermContributor(t, h, callerID)

	cases := []struct {
		name       string
		path       string
		termScopes []string
		reqScopes  []string
		keep       bool
	}{
		{
			name:       "exact: requester dist:US covers term dist:US (scopeCovered exact)",
			path:       "/scope/exact",
			termScopes: []string{"dist:US"},
			reqScopes:  []string{"dist:US"},
			keep:       true,
		},
		{
			name:       "absent: requester with no scope cannot cover term dist:US (scopeCovered)",
			path:       "/scope/absent",
			termScopes: []string{"dist:US"},
			reqScopes:  nil,
			keep:       false,
		},
		{
			name:       "hierarchical: requester dist:* covers term dist:US (scopeCovered prefix)",
			path:       "/scope/hierarchical",
			termScopes: []string{"dist:US"},
			reqScopes:  []string{"dist:*"},
			keep:       true,
		},
		{
			name:       "global: requester * covers term dist:US (scopeCovered global)",
			path:       "/scope/global",
			termScopes: []string{"dist:US"},
			reqScopes:  []string{"*"},
			keep:       true,
		},
		{
			name:       "AND-coverage partial: dist:* alone cannot cover [dist:US, sub:premium] (scopesCovered)",
			path:       "/scope/and-partial",
			termScopes: []string{"dist:US", "sub:premium"},
			reqScopes:  []string{"dist:*"},
			keep:       false,
		},
		{
			name:       "AND-coverage full: dist:* + sub:premium cover [dist:US, sub:premium] (scopesCovered)",
			path:       "/scope/and-full",
			termScopes: []string{"dist:US", "sub:premium"},
			reqScopes:  []string{"dist:*", "sub:premium"},
			keep:       true,
		},
		{
			name:       "public: empty term scopes are covered for a scopeless requester (scopesCovered public)",
			path:       "/scope/public",
			termScopes: nil,
			reqScopes:  nil,
			keep:       true,
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			uri := "https://" + h.publisherDom + tc.path
			pushTerms(t, h, client, callerID, tc.path, scopedTerm("scoped", tc.termScopes...))
			req := requesterWithScopes(fmt.Sprintf("agent-scope-%d", i), tc.reqScopes...)

			if tc.keep {
				got := discoverTermsAs(t, h, uri, req)
				assertStrs(t, tc.name, labels(got), []string{"scoped"})
				return
			}
			if offers := discoverOffersAs(t, h, uri, req); len(offers) != 0 {
				t.Fatalf("%s: offers = %d, want 0 (scope not covered => no eligible priced term => no offer)", tc.name, len(offers))
			}
		})
	}
}

// TestDiscover_ScopeShortStatesNoReason pins the answer to a requester whose
// scopes cover none of a resource's terms: the group carries no offer and NO
// absence reason. In particular it never says
// OFFER_ABSENCE_REASON_SCOPE_INSUFFICIENT. The Requester.scopes comment in the
// pinned protocol module is the rule: a resource outside the requester's scopes
// is not returned, and the requester never learns it exists. A reason naming
// the scope would tell it exactly that.
//
// Three requesters ask about the same catalog state:
//
//   - one without the scope gets no offer and no reason;
//   - one holding the scope gets the offer for that same resource and no
//     reason. This is the control: the term is there to be sold, so the empty
//     group above is the scope's doing;
//   - one without the scope, asking about a resource with a gated term and a
//     public term, gets the public offer and no reason. Scope removed one
//     term, and the other is still offered.
//
// Round-trip: pushed through the public CatalogService.PushResources surface,
// read back through the public ExchangeService.DiscoverResources surface.
func TestDiscover_ScopeShortStatesNoReason(t *testing.T) {
	h := newPushHarness(t)
	callerID := "caller.example"
	client := setupTermContributor(t, h, callerID)

	const scope = "subscription:premium"
	pushTerms(t, h, client, callerID, "/scope/reason/gated", scopedTerm("gated", scope))
	pushTerms(t, h, client, callerID, "/scope/reason/mixed", scopedTerm("gated", scope), scopedTerm("public"))
	gated := "https://" + h.publisherDom + "/scope/reason/gated"
	mixed := "https://" + h.publisherDom + "/scope/reason/mixed"

	cases := []struct {
		name       string
		uri        string
		spec       requesterSpec
		wantLabels []string
	}{
		{
			name: "scope not held: no offer, no reason",
			uri:  gated,
			spec: requesterWithScopes("agent-reason-none"),
		},
		{
			name:       "scope held: the same resource is offered, no reason",
			uri:        gated,
			spec:       requesterWithScopes("agent-reason-held", scope),
			wantLabels: []string{"gated"},
		},
		{
			name:       "scope removes one of two terms: the public offer, no reason",
			uri:        mixed,
			spec:       requesterWithScopes("agent-reason-mixed"),
			wantLabels: []string{"public"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := discoverAs(t, h, tc.uri, tc.spec)
			groups := resp.GetOfferGroups()
			if len(groups) != 1 {
				t.Fatalf("offer_groups = %d, want 1 (one requested URI)", len(groups))
			}
			group := groups[0]
			assertStrs(t, tc.name, labels(offerTerms(t, tc.uri, group.GetOffers())), tc.wantLabels)
			if got := len(resp.GetOffers()); got != len(tc.wantLabels) {
				t.Fatalf("flat offers = %d, want %d (the same offers as the group)", got, len(tc.wantLabels))
			}
			if group.AbsenceReason != nil {
				t.Fatalf("absence_reason = %v, want unset: a scope that covers no term is never named",
					group.GetAbsenceReason())
			}
		})
	}
}
