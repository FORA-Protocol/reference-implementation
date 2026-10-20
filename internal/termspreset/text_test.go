package termspreset_test

import (
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
)

func TestPriceText(t *testing.T) {
	t.Parallel()
	unit := "fetches"
	cases := []struct {
		name    string
		pricing *forav1.Pricing
		want    string
	}{
		{"free", &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE, Currency: "EUR"}, "free"},
		{"per unit", &forav1.Pricing{
			Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT, Rate: "0.05", Currency: "EUR", Unit: &unit,
		}, "0.05 EUR per fetches"},
		{"flat", &forav1.Pricing{
			Model: forav1.PricingModel_PRICING_MODEL_FLAT, Rate: "12", Currency: "EUR",
		}, "12 EUR once"},
		{"unnamed model", &forav1.Pricing{
			Model: forav1.PricingModel_PRICING_MODEL_UNSPECIFIED, Rate: "3", Currency: "EUR",
		}, "3 EUR unspecified"},
		{"no pricing", nil, "unspecified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := termspreset.PriceText(tc.pricing); got != tc.want {
				t.Errorf("PriceText = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribeTerm(t *testing.T) {
	t.Parallel()
	unit := "fetches"
	term := &forav1.LicenseTerm{
		Pricing: &forav1.Pricing{
			Model: forav1.PricingModel_PRICING_MODEL_PER_UNIT, Rate: "0.02", Currency: "EUR", Unit: &unit,
		},
		Restrictions: []*forav1.Restriction{
			{
				Kind:      forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION,
				Permitted: []string{"search", "ai-input"}, Prohibited: []string{"ai-train"},
			},
			{Kind: forav1.RestrictionKind_RESTRICTION_KIND_GEOGRAPHY},
		},
		Obligations: []*forav1.Obligation{{Kind: forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION}},
	}
	want := "0.02 EUR per fetches · allowed only: search, ai input · not allowed: ai training · " +
		"attribution required · and 1 more condition"
	if got := termspreset.DescribeTerm(term); got != want {
		t.Fatalf("DescribeTerm =\n %q\nwant\n %q", got, want)
	}
}

func TestDescribeTerm_ShowsAnUnknownUseAsItsToken(t *testing.T) {
	t.Parallel()
	term := &forav1.LicenseTerm{
		Pricing: &forav1.Pricing{Model: forav1.PricingModel_PRICING_MODEL_FREE},
		Restrictions: []*forav1.Restriction{{
			Kind: forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION, Permitted: []string{"archive"},
		}},
	}
	if got, want := termspreset.DescribeTerm(term), "free · allowed only: archive"; got != want {
		t.Fatalf("DescribeTerm = %q, want %q", got, want)
	}
}
