package billing_test

import (
	"context"
	"errors"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/billing"
)

// AvailableBalance cases of the adapter conformance suite (registered in
// runAdapterConformance, adapter_conformance_test.go). The available balance is
// what an agent can spend now: the settled balance minus live holds.

// unbilledCurrency is a real ISO 4217 code that no conformance adapter bills in.
const unbilledCurrency = "JPY"

// assertAvailable reads the available balance of "ag" and compares it to want.
// Every read also checks the amount is never negative and carries the asked
// currency.
func assertAvailable(t *testing.T, a billing.Adapter, want string) {
	t.Helper()
	got, err := a.AvailableBalance(context.Background(), "ag", confCurrency)
	if err != nil {
		t.Fatalf("AvailableBalance: %v", err)
	}
	if got.Value.Sign() < 0 {
		t.Fatalf("AvailableBalance = %s, must never be negative", got.Value.FloatString(8))
	}
	if got.Currency != confCurrency {
		t.Errorf("AvailableBalance currency = %q, want %q", got.Currency, confCurrency)
	}
	if w := mustAmount(t, want, confCurrency); got.Value.Cmp(w.Value) != 0 {
		t.Errorf("AvailableBalance = %s, want %s", got.Value.FloatString(8), want)
	}
}

// confAvailableExcludesHold: a live hold lowers the available balance while the
// settled balance stays unchanged. Release returns the held amount, and Record
// leaves the available balance equal to the new settled balance.
func confAvailableExcludesHold(t *testing.T, f adapterFactory) {
	ctx := context.Background()
	a := seedTen(t, f)
	assertAvailable(t, a, "10.00")

	released := authHold(t, a, 10, "k-avail-1") // hold 0.50
	assertAvailable(t, a, "9.50")
	assertBalance(t, a, "10.00")
	if err := a.Release(ctx, released, "k-avail-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	assertAvailable(t, a, "10.00")

	recorded := authHold(t, a, 10, "k-avail-2")
	if err := a.Record(ctx, recorded, 10, "k-avail-2"); err != nil {
		t.Fatalf("record: %v", err)
	}
	assertAvailable(t, a, "9.50")
	assertBalance(t, a, "9.50")
}

// confAvailableNoLedgerAccount: the deployment bills in the currency, but the
// billing_ref has no account in it.
func confAvailableNoLedgerAccount(t *testing.T, f adapterFactory) {
	a := seedTen(t, f)
	_, err := a.AvailableBalance(context.Background(), "never-registered", confCurrency)
	if !errors.Is(err, billing.ErrNoLedgerAccount) {
		t.Fatalf("AvailableBalance(unknown ref) err = %v, want ErrNoLedgerAccount", err)
	}
}

// confAvailableUnbilledCurrency: a currency the deployment does not bill in is a
// configuration error, not a missing account, even for an existing account.
func confAvailableUnbilledCurrency(t *testing.T, f adapterFactory) {
	a := seedTen(t, f)
	_, err := a.AvailableBalance(context.Background(), "ag", unbilledCurrency)
	if err == nil {
		t.Fatalf("AvailableBalance(%s) = nil error, want a configuration error", unbilledCurrency)
	}
	if errors.Is(err, billing.ErrNoLedgerAccount) {
		t.Fatalf("AvailableBalance(%s) err = %v, must not be ErrNoLedgerAccount", unbilledCurrency, err)
	}
}

// TestInMemoryAvailableBalance_OtherCurrencyAccount: the in-memory tier holds one
// balance per account. An account seeded in another currency has no ledger
// account in the tier's currency, so AvailableBalance honours the argument
// instead of returning the other currency's money.
func TestInMemoryAvailableBalance_OtherCurrencyAccount(t *testing.T) {
	t.Parallel()
	a := billing.NewInMemoryAdapter(billing.InMemoryOptions{
		Balances: map[string]billing.Amount{"eur-ag": mustAmount(t, "5.00", "EUR")},
	})
	_, err := a.AvailableBalance(context.Background(), "eur-ag", billing.DemoCurrency)
	if !errors.Is(err, billing.ErrNoLedgerAccount) {
		t.Fatalf("AvailableBalance err = %v, want ErrNoLedgerAccount", err)
	}
}

// TestFreeAdapter_AvailableBalanceNotReported: the free tier keeps no balance,
// so it never reports its GetBalance stand-in as money.
func TestFreeAdapter_AvailableBalanceNotReported(t *testing.T) {
	t.Parallel()
	_, err := billing.FreeAdapter{}.AvailableBalance(context.Background(), "billing-ref-1", billing.DemoCurrency)
	if !errors.Is(err, billing.ErrBalanceNotReported) {
		t.Fatalf("AvailableBalance err = %v, want ErrBalanceNotReported", err)
	}
}

// TestInMemoryAvailableBalance_NegativeSeedReportsZero: a ledger that shows less
// than nothing has nothing to spend, so the available balance is zero, never
// negative. Only a direct seed can put the in-memory tier in this state;
// TigerBeetle agent accounts are debit-capped and cannot reach it.
func TestInMemoryAvailableBalance_NegativeSeedReportsZero(t *testing.T) {
	t.Parallel()
	a := billing.NewInMemoryAdapter(billing.InMemoryOptions{
		Balances: map[string]billing.Amount{"ag": mustAmount(t, "-1.00", confCurrency)},
	})
	assertAvailable(t, a, "0")
}
