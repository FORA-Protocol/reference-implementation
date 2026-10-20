package billing

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	tb "github.com/tigerbeetle/tigerbeetle-go"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
)

// Balance reads for TigerBeetleAdapter: the settled balance, the available
// balance, and the pre-check Authorize runs against the available balance.

// GetBalance returns the settled balance of one currency's account
// (credits_posted − debits_posted); pending holds are excluded, matching
// InMemoryAdapter. Balances are never summed across currencies — the caller asks
// for one currency and gets that currency's money. A currency this deployment
// does not bill in, and an account that does not exist on that currency's
// ledger, are both errors.
func (a *TigerBeetleAdapter) GetBalance(
	ctx context.Context, billingRef, currency string,
) (amt Amount, err error) {
	defer func() { err = classifyUnavailable(err) }()
	acc, err := a.lookupAgentAccount(ctx, billingRef, currency)
	if err != nil {
		return Amount{}, err
	}
	bal := new(big.Int).Sub(acc.CreditsPosted.BigInt(), acc.DebitsPosted.BigInt())
	return a.fromMinor(bal, currency), nil
}

// AvailableBalance returns what the account can spend now in one currency
// (credits_posted − debits_posted − debits_pending), the same figure Authorize
// checks a charge against. A hold that is neither recorded nor released counts
// until TigerBeetle expires it (see timeoutSeconds).
func (a *TigerBeetleAdapter) AvailableBalance(
	ctx context.Context, billingRef, currency string,
) (amt Amount, err error) {
	defer func() { err = classifyUnavailable(err) }()
	acc, err := a.lookupAgentAccount(ctx, billingRef, currency)
	if err != nil {
		return Amount{}, err
	}
	available := a.fromMinor(availableMinor(acc), currency)
	available.Value = nonNegative(available.Value)
	return available, nil
}

// checkBalance pre-checks available funds (credits_posted − debits_posted −
// debits_pending) so an insufficient balance is a clean soft denial rather than a
// failed transfer whose (poisoned) id could never be retried.
func (a *TigerBeetleAdapter) checkBalance(
	ctx context.Context, agentAcct tigerbeetle.ID, minor *big.Int,
) (AuthorizeResult, bool, error) {
	acc, found, err := a.tb.LookupAccount(ctx, agentAcct)
	if err != nil {
		return AuthorizeResult{}, false, fmt.Errorf("billing: lookup agent: %w", err)
	}
	available := new(big.Int)
	if found {
		available = availableMinor(acc)
	}
	if available.Cmp(minor) < 0 {
		return AuthorizeResult{Approved: false, Reason: "insufficient balance"}, false, nil
	}
	return AuthorizeResult{}, true, nil
}

// lookupAgentAccount reads the agent's account in one currency. A currency the
// deployment does not bill in is a plain configuration error; a billed currency
// in which this billing_ref has no account is ErrNoLedgerAccount.
func (a *TigerBeetleAdapter) lookupAgentAccount(
	ctx context.Context, billingRef, currency string,
) (tb.Account, error) {
	if _, ok := a.ledgerFor(currency); !ok {
		return tb.Account{}, fmt.Errorf("billing: currency %q is not configured (configured: %s)",
			currency, strings.Join(a.currencies, ", "))
	}
	id, err := a.accountID(tigerbeetle.PrefixAgent, currency, billingRef)
	if err != nil {
		return tb.Account{}, err
	}
	acc, found, err := a.tb.LookupAccount(ctx, id)
	if err != nil {
		return tb.Account{}, fmt.Errorf("billing: lookup agent account: %w", err)
	}
	if !found {
		return tb.Account{}, fmt.Errorf("%w: %q in %s", ErrNoLedgerAccount, billingRef, currency)
	}
	return acc, nil
}

// availableMinor is the spendable amount of an agent account in minor units:
// credits_posted − debits_posted − debits_pending. Authorize and
// AvailableBalance share it so the two cannot disagree on what is spendable.
func availableMinor(acc tb.Account) *big.Int {
	available := new(big.Int).Sub(acc.CreditsPosted.BigInt(), acc.DebitsPosted.BigInt())
	return available.Sub(available, acc.DebitsPending.BigInt())
}
