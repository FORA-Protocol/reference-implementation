package main

import (
	"slices"
	"strings"
	"testing"
)

// EXCHANGE_BILLING_LEDGER carries the currencies a deployment bills in, and its
// ORDER decides what the Exchange advertises as base_currency (the first entry).
// These cases pin both halves of that rule at the one place the variable is
// parsed, because nothing downstream can tell a reordered list from an intended
// one.
func TestBillingLedgerCurrencies_ListAndOrder(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"single ledger behaves as before", "978", []string{"EUR"}},
		{"two ledgers, EUR advertised", "978,840", []string{"EUR", "USD"}},
		// The same two currencies in the other order advertise the other one.
		// This is the documented consequence of reordering, not a bug.
		{"two ledgers, USD advertised", "840,978", []string{"USD", "EUR"}},
		{"surrounding spaces are tolerated", " 978 , 840 ", []string{"EUR", "USD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EXCHANGE_BILLING_LEDGER", tc.raw)
			got, err := billingLedgerCurrencies()
			if err != nil {
				t.Fatalf("billingLedgerCurrencies(%q): %v", tc.raw, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("currencies = %q, want %q", got, tc.want)
			}
			// The advertised base currency is the first entry, always.
			if got[0] != tc.want[0] {
				t.Errorf("advertised base currency = %q, want %q", got[0], tc.want[0])
			}
		})
	}
}

// Every rejected shape must name what was wrong, so an operator reading the
// startup failure can fix the variable without reading source.
func TestBillingLedgerCurrencies_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name, raw, wantIn string
	}{
		{"unset", "", "required"},
		{"unsupported numeric", "826", "unsupported"},
		{"unsupported numeric in a list", "978,826", "unsupported"},
		{"duplicate", "978,978", "more than once"},
		{"duplicate at the end of a longer list", "840,978,840", "more than once"},
		{"alpha code instead of numeric", "EUR", "invalid"},
		{"trailing comma", "978,", "invalid"},
		{"only a comma", ",", "invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("EXCHANGE_BILLING_LEDGER", tc.raw)
			got, err := billingLedgerCurrencies()
			if err == nil {
				t.Fatalf("billingLedgerCurrencies(%q) = %q, want an error", tc.raw, got)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
		})
	}
}

// The parse and the advertised value are wired together here: whatever
// EXCHANGE_BILLING_LEDGER resolves to, the well-known document publishes its
// first entry. Testing the two halves separately would leave the wiring between
// them — the one line that reads index 0 — unasserted.
func TestBaseCurrencyIsTheFirstConfiguredLedger(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{"978", "EUR"},
		{"978,840", "EUR"},
		{"840,978", "USD"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("EXCHANGE_BILLING_LEDGER", tc.raw)
			currencies, err := billingLedgerCurrencies()
			if err != nil {
				t.Fatalf("billingLedgerCurrencies(%q): %v", tc.raw, err)
			}
			if got := (muxDeps{ledgerCurrencies: currencies}).baseCurrency(); got != tc.want {
				t.Errorf("advertised base_currency = %q, want %q", got, tc.want)
			}
		})
	}
	// An unwired list must not take the boot down; it publishes no base currency.
	if got := (muxDeps{}).baseCurrency(); got != "" {
		t.Errorf("baseCurrency() with no configured currencies = %q, want \"\"", got)
	}
}
