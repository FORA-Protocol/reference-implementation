package selection_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/core"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/shopspring/decimal"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/broker/internal/selection"
)

// offerWith builds an offer whose unit_cost is the canonical wire decimal STRING
// for the given amount (money-as-string). The float argument is a
// test-authoring convenience; FormatMoney renders it to the same canonical form
// the production wire carries (e.g. 0.05 -> "0.05", 0.10 -> "0.1").
func offerWith(id, canonical string, unitCost float64) *forav1.Offer {
	u, err := helpers.FormatMoney(decimal.NewFromFloat(unitCost))
	if err != nil {
		panic(err)
	}
	url := canonical
	return &forav1.Offer{
		OfferId: id,
		Pricing: &forav1.Pricing{UnitCost: &u},
		Identity: &forav1.ResourceIdentity{
			CanonicalUrl: &url,
		},
	}
}

// termWith builds a license term whose pricing matches the offer that carries
// it. A term carries its own price, so two offers with byte-identical terms
// always cost the same — the tests below never pair an identical term with a
// different offer price.
func termWith(model forav1.PricingModel, unitCost float64, scopes ...string) *forav1.LicenseTerm {
	u, err := helpers.FormatMoney(decimal.NewFromFloat(unitCost))
	if err != nil {
		panic(err)
	}
	return &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing:   &forav1.Pricing{Model: model, UnitCost: &u, Currency: "USD"},
		Scopes:    scopes,
	}
}

// oneArticle is the canonical URL every per-term case below licenses: the whole
// question these tests ask is what Dedup does with offers that share it.
const oneArticle = "https://a/article"

// offerSelling builds the shape discovery actually returns now: one offer
// selling exactly one license term, for oneArticle.
func offerSelling(id string, unitCost float64, term *forav1.LicenseTerm) *forav1.Offer {
	o := offerWith(id, oneArticle, unitCost)
	o.Terms = []*forav1.LicenseTerm{term}
	return o
}

// TestDedup_DifferentTermsForOneURLBothSurvive is the property the per-term
// offer model needs from Dedup: one article licensed two ways is two offers
// with the same canonical URL, and the agent must see both so it can choose.
func TestDedup_DifferentTermsForOneURLBothSurvive(t *testing.T) {
	free := termWith(forav1.PricingModel_PRICING_MODEL_FREE, 0, "academic")
	paid := termWith(forav1.PricingModel_PRICING_MODEL_PER_UNIT, 0.03)
	cands := []selection.Candidate{
		{
			Offer:    unsafeVerified(offerSelling("free", 0, free)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
		{
			Offer:    unsafeVerified(offerSelling("paid", 0.03, paid)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
	}
	got := selection.Dedup(t.Context(), cands)
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2 (two licensing alternatives for one URL)", len(got))
	}
}

// TestDedup_SameTermFromTwoExchangesCollapses covers the case Dedup exists for:
// two exchanges relaying the same resource under the same term is one product,
// not two. The terms are byte-identical, so both offers carry the same price
// and either survivor is correct.
func TestDedup_SameTermFromTwoExchangesCollapses(t *testing.T) {
	cands := []selection.Candidate{
		{
			Offer: unsafeVerified(offerSelling("mp1-offer", 0.03,
				termWith(forav1.PricingModel_PRICING_MODEL_PER_UNIT, 0.03))),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
		{
			Offer: unsafeVerified(offerSelling("mp2-offer", 0.03,
				termWith(forav1.PricingModel_PRICING_MODEL_PER_UNIT, 0.03))),
			Exchange: selection.ExchangeRef{Domain: "mp2", Trust: "VERIFIED"},
		},
	}
	got := selection.Dedup(t.Context(), cands)
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 (same resource, same term, two exchanges)", len(got))
	}
}

// TestDedup_TermCardinalityEdges pins the two shapes that are not "exactly one
// term": an offer with no terms at all, and a fused multi-term offer from an
// Exchange that has not been upgraded yet. Neither may panic, and a fused offer
// must not collapse into one of its own single-term parts.
func TestDedup_TermCardinalityEdges(t *testing.T) {
	free := termWith(forav1.PricingModel_PRICING_MODEL_FREE, 0, "academic")
	paid := termWith(forav1.PricingModel_PRICING_MODEL_PER_UNIT, 0.03)
	fused := offerWith("fused", oneArticle, 0)
	fused.Terms = []*forav1.LicenseTerm{free, paid}
	cands := []selection.Candidate{
		{
			Offer:    unsafeVerified(offerWith("termless", oneArticle, 0.05)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
		{
			Offer:    unsafeVerified(fused),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
		{
			Offer:    unsafeVerified(offerSelling("single", 0, free)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
	}
	got := selection.Dedup(t.Context(), cands)
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3 (termless, fused and single-term keys are all distinct)", len(got))
	}
}

// TestDedup_KeepsCheapestPerIdentity covers offers that carry no terms: those
// key on identity alone, and the cheapest of them survives.
func TestDedup_KeepsCheapestPerIdentity(t *testing.T) {
	cands := []selection.Candidate{
		{
			Offer:    unsafeVerified(offerWith("a1", "https://a/article", 0.05)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
		{
			Offer:    unsafeVerified(offerWith("a2", "https://a/article", 0.03)),
			Exchange: selection.ExchangeRef{Domain: "mp2", Trust: "VERIFIED"},
		},
		{
			Offer:    unsafeVerified(offerWith("b1", "https://b/article", 0.10)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED"},
		},
	}
	got := selection.Dedup(t.Context(), cands)
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	for _, c := range got {
		if c.Offer.Offer().GetIdentity().GetCanonicalUrl() == "https://a/article" &&
			c.Offer.Offer().GetOfferId() != "a2" {
			t.Errorf("expected cheapest a2 to survive, got %q", c.Offer.Offer().GetOfferId())
		}
	}
}

func TestRank_TrustBeatsCost(t *testing.T) {
	cands := []selection.Candidate{
		{
			Offer:    unsafeVerified(offerWith("cheap", "url-a", 0.01)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "DISCOVERED"},
		},
		{
			Offer:    unsafeVerified(offerWith("expensive", "url-b", 0.05)),
			Exchange: selection.ExchangeRef{Domain: "mp2", Trust: "PREFERRED"},
		},
	}
	got := selection.Rank(cands)
	if got[0].Offer.Offer().GetOfferId() != "expensive" {
		t.Errorf("winner = %q, want expensive (PREFERRED trust wins)", got[0].Offer.Offer().GetOfferId())
	}
}

func TestRank_TieBreakByCostThenPriority(t *testing.T) {
	cands := []selection.Candidate{
		{
			Offer:    unsafeVerified(offerWith("b", "url-a", 0.05)),
			Exchange: selection.ExchangeRef{Domain: "mp1", Trust: "VERIFIED", Priority: 1},
		},
		{
			Offer:    unsafeVerified(offerWith("a", "url-b", 0.03)),
			Exchange: selection.ExchangeRef{Domain: "mp2", Trust: "VERIFIED", Priority: 1},
		},
		{
			Offer:    unsafeVerified(offerWith("c", "url-c", 0.03)),
			Exchange: selection.ExchangeRef{Domain: "mp3", Trust: "VERIFIED", Priority: 5},
		},
	}
	got := selection.Rank(cands)
	if got[0].Offer.Offer().GetOfferId() != "c" {
		t.Errorf("winner = %q, want c (priority breaks cost tie)", got[0].Offer.Offer().GetOfferId())
	}
}

// unsafeVerified mints a core.VerifiedOffer for fixture construction via the
// SDK's explicit, audit-visible escape hatch (RejectedOffer.Unsafe) — it does
// NOT verify a signature. The name says "unsafe" so a reader never mistakes the
// fixture for a genuinely-verified offer: these unit tests exercise ranking
// semantics, not verification (the verify path is covered by the transport
// integration suite).
func unsafeVerified(o *forav1.Offer) core.VerifiedOffer {
	return core.RejectedOffer{Offer: o}.Unsafe()
}
