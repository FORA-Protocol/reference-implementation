// Package selection implements the Broker's offer deduplication and ranking
// logic. Handlers fan out DiscoverResources across exchanges, collect the
// returned Offers, then call Rank() to produce a deterministic selection
// order. The first entry is the chosen winner; the rest are alternates for
// audit and logging.
package selection

import (
	"context"
	"sort"
	"strings"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/repo"
)

// ExchangeRef annotates an Offer with its originating exchange so the
// Broker can record the routing decision.
type ExchangeRef struct {
	Domain   string
	Endpoint string
	Trust    string // DISCOVERED / VERIFIED / PREFERRED / BLOCKED
	Priority int32
}

// Candidate pairs a VERIFIED offer with the exchange it came from. The field
// is core.VerifiedOffer — the SDK's unforgeable wrapper — so "only offers that
// passed the fail-closed Verifier are rankable" is a COMPILE guard: the only
// way to build a Candidate is through the verify path (or the audit-visible
// RejectedOffer.Unsafe escape).
type Candidate struct {
	Offer    core.VerifiedOffer
	Exchange ExchangeRef
}

// Dedup collapses candidates that offer the SAME licensing alternative for the
// same resource, keeping the cheapest (lowest unit_cost) of each.
//
// An offer sells exactly one license term, so a publisher that licenses one
// article two ways (free for academic use, paid per unit) is discovered as two
// offers with the same canonical URL. Those are different products and both
// must reach the agent, which is why the key is the resource identity AND the
// term, not the identity alone. Two exchanges relaying the same resource under
// the same term still collapse to one candidate.
func Dedup(ctx context.Context, cands []Candidate) []Candidate {
	best := make(map[string]Candidate, len(cands))
	for _, c := range cands {
		key := dedupKey(ctx, c.Offer.Offer())
		existing, ok := best[key]
		if !ok {
			best[key] = c
			continue
		}
		if OfferCharge(c.Offer.Offer()).LessThan(OfferCharge(existing.Offer.Offer())) {
			best[key] = c
		}
	}
	out := make([]Candidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	return out
}

// Rank returns the candidates in winner-first order.
//
// Ordering:
//  1. Higher trust_level_priority wins  (PREFERRED > VERIFIED > DISCOVERED)
//  2. Then lower unit_cost wins
//  3. Then higher exchange_priority wins
//  4. Then stable by offer_id for determinism
func Rank(cands []Candidate) []Candidate {
	sort.SliceStable(cands, func(i, j int) bool {
		ti, tj := trustWeight(cands[i].Exchange.Trust), trustWeight(cands[j].Exchange.Trust)
		if ti != tj {
			return ti > tj
		}
		ci, cj := OfferCharge(cands[i].Offer.Offer()), OfferCharge(cands[j].Offer.Offer())
		if !ci.Equal(cj) {
			return ci.LessThan(cj)
		}
		if cands[i].Exchange.Priority != cands[j].Exchange.Priority {
			return cands[i].Exchange.Priority > cands[j].Exchange.Priority
		}
		return cands[i].Offer.Offer().GetOfferId() < cands[j].Offer.Offer().GetOfferId()
	})
	return cands
}

// trustWeight orders the trust levels for ranking — a different question from
// repo.Exchange.Admission, which only answers whether a row is routable at all.
// A BLOCKED offer reaching here already escaped that gate, and the negative
// weight sinks it rather than refusing it.
func trustWeight(level string) int {
	switch strings.ToUpper(level) {
	case repo.TrustLevelPreferred:
		return 3
	case repo.TrustLevelVerified:
		return 2
	case repo.TrustLevelDiscovered:
		return 1
	case repo.TrustLevelBlocked:
		return -1
	default:
		return 0
	}
}

// OfferCharge is the EXACT decimal charge for an offer: its unit_cost when
// present, else its rate. This is the SINGLE source of truth for what the agent
// is billed — the Exchange charges unit_cost (rate is the provider's own model
// price, normalized to unit_cost; catalog_pricing falls back unit_cost<-rate
// only when a term omits unit_cost). Both offer ranking (Dedup/Rank) AND the
// broker budget pre-flight read this one function, so the gate can never admit
// or deny against a different price than the one selection ranks and the agent
// pays. Money rides the wire as a canonical decimal string; it is parsed once
// here via helpers.ParseMoney so comparisons are exact decimals (decimal.Cmp),
// never float. A nil offer/pricing, an unset (nil) unit_cost, or an
// empty/unparseable string all map to decimal.Zero — preserving the prior
// zero-value ranking semantics for offers without a price. (Empty must stay loud
// on the billing/mapper paths; here, on the ranking/budget path only, it is
// deliberately treated as zero.)
func OfferCharge(o *forav1.Offer) decimal.Decimal {
	if o == nil || o.GetPricing() == nil {
		return decimal.Zero
	}
	if uc := o.GetPricing().UnitCost; uc != nil {
		return parseMoneyOrZero(*uc)
	}
	return parseMoneyOrZero(o.GetPricing().GetRate())
}

func parseMoneyOrZero(s string) decimal.Decimal {
	d, err := helpers.ParseMoney(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

// dedupKey names one licensing alternative: the resource identity plus the
// exact terms the offer carries. Term equality is EXACT — the deterministic
// protobuf encoding of each term, which covers rights, restrictions, scopes,
// obligations and pricing without a hand-written comparator. Two terms that
// mean the same thing but encode differently (a reordered repeated field, a
// differently spelled money string) therefore stay separate candidates; that is
// deliberate, because keeping an extra alternative is safe and dropping a real
// one is not.
//
// An offer with no term keys on identity alone. An offer with several terms
// keys on all of them: the protocol bounds an offer to one term, so such an
// offer only appears while an older Exchange is still fusing terms, and keying
// on the whole set keeps two differently fused offers apart.
func dedupKey(ctx context.Context, o *forav1.Offer) string {
	key := identityKey(o)
	terms := o.GetTerms()
	if len(terms) == 0 {
		return key
	}
	opts := proto.MarshalOptions{Deterministic: true}
	var b strings.Builder
	b.WriteString(key)
	for _, t := range terms {
		raw, err := opts.Marshal(t)
		if err != nil {
			// Encoding a term the Exchange already signed should not fail. If it
			// does, fall back to the identity-only key: that can merge two real
			// alternatives, which costs the agent a choice, where a panic would
			// cost it the whole discovery.
			reqctx.FromContext(ctx).WarnContext(ctx, "broker.selection.term_encoding_failed",
				"offer_id", o.GetOfferId(), "err", err)
			return key
		}
		b.WriteString("|")
		b.Write(raw)
	}
	return b.String()
}

func identityKey(o *forav1.Offer) string {
	if o == nil {
		return ""
	}
	ident := o.GetIdentity()
	if ident != nil {
		if v := ident.CanonicalUrl; v != nil && *v != "" {
			return "url:" + *v
		}
		if v := ident.Doi; v != nil && *v != "" {
			return "doi:" + *v
		}
		if v := ident.IptcGuid; v != nil && *v != "" {
			return "iptc:" + *v
		}
		if v := ident.ContentHash; v != nil && *v != "" {
			return "hash:" + *v
		}
	}
	return "offer:" + o.GetOfferId()
}
