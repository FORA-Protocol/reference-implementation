package service

import (
	"context"
	"errors"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/money"
)

// accountBalances builds GetAccountStatusResponse.balances: the available
// balance in every currency this deployment bills in, in configured order.
//
// An empty list means "not reported", never zero, so an error must never
// shrink the list silently. The rules are:
//   - an adapter that keeps no balance (the free tier) reports nothing;
//   - a currency in which this account has no ledger account (a currency added
//     after the agent registered) is left out with a warning, and the others
//     are still reported;
//   - any other error fails the whole RPC, so an outage is never reported as
//     an empty or partial list.
func (s *ExchangeService) accountBalances(ctx context.Context, billingRef string) ([]*forav1.Cost, error) {
	out := make([]*forav1.Cost, 0, len(s.cfg.LedgerCurrencies))
	for _, currency := range s.cfg.LedgerCurrencies {
		amt, err := s.billing.AvailableBalance(ctx, billingRef, currency)
		switch {
		case errors.Is(err, billing.ErrBalanceNotReported):
			return nil, nil
		case errors.Is(err, billing.ErrNoLedgerAccount):
			reqctx.FromContext(ctx).WarnContext(ctx, "account has no ledger account in a billed currency",
				"billing_ref", billingRef, "currency", currency)
			continue
		case err != nil:
			return nil, exchange.Wrap(billingErrorKind(err), err, "read available balance in "+currency)
		}
		wire, err := wireAmount(amt)
		if err != nil {
			return nil, exchange.Wrap(exchange.KindInternal, err, "render available balance in "+currency)
		}
		out = append(out, &forav1.Cost{Amount: wire, Currency: amt.Currency})
	}
	return out, nil
}

// wireAmount renders an amount as the canonical wire decimal, exactly. It goes
// through money.MinorUnits, which rejects a value finer than the asset scale,
// because rendering the rational directly would round it.
func wireAmount(amt billing.Amount) (string, error) {
	minor, err := money.MinorUnits(amt.Value)
	if err != nil {
		return "", err
	}
	return money.WireString(minor)
}
