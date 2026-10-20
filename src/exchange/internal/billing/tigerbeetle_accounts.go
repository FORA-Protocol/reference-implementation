package billing

import (
	"context"
	"fmt"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
)

// Account lifecycle for TigerBeetleAdapter: business-id → ledger-account-id
// derivation and lazy account creation. The transfer lifecycle (Authorize /
// Record / Release / Refund) lives in tigerbeetle_adapter.go and
// tigerbeetle_settle.go.

// EnsureAgentAccount creates the agent's ledger account on EVERY configured
// currency's ledger for a freshly registered billing_ref. One agent holds one
// balance per currency, so the Register flow creates them all: a later balance
// read in any configured currency then finds a zero-balance account instead of
// an error, whether or not that currency was ever funded.
//
// Each account id is derived exactly the way the Authorize path derives agent
// accounts — AccountID(PrefixAgent, currency, ns+ref) — so pay/balance calls
// keyed by the same ref and currency land on the account created here (ADR-021
// D5: the ledger hashes the billing_ref text into its account number and stores
// no text). Idempotency is ledger-native: an identical pre-existing account is
// success, and a failed create leaves nothing behind (each CreateAccounts call
// either lands its account or does not).
func (a *TigerBeetleAdapter) EnsureAgentAccount(ctx context.Context, billingRef string) (err error) {
	defer func() { err = classifyUnavailable(err) }()
	if billingRef == "" {
		return errEmptyBillingRef
	}
	for _, currency := range a.currencies {
		id, idErr := a.accountID(tigerbeetle.PrefixAgent, currency, billingRef)
		if idErr != nil {
			return idErr
		}
		if err := a.ensureAccount(ctx, id, a.ledgers[currency], tigerbeetle.CodeAgent, "agent"); err != nil {
			return err
		}
	}
	return nil
}

// ownerAccount is the resource-owner revenue account the settlement credits. An empty
// resource_owner_id is refused with ErrUnknownPayee rather than settled into a shared
// owner:revenue: bucket: ADR-010 amendment A requires every catalog entry to attest its
// owner and forbids a tenant-id fallback. Attestation is enforced upstream at catalog
// push (migration 000018); a row predating that migration must be backfilled before
// settlement, and until then this guard turns a silent mis-settlement into a loud
// refusal.
func (a *TigerBeetleAdapter) ownerAccount(currency, resourceOwnerID string) (tigerbeetle.ID, error) {
	if resourceOwnerID == "" {
		return tigerbeetle.ID{}, ErrUnknownPayee
	}
	return a.accountID(tigerbeetle.PrefixOwner, currency, tigerbeetle.OwnerRevenuePrefix+resourceOwnerID)
}

func (a *TigerBeetleAdapter) ensureAccounts(
	ctx context.Context, ledger uint32, agentAcct, ownerID tigerbeetle.ID,
) error {
	if err := a.ensureAccount(ctx, agentAcct, ledger, tigerbeetle.CodeAgent, "agent"); err != nil {
		return err
	}
	return a.ensureAccount(ctx, ownerID, ledger, tigerbeetle.CodeOwner, "owner")
}

// ensureAccount lazily creates id with the flag policy for its code (agent accounts
// are debit-capped; owner/platform accounts keep history), wrapping any failure with
// the account label. Shared by the agent/owner reservation path and the platform-fee
// settlement path.
func (a *TigerBeetleAdapter) ensureAccount(
	ctx context.Context, id tigerbeetle.ID, ledger uint32, code tigerbeetle.AccountCode, label string,
) error {
	if err := a.tb.EnsureAccount(ctx, id, ledger, code, tigerbeetle.DefaultFlagsForCode(code)); err != nil {
		return fmt.Errorf("billing: ensure %s account: %w", label, err)
	}
	return nil
}

// platformFeeAccount derives the platform commission account id the fee leg
// credits for one currency: AccountID(PrefixPlatform, currency, platformFeeID).
// There is one such account per currency — commission collected in EUR and
// commission collected in USD are separate money — and both the settlement split
// and the refund reversal derive it from the currency of the transfer they are
// resolving.
func (a *TigerBeetleAdapter) platformFeeAccount(currency string) (tigerbeetle.ID, error) {
	return a.accountID(tigerbeetle.PrefixPlatform, currency, tigerbeetle.PlatformFeeID)
}

func (a *TigerBeetleAdapter) accountID(
	p tigerbeetle.Prefix, currency, businessID string,
) (tigerbeetle.ID, error) {
	id, err := tigerbeetle.AccountID(p, currency, a.idNS+businessID)
	if err != nil {
		return id, fmt.Errorf("billing: derive account id: %w", err)
	}
	return id, nil
}
