package foraoffer_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/foraoffer"
	"google.golang.org/protobuf/proto"
)

func TestPricingFromTerm(t *testing.T) {
	zero := "0"
	for _, tc := range []struct {
		name         string
		input        *forav1.Pricing
		rate, charge string
	}{
		{"free", &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE}, "0", "0"},
		{"rate fallback", &forav1.Pricing{Rate: "0.05"}, "0.05", "0.05"},
		{"explicit free charge", &forav1.Pricing{Rate: "0.05", UnitCost: &zero}, "0.05", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := proto.Clone(tc.input)
			got, err := foraoffer.PricingFromTerm(&forav1.LicenseTerm{Pricing: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			if got.Rate != tc.rate || got.UnitCost == nil || got.GetUnitCost() != tc.charge || got.Unit == nil || got.GetUnit() != "" || got.Metering != nil {
				t.Fatalf("unexpected offer pricing: %v", got)
			}
			if !proto.Equal(before, tc.input) {
				t.Fatal("input pricing changed")
			}
		})
	}
	for _, p := range []*forav1.Pricing{nil, {Rate: "invalid"}, {Rate: "0.05", UnitCost: proto.String("invalid")}} {
		if _, err := foraoffer.PricingFromTerm(&forav1.LicenseTerm{Pricing: p}); err == nil {
			t.Fatalf("accepted invalid pricing: %v", p)
		}
	}
}

func TestPricingFromTermPreservesMeteringAndQuantity(t *testing.T) {
	input := &forav1.Pricing{
		Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT, Rate: "0.05", Currency: "EUR",
		Unit: proto.String("article"), EstimatedQuantity: proto.Int32(3),
		Metering: forav1.PricingMetering_PRICING_METERING_NONE.Enum(),
	}
	got, err := foraoffer.PricingFromTerm(&forav1.LicenseTerm{Pricing: input})
	if err != nil {
		t.Fatal(err)
	}
	want := proto.Clone(input).(*forav1.Pricing)
	want.UnitCost = proto.String("0.05")
	if !proto.Equal(got, want) {
		t.Fatalf("pricing = %v, want %v", got, want)
	}
	*got.Unit = "changed"
	*got.EstimatedQuantity = 4
	*got.Metering = forav1.PricingMetering_PRICING_METERING_ONLINE
	if input.GetUnit() != "article" || input.GetEstimatedQuantity() != 3 || input.GetMetering() != forav1.PricingMetering_PRICING_METERING_NONE {
		t.Fatal("output aliases the term's pricing")
	}
}
