// ADR-008 D2 obligation tests for the Broker-side absence_reason
// vocabulary. Each test asserts on the specific enum value (NOT on
// len(offers)==0 / truthiness) so a regression that collapses all
// causes to one value is caught at the helper boundary.
//
// W1 of the proto-rename wave dropped the project-specific enum values
// (NO_HEALTHY_EXCHANGE, NO_AGENT_ENTITLEMENT, GRANTS_DO_NOT_COVER,
// UNKNOWN_RESOURCE, INTERNAL_ERROR, OUTSTANDING_OBLIGATIONS,
// BILLING_BLOCK, SUBSCRIPTION_EXPIRED) in favour of the canonical
// FORA-protocol vocabulary (NOT_IN_CATALOG, NOT_AUTHORIZED,
// TEMPORARILY_UNAVAILABLE). The assertions below
// follow the new mapping documented on
// `pickResolveAbsenceReason` in resolve_responses.go.

package resolve

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// TestPickResolveAbsenceReason exhaustively covers the branches of
// pickResolveAbsenceReason after the W1 proto rename. The precedence
// order mirrors the production reasoning: transient upstream-wide
// failures beat refusals, upstream-propagated reasons beat the
// broker-local fallbacks.
func TestPickResolveAbsenceReason(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		flags discoverFlags
		want  forav1.OfferAbsenceReason
	}{
		{
			name: "TEMPORARILY_UNAVAILABLE: every upstream call failed",
			flags: discoverFlags{
				allUpstreamFailed: true,
				// upstreamReason cannot suppress the transient-upstream
				// verdict — ADR-008 D2 rule.
				upstreamReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_AUTHORIZED,
			},
			want: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE,
		},
		{
			name: "upstream NOT_IN_CATALOG propagates",
			flags: discoverFlags{
				upstreamReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
			},
			want: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
		},
		{
			name: "upstream NOT_AUTHORIZED propagates",
			flags: discoverFlags{
				upstreamReason: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_AUTHORIZED,
			},
			want: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_AUTHORIZED,
		},
		{
			name: "TEMPORARILY_UNAVAILABLE: a named exchange was down, nothing else set",
			flags: discoverFlags{
				namedExchangeDown: true,
			},
			want: forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE,
		},
		{
			// An Exchange answers a requester whose scopes cover nothing with an
			// empty group and no reason, so this is also the scope-short answer.
			name:  "NOT_IN_CATALOG: nothing else set (catalog miss, or nothing to offer)",
			flags: discoverFlags{},
			want:  forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := pickResolveAbsenceReason(tc.flags)
			if got != tc.want {
				t.Fatalf("absence_reason = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestProbeRefusalResponse covers the absence reason the Broker answers when no
// probe yielded a usable manifest, over every mix of the three outcomes.
//
// The split it pins is settled versus transient. A publisher that served no
// manifest, and one whose manifest carries a document version this Broker does
// not implement, have both ANSWERED: the next attempt reads the same thing, so
// the refusal is the canonical NOT_IN_CATALOG. Only when every failure was a
// peer that did not answer is TEMPORARILY_UNAVAILABLE the honest reply, and the
// mixed rows are the ones that matter — a settled outcome beside a transient one
// must not be softened into "try again", because the agent's retry would find
// the same publishers refusing for the same reason.
func TestProbeRefusalResponse(t *testing.T) {
	t.Parallel()
	const (
		notInCatalog = forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_NOT_IN_CATALOG
		unavailable  = forav1.OfferAbsenceReason_OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE
	)
	for name, tc := range map[string]struct {
		outcome probeOutcome
		want    forav1.OfferAbsenceReason
	}{
		"no manifest at all":           {probeOutcome{missing: 1}, notInCatalog},
		"a version this build refuses": {probeOutcome{refused: 1}, notInCatalog},
		"every publisher unreachable":  {probeOutcome{transient: 1}, unavailable},
		"refused beside unreachable":   {probeOutcome{refused: 1, transient: 1}, notInCatalog},
		"missing beside unreachable":   {probeOutcome{missing: 1, transient: 1}, notInCatalog},
		"missing beside refused":       {probeOutcome{missing: 1, refused: 1}, notInCatalog},
		"nothing observed at all":      {probeOutcome{}, notInCatalog},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := probeRefusalResponse(tc.outcome).AbsenceReason; got != tc.want {
				t.Errorf("absence reason = %v, want %v", got, tc.want)
			}
		})
	}
}
