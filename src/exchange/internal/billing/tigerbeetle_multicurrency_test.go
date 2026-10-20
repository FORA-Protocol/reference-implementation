//go:build integration

package billing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing/tbtest"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/money"
)

// These tests drive ONE adapter configured with TWO ledgers against the real
// cluster. The property under test in every case is that the two currencies are
// separate money: a charge in one never touches the other's balance, a balance
// read names one currency and gets only that currency, and a reversal goes back
// to the ledger the charge was taken from.
//
// Everything is read through the tier-1 adapter.GetBalance surface. The one
// exception is the platform fee leg, which has no adapter read surface at all
// (GetBalance is agent-only); those go through the shared Testing-Doctrine §9
// ledger read on tbtest.Ledger, the same exception the split suite documents.

const (
	// mcEUR and mcUSD are the two currencies these tests configure. They are
	// written out rather than taken from confCurrency because the whole point is
	// that there are two of them and that the pair is asymmetric.
	mcEUR = "EUR"
	mcUSD = "USD"
	// mcOwner is the attested payee, and mcFeeBps a non-zero commission so each
	// settlement produces a real platform fee leg on its own ledger.
	mcOwner  = "mc-owner"
	mcFeeBps = 1000 // 10%
)

// mcAdapter builds one adapter configured for both currencies and funds the agent
// in each currency it is given an amount for. It returns the adapter and its id
// namespace (the salt every derived account and transfer id carries).
func mcAdapter(t *testing.T, eurSeed, usdSeed string) (billing.Adapter, string) {
	t.Helper()
	a, ns := newTBAdapterFor("mc", time.Minute, mcEUR, mcUSD)
	ctx := context.Background()
	for _, seed := range []struct {
		currency, amount string
		ledger           uint32
	}{
		{mcEUR, eurSeed, mcEURLedger},
		{mcUSD, usdSeed, tbConfLedger},
	} {
		if seed.amount == "" {
			continue
		}
		led := tbtest.Ledger{Client: tbClient, ID: seed.ledger}
		if err := led.FundAgent(ctx, ns, "ag", mustAmount(t, seed.amount, seed.currency).Value); err != nil {
			t.Fatalf("fund agent in %s: %v", seed.currency, err)
		}
	}
	return a, ns
}

// mcEURLedger is the EUR ledger number. tbConfLedger is already the USD one, so
// only the second is named here.
const mcEURLedger uint32 = 978

// mcBalance reads the agent's settled balance in one currency, in minor units.
func mcBalance(t *testing.T, a billing.Adapter, currency string) int64 {
	t.Helper()
	bal, err := a.GetBalance(context.Background(), "ag", currency)
	if err != nil {
		t.Fatalf("GetBalance(ag, %s): %v", currency, err)
	}
	if bal.Currency != currency {
		t.Fatalf("GetBalance(ag, %s) returned a %s amount", currency, bal.Currency)
	}
	minor, err := money.MinorUnits(bal.Value)
	if err != nil {
		t.Fatalf("balance %s not representable at the asset scale: %v", bal.Value, err)
	}
	return minor.Int64()
}

// mcAuthorize authorizes a single unit at cost in currency, under key.
func mcAuthorize(
	t *testing.T, a billing.Adapter, currency, cost, key string,
) billing.AuthorizeResult {
	t.Helper()
	res, err := a.Authorize(context.Background(), billing.AuthorizeRequest{
		BillingRef: "ag", UnitCost: mustAmount(t, cost, currency), Quantity: 1,
		IdempotencyKey: key, ResourceOwnerID: mcOwner, FeeRateBps: mcFeeBps,
	})
	if err != nil {
		t.Fatalf("Authorize(%s %s): %v", cost, currency, err)
	}
	return res
}

// TestMultiCurrency_ChargeTouchesOnlyItsOwnCurrency settles one charge in each
// currency on one adapter and checks that each debit came out of its own
// balance. If the two currencies shared an account or a ledger, one of these
// balances would be short by the other's charge.
func TestMultiCurrency_ChargeTouchesOnlyItsOwnCurrency(t *testing.T) {
	a, ns := mcAdapter(t, "10.00", "4.00")
	ctx := context.Background()

	eur := mcAuthorize(t, a, mcEUR, "1.00", "mc-eur")
	if !eur.Approved {
		t.Fatalf("EUR authorize denied: %q", eur.Reason)
	}
	if err := a.Record(ctx, eur.BillingID, 1, "mc-eur-rec"); err != nil {
		t.Fatalf("Record(EUR): %v", err)
	}
	// The USD balance must be untouched by the EUR settlement.
	if got := mcBalance(t, a, mcUSD); got != mustMinor(t, "4.00") {
		t.Errorf("USD balance after a EUR charge = %d, want %d (untouched)",
			got, mustMinor(t, "4.00"))
	}

	usd := mcAuthorize(t, a, mcUSD, "2.50", "mc-usd")
	if !usd.Approved {
		t.Fatalf("USD authorize denied: %q", usd.Reason)
	}
	if err := a.Record(ctx, usd.BillingID, 1, "mc-usd-rec"); err != nil {
		t.Fatalf("Record(USD): %v", err)
	}

	if got, want := mcBalance(t, a, mcEUR), mustMinor(t, "9.00"); got != want {
		t.Errorf("EUR balance = %d, want %d", got, want)
	}
	if got, want := mcBalance(t, a, mcUSD), mustMinor(t, "1.50"); got != want {
		t.Errorf("USD balance = %d, want %d", got, want)
	}
	// Each settlement's commission lands on its own currency's platform account,
	// so the two fee accounts hold 0.10 EUR and 0.25 USD and neither holds both.
	eurLedger := tbtest.Ledger{Client: tbClient, ID: mcEURLedger}
	eurLedger.AssertOwnerPlatformSplit(t, ns, mcOwner, mustMinor(t, "0.90"), mustMinor(t, "0.10"))
	confLedger().AssertOwnerPlatformSplit(t, ns, mcOwner, mustMinor(t, "2.25"), mustMinor(t, "0.25"))
}

// TestMultiCurrency_InsufficientInOneCurrencyIsDenied is the negative path that
// proves the balances are not pooled: the agent holds plenty of USD and almost
// no EUR, and a EUR charge above the EUR balance is denied even though the
// combined value across both currencies would cover it.
func TestMultiCurrency_InsufficientInOneCurrencyIsDenied(t *testing.T) {
	a, _ := mcAdapter(t, "0.50", "100.00")

	res := mcAuthorize(t, a, mcEUR, "5.00", "mc-short")
	if res.Approved {
		t.Fatal("a 5.00 EUR charge against a 0.50 EUR balance was approved")
	}
	if res.Reason != "insufficient balance" {
		t.Errorf("denial reason = %q, want \"insufficient balance\"", res.Reason)
	}
	// The USD balance must not have been consulted, let alone drawn on.
	if got, want := mcBalance(t, a, mcUSD), mustMinor(t, "100.00"); got != want {
		t.Errorf("USD balance = %d, want %d (untouched)", got, want)
	}
}

// TestMultiCurrency_UnconfiguredCurrencyIsDenied: a charge in a currency the
// deployment does not bill in is a soft denial, not an error and not a charge
// against some other ledger.
func TestMultiCurrency_UnconfiguredCurrencyIsDenied(t *testing.T) {
	a, _ := mcAdapter(t, "10.00", "10.00")

	res := mcAuthorize(t, a, "GBP", "1.00", "mc-gbp")
	if res.Approved {
		t.Fatal("a GBP charge was approved on a EUR/USD deployment")
	}
	if res.Reason != "currency mismatch" {
		t.Errorf("denial reason = %q, want \"currency mismatch\"", res.Reason)
	}
	for _, currency := range []string{mcEUR, mcUSD} {
		if got, want := mcBalance(t, a, currency), mustMinor(t, "10.00"); got != want {
			t.Errorf("%s balance = %d, want %d (untouched)", currency, got, want)
		}
	}
	// The read side refuses the same currency, rather than answering 0 for a
	// ledger this deployment has no accounts on.
	if _, err := a.GetBalance(context.Background(), "ag", "GBP"); err == nil {
		t.Error("GetBalance(ag, GBP) succeeded on a EUR/USD deployment")
	}
}

// TestMultiCurrency_RefundReversesInTheChargeCurrency settles a EUR charge and
// reverses it. The reversal must land back on the EUR balance, and a reversal
// that names the other currency must be refused rather than paid out of the
// wrong ledger.
func TestMultiCurrency_RefundReversesInTheChargeCurrency(t *testing.T) {
	a, _ := mcAdapter(t, "10.00", "10.00")
	ctx := context.Background()

	res := mcAuthorize(t, a, mcEUR, "2.00", "mc-ref")
	if !res.Approved {
		t.Fatalf("EUR authorize denied: %q", res.Reason)
	}
	if err := a.Record(ctx, res.BillingID, 1, "mc-ref-rec"); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// A refund amount labelled USD reverses a EUR charge: refused.
	err := a.Refund(ctx, res.BillingID, mustAmount(t, "2.00", mcUSD), "wrong currency", "mc-ref-bad")
	if !errors.Is(err, billing.ErrInvalidAmount) {
		t.Fatalf("Refund with a USD amount against a EUR charge = %v, want ErrInvalidAmount", err)
	}

	if err := a.Refund(
		ctx, res.BillingID, mustAmount(t, "2.00", mcEUR), "duplicate charge", "mc-ref-ok",
	); err != nil {
		t.Fatalf("Refund(EUR): %v", err)
	}
	if got, want := mcBalance(t, a, mcEUR), mustMinor(t, "10.00"); got != want {
		t.Errorf("EUR balance after a full refund = %d, want %d", got, want)
	}
	if got, want := mcBalance(t, a, mcUSD), mustMinor(t, "10.00"); got != want {
		t.Errorf("USD balance = %d, want %d (untouched by a EUR refund)", got, want)
	}
}

// TestMultiCurrency_CreditNeedsACurrencyBearingKey documents the one sharp edge
// of the shared credit slot: a TigerBeetle transfer id is unique per cluster,
// not per ledger, so one key names ONE slot across every currency. Two credits
// in different currencies under the same key leave the second one a silent
// no-op; the same two credits under keys that carry the currency both apply.
// This is exactly why WelcomeCreditKey includes the currency.
func TestMultiCurrency_CreditNeedsACurrencyBearingKey(t *testing.T) {
	a, _ := mcAdapter(t, "", "")
	ctx := context.Background()
	if err := a.EnsureAgentAccount(ctx, "ag"); err != nil {
		t.Fatalf("EnsureAgentAccount: %v", err)
	}

	// One shared key: the EUR credit lands, the USD credit finds the slot taken.
	if err := a.Credit(ctx, "ag", mustAmount(t, "5.00", mcEUR), "shared-slot"); err != nil {
		t.Fatalf("Credit(EUR): %v", err)
	}
	if err := a.Credit(ctx, "ag", mustAmount(t, "5.00", mcUSD), "shared-slot"); err != nil {
		t.Fatalf("Credit(USD) under a taken slot must be a no-op success, got %v", err)
	}
	if got, want := mcBalance(t, a, mcEUR), mustMinor(t, "5.00"); got != want {
		t.Errorf("EUR balance = %d, want %d", got, want)
	}
	if got := mcBalance(t, a, mcUSD); got != 0 {
		t.Errorf("USD balance = %d, want 0: the shared key was already spent on EUR", got)
	}

	// Keys that carry the currency occupy separate slots, so both apply.
	for _, c := range []struct{ currency, amount string }{
		{mcEUR, "1.00"}, {mcUSD, "2.00"},
	} {
		if err := a.Credit(ctx, "ag", mustAmount(t, c.amount, c.currency), "per-currency:"+c.currency); err != nil {
			t.Fatalf("Credit(%s): %v", c.currency, err)
		}
	}
	if got, want := mcBalance(t, a, mcEUR), mustMinor(t, "6.00"); got != want {
		t.Errorf("EUR balance = %d, want %d", got, want)
	}
	if got, want := mcBalance(t, a, mcUSD), mustMinor(t, "2.00"); got != want {
		t.Errorf("USD balance = %d, want %d", got, want)
	}
}

// TestMultiCurrency_EnsureAgentAccountCoversEveryCurrency: Register creates the
// agent's account on every configured ledger, so a balance read in a currency
// that was never funded answers zero instead of failing.
func TestMultiCurrency_EnsureAgentAccountCoversEveryCurrency(t *testing.T) {
	a, _ := newTBAdapterFor("mc-ensure", time.Minute, mcEUR, mcUSD)
	ctx := context.Background()
	if err := a.EnsureAgentAccount(ctx, "ag"); err != nil {
		t.Fatalf("EnsureAgentAccount: %v", err)
	}
	if err := a.EnsureAgentAccount(ctx, "ag"); err != nil {
		t.Fatalf("repeat EnsureAgentAccount must be a no-op success, got %v", err)
	}
	for _, currency := range []string{mcEUR, mcUSD} {
		bal, err := a.GetBalance(ctx, "ag", currency)
		if err != nil {
			t.Fatalf("GetBalance(ag, %s): %v", currency, err)
		}
		if bal.Value.Sign() != 0 {
			t.Errorf("%s balance = %s, want 0", currency, bal.Value.FloatString(8))
		}
	}
}

// TestMultiCurrency_ConstructorRejectsBadCurrencySets pins the boot-time guard:
// an empty set, a duplicate, and a currency with no ledger are all refused, so a
// misconfigured deployment fails to start instead of deriving ids for a currency
// it cannot place.
func TestMultiCurrency_ConstructorRejectsBadCurrencySets(t *testing.T) {
	for name, currencies := range map[string][]string{
		"empty":       {},
		"duplicate":   {mcEUR, mcEUR},
		"unsupported": {mcEUR, "GBP"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := billing.NewTigerBeetleAdapter(billing.TigerBeetleOptions{
				Client: tbClient, Currencies: currencies,
			}); err == nil {
				t.Fatalf("NewTigerBeetleAdapter(%v) succeeded, want an error", currencies)
			}
		})
	}
}

// TestMultiCurrency_AvailableBalance_CurrencyAddedAfterRegistration: an agent
// registered while the deployment billed only in USD. EUR is added later, so the
// agent has no EUR account. AvailableBalance reports ErrNoLedgerAccount for EUR
// and still reports USD.
func TestMultiCurrency_AvailableBalance_CurrencyAddedAfterRegistration(t *testing.T) {
	ctx := context.Background()
	before, ns := newTBAdapterFor("mc-added", time.Minute, mcUSD)
	if err := before.EnsureAgentAccount(ctx, "ag"); err != nil {
		t.Fatalf("EnsureAgentAccount: %v", err)
	}
	after, err := billing.NewTigerBeetleAdapter(billing.TigerBeetleOptions{
		Client: tbClient, Currencies: []string{mcUSD, mcEUR}, HoldTimeout: time.Minute, IDNamespace: ns,
	})
	if err != nil {
		t.Fatalf("NewTigerBeetleAdapter: %v", err)
	}
	if _, err := after.AvailableBalance(ctx, "ag", mcEUR); !errors.Is(err, billing.ErrNoLedgerAccount) {
		t.Fatalf("AvailableBalance(EUR) err = %v, want ErrNoLedgerAccount", err)
	}
	usd, err := after.AvailableBalance(ctx, "ag", mcUSD)
	if err != nil {
		t.Fatalf("AvailableBalance(USD): %v", err)
	}
	if usd.Value.Sign() != 0 || usd.Currency != mcUSD {
		t.Errorf("AvailableBalance(USD) = %s %s, want 0 USD", usd.Value.FloatString(8), usd.Currency)
	}
}
