package main

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tigerbeetle"
)

// billingURLTTL mirrors service.ExchangeConfig's default signed-URL TTL (5m).
// The hold must outlive the URL by a grace window so a settle/void can still
// land at the edge of URL validity. If the Exchange later makes URLTTL
// configurable, thread the same value here so the two stay coupled.
const billingURLTTL = 5 * time.Minute

// newTigerBeetleBillingAdapter builds the persisted TigerBeetle billing adapter
// from the environment. Config is fully validated before the client connection
// opens, so a misconfiguration fails fast without leaking a socket. The returned
// func() closes the client on shutdown.
//
// Environment variables (all under the EXCHANGE_BILLING_ prefix — one adapter,
// one convention):
//   - EXCHANGE_BILLING_LEDGER: one ISO 4217 numeric currency (e.g. 978 = EUR),
//     or several separated by commas (e.g. "978,840"). Each is a TigerBeetle
//     ledger the deployment bills on, and the FIRST one is the currency the
//     well-known document advertises as base_currency — see
//     billingLedgerCurrencies.
//   - EXCHANGE_BILLING_TB_ADDRESS: TigerBeetle IP:port (hostnames are rejected).
//   - EXCHANGE_BILLING_TB_CLUSTER_ID: cluster id (default 0).
//   - EXCHANGE_BILLING_HOLD_GRACE: hold-timeout grace over the URL TTL (default 1m).
//   - EXCHANGE_BILLING_TB_OP_TIMEOUT: per-call cluster deadline (default 5s); a
//     call exceeding it fails fast with a retryable unavailable error instead of
//     blocking on an unreachable cluster.
func newTigerBeetleBillingAdapter(
	ctx context.Context, logger *slog.Logger,
) (billing.Adapter, []string, func(), error) {
	currencies, err := billingLedgerCurrencies()
	if err != nil {
		return nil, nil, nil, err
	}
	addr := runhttp.EnvOr("EXCHANGE_BILLING_TB_ADDRESS", "")
	if addr == "" {
		return nil, nil, nil, fmt.Errorf(
			"billing: EXCHANGE_BILLING_TB_ADDRESS is required for FORA_BILLING_ADAPTER=tigerbeetle")
	}
	cluster, err := strconv.ParseUint(runhttp.EnvOr("EXCHANGE_BILLING_TB_CLUSTER_ID", "0"), 10, 64)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("billing: invalid EXCHANGE_BILLING_TB_CLUSTER_ID: %w", err)
	}
	grace, err := time.ParseDuration(runhttp.EnvOr("EXCHANGE_BILLING_HOLD_GRACE", "1m"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("billing: invalid EXCHANGE_BILLING_HOLD_GRACE: %w", err)
	}
	var clientOpts []tigerbeetle.Option
	if raw := runhttp.EnvOr("EXCHANGE_BILLING_TB_OP_TIMEOUT", ""); raw != "" {
		opTimeout, pErr := time.ParseDuration(raw)
		if pErr != nil {
			return nil, nil, nil, fmt.Errorf("billing: invalid EXCHANGE_BILLING_TB_OP_TIMEOUT: %w", pErr)
		}
		clientOpts = append(clientOpts, tigerbeetle.WithOpTimeout(opTimeout))
	}

	client, err := tigerbeetle.NewClient(cluster, []string{addr}, logger, clientOpts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("billing: connect TigerBeetle at %s: %w", addr, err)
	}
	if err := client.Health(ctx); err != nil {
		client.Close()
		return nil, nil, nil, fmt.Errorf("billing: TigerBeetle health check: %w", err)
	}

	adapter, err := billing.NewTigerBeetleAdapter(billing.TigerBeetleOptions{
		Client:      client,
		Currencies:  currencies,
		HoldTimeout: billingURLTTL + grace,
		// IDNamespace deliberately empty: production hashing stays unsalted.
	})
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}
	// Boot-path logs use free-text messages by convention (see keys.go /
	// keyresolver.go); dotted event names (exchange.*) are the request/lifecycle path.
	//
	// base_currency is logged separately from the list because it is a decision
	// the list's ORDER makes silently: an operator who reorders the variable
	// changes what this Exchange advertises, and this line is where that shows up.
	logger.Info("billing adapter: tigerbeetle",
		"currencies", strings.Join(currencies, ","), "base_currency", currencies[0],
		"address", addr, "hold_timeout", billingURLTTL+grace)
	return adapter, currencies, client.Close, nil
}

// billingLedgerCurrencies reads EXCHANGE_BILLING_LEDGER — one ISO 4217 numeric
// code, or several separated by commas — and returns their ISO 4217 alpha codes
// IN THE ORDER THEY WERE LISTED. The deployment bills in every currency it
// returns, one TigerBeetle ledger each.
//
// The order is load-bearing, not cosmetic: the FIRST entry is the currency this
// Exchange advertises as base_currency in its well-known document, because that
// field holds a single value. Listing "978,840" advertises EUR; listing
// "840,978" advertises USD. Reordering the list is therefore a deliberate change
// to what other parties are told our prices are denominated in, and never a side
// effect of adding a currency — add new currencies at the END to leave the
// advertised value alone. The boot log line prints the resolved list and the
// advertised currency so the running value is visible without reading config.
//
// An empty value, an unparseable entry, a repeated entry, and a numeric with no
// ledger are all hard errors: the process refuses to start rather than bill in a
// set the operator did not mean.
func billingLedgerCurrencies() ([]string, error) {
	raw := runhttp.EnvOr("EXCHANGE_BILLING_LEDGER", "")
	if raw == "" {
		return nil, fmt.Errorf("billing: EXCHANGE_BILLING_LEDGER is required for FORA_BILLING_ADAPTER=tigerbeetle")
	}
	fields := strings.Split(raw, ",")
	currencies := make([]string, 0, len(fields))
	for _, field := range fields {
		entry := strings.TrimSpace(field)
		numeric, err := strconv.ParseUint(entry, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("billing: invalid EXCHANGE_BILLING_LEDGER entry %q: %w", entry, err)
		}
		currency, ok := tigerbeetle.CurrencyForLedger(uint32(numeric))
		if !ok {
			return nil, fmt.Errorf("billing: unsupported EXCHANGE_BILLING_LEDGER entry %d (supported: %s)",
				numeric, tigerbeetle.SupportedLedgers())
		}
		if slices.Contains(currencies, currency) {
			return nil, fmt.Errorf("billing: EXCHANGE_BILLING_LEDGER lists %d (%s) more than once",
				numeric, currency)
		}
		currencies = append(currencies, currency)
	}
	return currencies, nil
}
