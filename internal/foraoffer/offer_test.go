package foraoffer_test

import (
	"slices"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/gen/go/vocab/functiontokens"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraoffer"
)

// The fields both the Exchange and the preview depend on, pinned here because
// this is the one place either of them gets them from. Each assertion below is
// a decision, not a restatement of the code: the delivery method is what makes
// FORA hand an agent a signed URL rather than bytes, the mutability is a stated
// default the protocol refuses to leave unspecified, and the canonical address
// is what the execute path binds a signed offer to its catalog entry by.
func TestBuildSetsWhatBothCallersDependOn(t *testing.T) {
	t.Parallel()
	title := "Midnight Harbour"
	offer := foraoffer.Build(foraoffer.Resource{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/news/a",
		Title:        &title,
		Pricing:      &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE, Rate: "0", Currency: "EUR"},
		Terms: []*forav1.LicenseTerm{{
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
			Restrictions: []*forav1.Restriction{{
				Kind:      forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
				Permitted: []string{functiontokens.Search},
			}},
		}},
	})
	if offer.GetDeliveryMethod() != forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS {
		t.Fatalf("delivery method = %v, want instructions", offer.GetDeliveryMethod())
	}
	if got := offer.GetIdentity().GetResourceMutability(); got != forav1.ResourceMutability_RESOURCE_MUTABILITY_STATIC {
		t.Fatalf("mutability = %v, want static: the protocol refuses an unspecified value, "+
			"and neither caller has a per-resource source for it yet", got)
	}
	if got := offer.GetIdentity().GetCanonicalUrl(); got != "https://publisher.example/news/a" {
		t.Fatalf("canonical url = %q", got)
	}
	if offer.GetExchange() != "exchange.example" {
		t.Fatalf("exchange = %q", offer.GetExchange())
	}
	if offer.GetTitle() != title {
		t.Fatalf("title = %q, want %q", offer.GetTitle(), title)
	}
	if len(offer.GetTerms()) != 1 {
		t.Fatalf("terms = %d, want the one that was passed in", len(offer.GetTerms()))
	}
}

// What only an Exchange can issue is absent, and its absence is what keeps a
// preview from being mistaken for an offer and replayed. The Exchange sets
// these on top of what Build returns.
func TestBuildIssuesNothingOnlyAnExchangeCan(t *testing.T) {
	t.Parallel()
	offer := foraoffer.Build(foraoffer.Resource{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/news/a",
		Pricing:      &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE, Rate: "0", Currency: "EUR"},
	})
	if offer.GetOfferId() != "" {
		t.Fatalf("offer id = %q, want none: an Exchange assigns it", offer.GetOfferId())
	}
	if offer.GetExpiresAt() != nil {
		t.Fatal("an expiry was set: it is a window an Exchange commits to")
	}
	if offer.GetSignature() != "" {
		t.Fatal("a signature was set: it is the Exchange's statement that the price is its own")
	}
}

// A resource with no title yields an offer with none, rather than one carrying
// an empty string that an agent would render as a blank headline.
func TestBuildLeavesAnAbsentTitleAbsent(t *testing.T) {
	t.Parallel()
	offer := foraoffer.Build(foraoffer.Resource{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/news/a",
		Pricing:      &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE, Rate: "0", Currency: "EUR"},
	})
	if offer.Title != nil {
		t.Fatalf("title = %q, want none", offer.GetTitle())
	}
}

// An offer sells exactly one licensing term, and the pinned protocol enforces
// that bound in validation instead of leaving it to each caller: one term is
// valid, and zero or two terms are rejected with the rule that names the side
// of the bound that was crossed. A protocol version without the bound accepts
// all three counts, so this test fails if the pin goes back to such a version.
func TestValidationBoundsAnOfferToOneTerm(t *testing.T) {
	t.Parallel()
	// The term an offer carries is unpriced: the offer states its price once,
	// in Offer.pricing (the offer.terms.pricing_unset rule).
	term := func() *forav1.LicenseTerm {
		return &forav1.LicenseTerm{
			Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
			Restrictions: []*forav1.Restriction{{
				Kind:      forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
				Permitted: []string{functiontokens.Search},
			}},
		}
	}
	cases := []struct {
		name     string
		terms    []*forav1.LicenseTerm
		wantRule string
	}{
		{name: "no term", terms: nil, wantRule: "repeated.min_items"},
		{name: "one term", terms: []*forav1.LicenseTerm{term()}},
		{name: "two terms", terms: []*forav1.LicenseTerm{term(), term()}, wantRule: "repeated.max_items"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			offer := foraoffer.Build(foraoffer.Resource{
				Exchange:     "exchange.example",
				CanonicalURL: "https://publisher.example/news/a",
				Pricing:      &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE, Rate: "0", Currency: "EUR"},
				Terms:        tc.terms,
			})
			err := helpers.Validate(offer)
			if tc.wantRule == "" {
				if err != nil {
					t.Fatalf("validate: %v, want a one-term offer to be valid", err)
				}
				return
			}
			if ids := helpers.ValidationRuleIDs(err); !slices.Contains(ids, tc.wantRule) {
				t.Fatalf("rule ids = %v (err %v), want %s", ids, err, tc.wantRule)
			}
		})
	}
}
