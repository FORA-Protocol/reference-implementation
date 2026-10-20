//go:build integration

package exchangetest

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
)

// The license terms a suite lists resources under. Each is unrestricted, so it
// projects an offer for every requester, and priced in the one currency a
// selling seat keeps a ledger in.

// PerUnitTerm charges rate per access. estimatedQuantity is the quantity the
// usage report is checked against, within the seat's tolerance; zero or less
// leaves it unset.
func PerUnitTerm(rate string, estimatedQuantity int32) *forav1.LicenseTerm {
	unit := "accesses"
	pricing := &forav1.Pricing{
		Model:    forav1.PricingModel_PRICING_MODEL_PER_UNIT,
		Rate:     rate,
		Currency: ledgerCurrency,
		Unit:     &unit,
	}
	if estimatedQuantity > 0 {
		pricing.EstimatedQuantity = &estimatedQuantity
	}
	return &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing:   pricing,
	}
}

// UnmeteredTerm is PerUnitTerm for a one-time sale that meters nothing: its
// charge closes at the transaction, so no usage report is owed and no
// reporting obligation is minted.
func UnmeteredTerm(rate string) *forav1.LicenseTerm {
	term := PerUnitTerm(rate, 1)
	metering := forav1.PricingMetering_PRICING_METERING_NONE
	term.Pricing.Metering = &metering
	return term
}

// FreeTerm charges nothing. It needs no rate, which reads as zero, and no unit.
func FreeTerm() *forav1.LicenseTerm {
	return &forav1.LicenseTerm{
		Semantics: forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED,
		Pricing: &forav1.Pricing{
			Model:    forav1.PricingModel_PRICING_MODEL_FREE,
			Currency: ledgerCurrency,
		},
	}
}
