package billing

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"
)

// Balance reads for InMemoryAdapter: the settled balance, the available
// balance, and the money gate Authorize runs against the available balance.

// checkCurrencyAndBalance enforces the money gate for a non-zero charge: the
// agent's balance currency must match the term's currency, and the available
// balance (actual minus same-currency pending reservations) must cover the
// charge. Returns (denial, false) on refusal, (zero, true) on pass. Caller holds
// a.mu. Only reached for non-zero charges (the zero-cost path skips it).
func (a *InMemoryAdapter) checkCurrencyAndBalance(
	req AuthorizeRequest, bal Amount, charge *big.Rat,
) (AuthorizeResult, bool) {
	if bal.Currency != req.UnitCost.Currency {
		return AuthorizeResult{Approved: false, Reason: "currency mismatch"}, false
	}
	available := new(big.Rat).Sub(bal.Value, a.reservedTotal(req.BillingRef, bal.Currency))
	if available.Cmp(charge) < 0 {
		return AuthorizeResult{Approved: false, Reason: "insufficient balance"}, false
	}
	return AuthorizeResult{}, true
}

// reservedTotal sums the live reservations of one account in one currency.
// Caller holds a.mu.
func (a *InMemoryAdapter) reservedTotal(billingRef, currency string) *big.Rat {
	total := new(big.Rat)
	for _, r := range a.reserved {
		if r.BillingRef == billingRef && r.EstAmt.Currency == currency {
			total.Add(total, r.EstAmt.Value)
		}
	}
	return total
}

// GetBalance returns the actual settled balance for an account. Pending
// reservations are not included (they represent authorised but not yet
// consumed funds).
//
// The currency argument is deliberately ignored: this tier is single-currency,
// an account holds exactly one balance, and the returned Amount carries that
// balance's currency. Only the persisted TigerBeetle adapter keeps a balance per
// currency, and only there does the argument select one.
func (a *InMemoryAdapter) GetBalance(_ context.Context, billingRef, _ string) (Amount, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	bal, ok := a.balances[billingRef]
	if !ok {
		return Amount{}, fmt.Errorf("billing: unknown account %q", billingRef)
	}
	return Amount{Value: new(big.Rat).Set(bal.Value), Currency: bal.Currency}, nil
}

// AvailableBalance returns the settled balance minus the account's live
// reservations, the same figure Authorize checks a charge against.
//
// Unlike GetBalance, it honours the currency argument. A currency outside this
// tier's one currency is a configuration error. An account that does not exist,
// or holds its one balance in another currency (only possible through a direct
// seed), has no ledger account in the asked currency: ErrNoLedgerAccount.
func (a *InMemoryAdapter) AvailableBalance(_ context.Context, billingRef, currency string) (Amount, error) {
	if !slices.Contains(demoCurrencies, currency) {
		return Amount{}, fmt.Errorf("billing: currency %q is not configured (configured: %s)",
			currency, strings.Join(demoCurrencies, ", "))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	bal, ok := a.balances[billingRef]
	if !ok || bal.Currency != currency {
		return Amount{}, fmt.Errorf("%w: %q in %s", ErrNoLedgerAccount, billingRef, currency)
	}
	available := new(big.Rat).Sub(bal.Value, a.reservedTotal(billingRef, currency))
	return Amount{Value: nonNegative(available), Currency: currency}, nil
}
