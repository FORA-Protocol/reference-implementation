package termspreset_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"google.golang.org/protobuf/proto"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

func TestBuildOffersFromOneTerm(t *testing.T) {
	t.Parallel()
	built, err := termspreset.FromControls(termspreset.Controls{
		PricingModel:       "per_unit",
		Rate:               "0.05",
		PermittedFunctions: []string{"ai-input"},
	}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	offers, err := termspreset.BuildOffers(termspreset.OfferInput{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/news/a",
		Title:        "Midnight Harbour",
		Terms:        built,
	})
	if err != nil {
		t.Fatalf("build offers: %v", err)
	}
	if len(offers) != 1 {
		t.Fatalf("offers = %d, want one per term", len(offers))
	}
	offer := offers[0]
	if offer.GetExchange() != "exchange.example" {
		t.Fatalf("exchange = %q", offer.GetExchange())
	}
	if offer.GetTitle() != "Midnight Harbour" {
		t.Fatalf("title = %q", offer.GetTitle())
	}
	if offer.GetIdentity().GetCanonicalUrl() != "https://publisher.example/news/a" {
		t.Fatalf("canonical url = %q", offer.GetIdentity().GetCanonicalUrl())
	}
	if offer.GetDeliveryMethod() != forav1.DeliveryMethod_DELIVERY_METHOD_INSTRUCTIONS {
		t.Fatalf("delivery method = %v", offer.GetDeliveryMethod())
	}
	// The price is the term this offer sells, as on the real discovery path.
	if offer.GetPricing().GetRate() != "0.05" {
		t.Fatalf("pricing = %+v, want the sold term's", offer.GetPricing())
	}
	if len(offer.GetTerms()) != 1 {
		t.Fatalf("terms = %d, want exactly the one term this offer sells", len(offer.GetTerms()))
	}
}

// The three fields only an Exchange can produce stay absent. Their absence is
// what keeps this a preview rather than something that could be mistaken for a
// real offer and replayed.
func TestBuildOffersCarryNothingOnlyAnExchangeCanIssue(t *testing.T) {
	t.Parallel()
	offers, err := termspreset.BuildOffers(termspreset.OfferInput{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/news/a",
		Terms:        termspreset.DefaultFree(testDefaults),
	})
	if err != nil {
		t.Fatalf("build offers: %v", err)
	}
	offer := offers[0]
	if offer.GetOfferId() != "" {
		t.Fatalf("offer id = %q, want none: only an Exchange assigns one", offer.GetOfferId())
	}
	if offer.GetSignature() != "" {
		t.Fatalf("signature = %q, want none: a preview is nobody's statement", offer.GetSignature())
	}
	if offer.GetExpiresAt() != nil {
		t.Fatal("expiry was set, but a preview commits to no window")
	}
}

// A page licensed two ways previews as two offers, in the publisher's order,
// each selling one term and priced from it. Nothing fuses them, because an
// offer an agent could act on sells exactly one term.
func TestBuildOffersOnePerTerm(t *testing.T) {
	t.Parallel()
	free := termspreset.DefaultFree(testDefaults)
	paid, err := termspreset.FromControls(termspreset.Controls{PricingModel: "per_unit", Rate: "2"}, testDefaults)
	if err != nil {
		t.Fatalf("from controls: %v", err)
	}
	input := make([]*forav1.LicenseTerm, 0, len(paid)+len(free))
	input = append(input, paid...)
	input = append(input, free...)
	offers, err := termspreset.BuildOffers(termspreset.OfferInput{
		Exchange:     "exchange.example",
		CanonicalURL: "https://publisher.example/a",
		Terms:        input,
	})
	if err != nil {
		t.Fatalf("build offers: %v", err)
	}
	if len(offers) != len(input) {
		t.Fatalf("offers = %d, want %d (one per term)", len(offers), len(input))
	}
	if offers[0].GetPricing().GetRate() != "2" {
		t.Fatalf("first offer pricing = %+v, want the paid term's", offers[0].GetPricing())
	}
	if got := offers[1].GetPricing().GetModel(); got != forav1.PricingModel_PRICING_MODEL_FREE {
		t.Fatalf("second offer pricing model = %v, want FREE (the free term's)", got)
	}
	for i, offer := range offers {
		if len(offer.GetTerms()) != 1 {
			t.Fatalf("offer %d carries %d terms, want exactly 1", i, len(offer.GetTerms()))
		}
		if !proto.Equal(offer.GetTerms()[0], input[i]) {
			t.Fatalf("offer %d sells %v, want the term at the same position", i, offer.GetTerms()[0])
		}
	}
}

func TestBuildOffersRefusesNoTerms(t *testing.T) {
	t.Parallel()
	if _, err := termspreset.BuildOffers(termspreset.OfferInput{Exchange: "exchange.example"}); err == nil {
		t.Fatal("an offer was built with no terms to price it")
	}
}
