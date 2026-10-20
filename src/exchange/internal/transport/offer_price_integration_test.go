//go:build integration

package transport_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"google.golang.org/protobuf/proto"
)

// The pinned protocol states an offer's price once, in Offer.pricing, and the
// term the offer sells carries none (offer.terms.pricing_unset). A test that
// pushed a catalog term therefore finds it on an offer in two parts: the term
// copy, equal to the catalog term without its pricing, and the offer's own
// price, which states the catalog term's. These helpers check both.

// sellsTerm reports whether offer sells the catalog term want: its one term is
// want without pricing, and its own price states want's price.
func sellsTerm(offer *forav1.Offer, want *forav1.LicenseTerm) bool {
	terms := offer.GetTerms()
	if len(terms) != 1 {
		return false
	}
	return proto.Equal(terms[0], unpriced(want)) && samePrice(offer.GetPricing(), want.GetPricing())
}

// findOfferSelling returns the one offer that sells want, failing the test when
// none does. It also fails when an offer's term copy carries pricing.
func findOfferSelling(t *testing.T, offers []*forav1.Offer, want *forav1.LicenseTerm) *forav1.Offer {
	t.Helper()
	for i, o := range offers {
		for _, term := range o.GetTerms() {
			if term.GetPricing() != nil {
				t.Fatalf("offer %d: the term copy carries pricing %v; the price belongs on Offer.pricing", i, term.GetPricing())
			}
		}
	}
	for _, o := range offers {
		if sellsTerm(o, want) {
			return o
		}
	}
	t.Fatalf("no discovered offer sells the expected term:\nwant term  = %v\nwant price = %v\ngot        = %v",
		unpriced(want), want.GetPricing(), offers)
	return nil
}

// unpriced is term without its pricing: the term copy an offer carries.
func unpriced(term *forav1.LicenseTerm) *forav1.LicenseTerm {
	out, _ := proto.Clone(term).(*forav1.LicenseTerm)
	out.Pricing = nil
	return out
}

// samePrice compares what an offer charges with a catalog term's price: model,
// rate, currency, unit, estimate and metering. unit_cost is not compared, because
// the Exchange states it on every offer and a catalog term may leave it to the
// rate.
func samePrice(got, want *forav1.Pricing) bool {
	return got.GetModel() == want.GetModel() &&
		sameMoney(got.GetRate(), want.GetRate()) &&
		got.GetCurrency() == want.GetCurrency() &&
		got.GetUnit() == want.GetUnit() &&
		got.GetEstimatedQuantity() == want.GetEstimatedQuantity() &&
		got.GetMetering() == want.GetMetering()
}

// sameMoney compares two decimal money strings by value; an empty one is zero.
func sameMoney(a, b string) bool {
	if a == "" {
		a = "0"
	}
	if b == "" {
		b = "0"
	}
	x, errA := helpers.ParseMoney(a)
	y, errB := helpers.ParseMoney(b)
	return errA == nil && errB == nil && x.Equal(y)
}
