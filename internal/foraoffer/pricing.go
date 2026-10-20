package foraoffer

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
)

// PricingFromTerm projects the sold term's price onto discovery and preview
// offers. Missing unit_cost falls back to rate; empty money means zero.
// Optional metering stays absent unless the term explicitly declares it.
func PricingFromTerm(term *forav1.LicenseTerm) (*forav1.Pricing, error) {
	p := term.GetPricing()
	if p == nil {
		return nil, fmt.Errorf("term carries no pricing")
	}
	rate, err := normalizeMoney(p.GetRate())
	if err != nil {
		return nil, fmt.Errorf("rate: %w", err)
	}
	unitCost := rate
	if p.UnitCost != nil {
		unitCost, err = normalizeMoney(p.GetUnitCost())
		if err != nil {
			return nil, fmt.Errorf("unit_cost: %w", err)
		}
	}
	unit := p.GetUnit()
	pricing := &forav1.Pricing{
		Model: p.GetModel(), Rate: rate, Currency: p.GetCurrency(),
		UnitCost: &unitCost, Unit: &unit,
	}
	if qty := p.GetEstimatedQuantity(); qty > 0 {
		pricing.EstimatedQuantity = &qty
	}
	if p.Metering != nil {
		pricing.Metering = p.GetMetering().Enum()
	}
	return pricing, nil
}

func normalizeMoney(raw string) (string, error) {
	if raw == "" {
		return "0", nil
	}
	value, err := helpers.ParseMoney(raw)
	if err != nil {
		return "", err
	}
	return helpers.FormatMoney(value)
}
