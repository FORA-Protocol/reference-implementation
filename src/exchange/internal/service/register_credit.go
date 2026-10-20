package service

import (
	"context"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/reqctx"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/exchange"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/money"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/sor"
)

// The welcome-credit half of the Register flow. The rest of registration is in
// register.go; this is separate because the grant is the only part that has to
// reason about currencies.

// grantWelcomeCredits grants the configured welcome credit
// (ExchangeConfig.DefaultAgentCredits) in every currency this deployment bills
// in, one Credit call per currency. A currency with no configured amount, and a
// configured amount of zero, are both skipped.
//
// It runs before SetBillingRef, so a failed grant fails the RPC while the row
// still has no billing_ref — the retry re-enters firstRegister instead of the
// fast path. Each currency has its OWN welcome slot
// (billing.WelcomeCreditKey carries the currency), so a retry after a partial
// grant is safe in both directions: the currencies that already landed are
// no-ops on their slots, and the ones that did not are granted exactly once.
// Without the currency in the key, all currencies would share one slot and
// every currency after the first would silently get nothing.
//
// Only an account the SoR reports active is granted: a tenant holding new
// agents for review (activate_new_agents_by_default = false) must not pay for
// every self-signup, and an agent activated manually later is funded through
// the operator script's shared service slot instead.
//
// The currencies are walked in the deployment's configured order rather than by
// iterating the credit map, for two reasons: the order is deterministic, so a
// retry repeats the same sequence, and a configured currency the deployment
// does not bill in is skipped instead of being sent to a billing gate that
// would reject it. Boot already refuses such a configuration; the walk keeps
// the service safe when it is constructed some other way.
func (s *ExchangeService) grantWelcomeCredits(ctx context.Context, acct sor.Account) error {
	if !acct.Active {
		return nil
	}
	for _, currency := range s.cfg.LedgerCurrencies {
		amount := s.cfg.DefaultAgentCredits[currency]
		if amount == nil || amount.Sign() <= 0 {
			continue
		}
		grant := billing.Amount{Value: amount, Currency: currency}
		key := billing.WelcomeCreditKey(acct.BillingRef, currency)
		if err := s.billing.Credit(ctx, acct.BillingRef, grant, key); err != nil {
			return exchange.Wrap(billingErrorKind(err), err, "grant default credit in "+currency)
		}
		// The welcome credit has no aggregate cap, so this request-correlated
		// line is the operator's visibility into a drain on the liquidity
		// account.
		reqctx.FromContext(ctx).InfoContext(ctx, "granted default agent credit",
			"billing_ref", acct.BillingRef,
			"currency", currency,
			"amount", money.DecimalString(amount))
	}
	return nil
}
