package billing

import (
	"errors"
	"fmt"
	"math/big"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/money"
)

// The TigerBeetle adapter's money side: how a deployment declares which
// currencies it bills in, the two lookups every money-moving path uses to go
// between a currency and its ledger, and the conversion between a decimal
// Amount and the integer minor units a ledger stores. The transfer lifecycle is
// in tigerbeetle_adapter.go, the account lifecycle in tigerbeetle_accounts.go.

// TigerBeetleOptions configures a TigerBeetleAdapter. HoldTimeout falls back
// to the package default when zero. The asset scale is not an option: it is
// deliberately fixed at money.AssetScale for every deployment.
type TigerBeetleOptions struct {
	Client *tigerbeetle.Client
	// Currencies are the ISO 4217 alpha codes this deployment bills in, in
	// configuration order. Boot derives them from EXCHANGE_BILLING_LEDGER. The
	// list must be non-empty, free of duplicates, and every code must be one the
	// shared currency table knows; the constructor rejects anything else rather
	// than starting with a ledger it cannot name.
	Currencies  []string
	HoldTimeout time.Duration
	IDNamespace string
}

// NewTigerBeetleAdapter builds an adapter from opts. It returns an error for an
// empty, duplicated or unsupported currency list: those are boot-time
// misconfigurations, and an adapter that started anyway would derive account ids
// for a currency it cannot map to a ledger.
func NewTigerBeetleAdapter(opts TigerBeetleOptions) (*TigerBeetleAdapter, error) {
	if len(opts.Currencies) == 0 {
		return nil, fmt.Errorf("billing: tigerbeetle adapter needs at least one currency")
	}
	ledgers := make(map[string]uint32, len(opts.Currencies))
	for _, currency := range opts.Currencies {
		ledger, ok := tigerbeetle.LedgerForCurrency(currency)
		if !ok {
			return nil, fmt.Errorf("billing: unsupported billing currency %q (supported: %s)",
				currency, tigerbeetle.SupportedLedgers())
		}
		if _, dup := ledgers[currency]; dup {
			return nil, fmt.Errorf("billing: billing currency %q listed twice", currency)
		}
		ledgers[currency] = ledger
	}
	a := &TigerBeetleAdapter{
		tb:          opts.Client,
		currencies:  append([]string(nil), opts.Currencies...),
		ledgers:     ledgers,
		holdTimeout: opts.HoldTimeout,
		idNS:        opts.IDNamespace,
	}
	if a.holdTimeout <= 0 {
		a.holdTimeout = defaultHoldTimeout
	}
	return a, nil
}

// ledgerFor resolves a currency to its ledger number, reporting whether this
// deployment bills in that currency at all. Every money-moving path opens with
// it, so an unconfigured currency is refused before any id is derived.
func (a *TigerBeetleAdapter) ledgerFor(currency string) (uint32, bool) {
	ledger, ok := a.ledgers[currency]
	return ledger, ok
}

// currencyFor is the reverse direction, used where a ledger number was read back
// from a transfer (a settlement's fee leg, a refund's reversal legs) and the
// account ids for that same ledger have to be re-derived. It answers from the
// configured set rather than the global table, so a ledger this deployment no
// longer bills in cannot be settled onto by accident.
func (a *TigerBeetleAdapter) currencyFor(ledger uint32) (string, bool) {
	for _, currency := range a.currencies {
		if a.ledgers[currency] == ledger {
			return currency, true
		}
	}
	return "", false
}

// toMinor converts a currency amount to an integer number of minor units at the
// ledger's asset scale. At scale 8 every amount the Exchange produces (8-decimal
// prices) is exact; a value with finer precision is rejected rather than silently
// rounded. The conversion itself is money.MinorUnits so the adapter, the repo
// layer, and the test fixtures share one money-math source of truth.
func (a *TigerBeetleAdapter) toMinor(amt Amount) (*big.Int, error) {
	minor, err := money.MinorUnits(amt.Value)
	if errors.Is(err, money.ErrAmountNotRepresentable) {
		// Translate the conversion-layer sentinel into the billing sentinel so a
		// finer-than-scale price surfaces as KindInvalidRequest (4xx), not a 500.
		return nil, fmt.Errorf("%w: %w", ErrAmountNotRepresentable, err)
	}
	return minor, err
}

func (a *TigerBeetleAdapter) fromMinor(minor *big.Int, currency string) Amount {
	return Amount{
		Value:    new(big.Rat).SetFrac(minor, money.ScaleFactor()),
		Currency: currency,
	}
}
