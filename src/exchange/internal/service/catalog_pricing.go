package service

import (
	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/shopspring/decimal"
)

// PricingDoc is the internal decimal pricing shape used for billing verified
// offers. Discovery and preview wire pricing is projected by foraoffer.
type PricingDoc struct {
	Model    string
	Rate     decimal.Decimal
	Currency string
	UnitCost decimal.Decimal
	Unit     string
	EstQty   int32
	// Metering is how usage is tracked for billing reconciliation, carried as a
	// pointer so an absent value stays distinguishable from an explicit ONLINE.
	// The protocol reads absent as ONLINE, which is also what MetersUsage below
	// does; the distinction is kept so the offer we mint says exactly what the
	// term said, and no more.
	Metering *forav1.PricingMetering
}

// MetersUsage reports whether a transaction under this price owes a usage
// report. False only for a one-time perpetual sale (PRICING_METERING_NONE):
// billing closes at ExecuteTransaction, so there is nothing to report later and
// no obligation is minted. Absent metering reads as ONLINE per the protocol, so
// a term that says nothing still meters.
func (p PricingDoc) MetersUsage() bool {
	return p.Metering == nil || *p.Metering != forav1.PricingMetering_PRICING_METERING_NONE
}

// IsFree reports whether this resolved price is zero — the single predicate behind
// the free-resource path: a zero unit_cost bypasses the billing adapter (no
// Authorize, no Record, no Release; ADR-009 D2) and stores an empty billing_id
// (ADR-009 D5). Keyed on unit_cost — the normalized, signature-covered charge basis
// — not rate, so a publisher may advertise a headline rate while setting unit_cost 0
// for free execution.
func (p PricingDoc) IsFree() bool { return p.UnitCost.IsZero() }

// moneyOrZero parses a canonical wire money string into an exact decimal,
// mapping the empty (unset / FREE) string to decimal.Zero. Any non-empty but
// non-canonical value surfaces as an error — empty is the ONLY value treated as
// zero, so a malformed price fails loud rather than silently pricing at 0.
func moneyOrZero(s string) (decimal.Decimal, error) {
	if s == "" {
		return decimal.Zero, nil
	}
	return helpers.ParseMoney(s)
}

// pricingDocFromPricing reads the VERIFIED offer's signed price for billing.
// Missing unit_cost falls back to rate, matching the wire pricing projection.
func pricingDocFromPricing(p *forav1.Pricing) (PricingDoc, error) {
	rate, err := moneyOrZero(p.GetRate())
	if err != nil {
		return PricingDoc{}, err
	}
	doc := PricingDoc{
		Model:    p.GetModel().String(),
		Rate:     rate,
		Currency: p.GetCurrency(),
		Unit:     p.GetUnit(),
		EstQty:   p.GetEstimatedQuantity(),
	}
	if p.Metering != nil {
		// Enum() returns a pointer to a copy, so the doc cannot observe a later
		// mutation of the request message it came from.
		doc.Metering = p.GetMetering().Enum()
	}
	if p.UnitCost != nil {
		uc, ucErr := moneyOrZero(p.GetUnitCost())
		if ucErr != nil {
			return PricingDoc{}, ucErr
		}
		doc.UnitCost = uc
	} else {
		doc.UnitCost = rate
	}
	return doc, nil
}
