package service

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
)

// selectedTerm is one term the requester may be offered, with the delegation
// evidence that admitted it when the term is subscription-gated (nil otherwise).
// The evidence is exposed so a later slice can bound the offer's expiry by the
// chain's and store it with the transaction; nothing here bounds it yet.
type selectedTerm struct {
	term       *forav1.LicenseTerm
	delegation *verifiedSubscription
}

// selectTerms projects a publisher's terms down to those the requester may be
// offered. It is the SINGLE source of term-eligibility in the Exchange: the
// discovery path calls selectTerms before projecting terms onto Offer.terms and
// never reimplements this filtering itself. Token canonicalization and the
// ingest-tier term checks are the SDK's (sdk/go/helpers); this projection is the
// one piece of term logic the Exchange keeps.
//
// Two kinds of term, decided by the term's own scopes:
//
//   - A GATED term carries one concrete 'subscription:' scope. It is kept only
//     when the delegation chain in Requester.delegation verifies against the
//     subscription record that owns that scope (gate.verified: trusted root key,
//     intact chain, audience, expiry, holder bound to the key that signed this
//     request) and the chain's verified scopes cover ALL of the term's scopes.
//     Requester.scopes is ignored for a gated term: stating the scope proves
//     nothing. A term with more than one subscription scope, or one that breaks
//     the concrete-scope rule, is hidden for every requester and journaled;
//     ingest refuses such a term, this is the backstop.
//
//   - Every other term is kept when the scopes the requester states cover ALL
//     of its scopes under the protocol's segment-wise rule (helpers.ScopesCover:
//     a granted segment equals the required one or is '*', a terminal '*' covers
//     the remaining segments, a grant with more segments never covers a shorter
//     requirement, and no implicit prefix match). An empty term scopes[] is
//     public. A stated scope is the requester's own statement, signed and
//     attributable and not verified: it decides which terms are presented, and a
//     false statement is settled through reconciliation and the account
//     relationship.
//
// Restriction axes (USER_TYPE, GEOGRAPHY, FUNCTION) are NOT used to exclude
// terms here: they ride on the returned offer for the agent to self-honour and
// are enforced at accept→report→reconcile. A REFERENCE_ONLY term's machine
// fields are an advisory summary of the referenced License document and are not
// evaluated either.
//
// A nil requester selects nothing. selectTerms never mutates its inputs, and the
// terms it returns are the same pointers it was given, in stored order —
// storedIndex recovers the publisher's term index by pointer identity.
func selectTerms(
	terms []*forav1.LicenseTerm, requester *forav1.Requester, tenantID string, gate *discoveryGate,
) []selectedTerm {
	if requester == nil {
		return nil
	}
	stated := requester.GetScopes()
	out := make([]selectedTerm, 0, len(terms))
	for _, term := range terms {
		if term == nil {
			continue
		}
		scope, gated, err := exchange.SubscriptionScopeOf(term.GetScopes())
		switch {
		case err != nil:
			gate.hideMalformedTerm(tenantID, err)
		case gated:
			if vs := gate.verified(tenantID, scope); vs != nil && helpers.ScopesCover(vs.Scopes, term.GetScopes()) {
				out = append(out, selectedTerm{term: term, delegation: vs})
			}
		case helpers.ScopesCover(stated, term.GetScopes()):
			out = append(out, selectedTerm{term: term})
		}
	}
	return out
}
