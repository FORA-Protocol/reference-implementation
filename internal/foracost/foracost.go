// Package foracost aggregates per-item FORA transaction costs: per currency
// (PerCurrency), and into the batch-level total_cost scalar (BatchTotal).
//
// A multi-source (multi-exchange) or mixed-currency batch must never collapse
// charges into a single mis-labeled scalar: summing amounts across differing
// ISO-4217 currencies is meaningless, and latching one source's subtotal as the
// whole-batch aggregate under-counts the rest. The authoritative cost is always
// the per-item items[].cost; the batch scalar is a convenience that is emitted
// ONLY when the whole batch shares one currency, and is absent (nil) otherwise.
//
// The Exchange renders its own items[] batch's total_cost through BatchTotal,
// and the Broker renders BrokerTransactionResponse.totals across Exchanges
// through PerCurrency, which BatchTotal is built on, so the two surfaces sum the
// same way. Money is exact decimal end to end (helpers.ParseMoney/FormatMoney),
// never float.
package foracost

import (
	"fmt"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"
	"github.com/shopspring/decimal"
)

// BatchTotal renders the batch-level total_cost from the authoritative per-item
// costs of a result set. It returns a single *forav1.Cost when every costed item
// shares one currency (Amount = the exact decimal sum), or nil when the items
// span multiple currencies or none carry a cost — in which case callers rely on
// the per-item items[].cost. A non-decimal cost Amount yields an error (the
// caller surfaces it as internal / omits the scalar) rather than a silent
// miscount.
func BatchTotal(items []*forav1.TransactionResultItem) (*forav1.Cost, error) {
	totals, err := PerCurrency(items)
	if err != nil {
		return nil, err
	}
	// Zero costed items (every item denied / free) or a mixed-currency batch: no
	// single scalar is meaningful, so the aggregate is absent and items[].cost is
	// the sole authoritative cost.
	if len(totals) != 1 {
		return nil, nil
	}
	return totals[0], nil
}

// PerCurrency sums the costed items per ISO-4217 currency and returns one
// *forav1.Cost per currency, in the order each currency first appears among the
// items. Amounts are added as exact decimals and never summed across currencies
// or converted; unit_cost stays unset. Items with a nil Cost or an empty
// Currency are skipped, and no costed item at all yields a nil slice. A
// non-decimal cost Amount yields an error rather than a silent miscount.
//
// It sums every item it is given. Which items count is the caller's decision:
// the Broker passes only the items that carry neither a denial nor an upstream
// refusal.
func PerCurrency(items []*forav1.TransactionResultItem) ([]*forav1.Cost, error) {
	sums := map[string]decimal.Decimal{}
	order := make([]string, 0)
	for _, it := range items {
		cost := it.GetCost()
		if cost == nil || cost.GetCurrency() == "" {
			continue
		}
		amount, err := helpers.ParseMoney(cost.GetAmount())
		if err != nil {
			return nil, fmt.Errorf("parse item cost amount %q: %w", cost.GetAmount(), err)
		}
		cur := cost.GetCurrency()
		if _, seen := sums[cur]; !seen {
			order = append(order, cur)
		}
		sums[cur] = sums[cur].Add(amount)
	}
	var totals []*forav1.Cost
	for _, cur := range order {
		amountStr, err := helpers.FormatMoney(sums[cur])
		if err != nil {
			return nil, fmt.Errorf("format %s total: %w", cur, err)
		}
		totals = append(totals, &forav1.Cost{Amount: amountStr, Currency: cur})
	}
	return totals, nil
}
