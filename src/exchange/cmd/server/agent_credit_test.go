package main

import (
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/testutil"
)

// EXCHANGE_DEFAULT_AGENT_CREDIT is a parser over an operator-written money
// knob, which is the unit-test category the Testing Doctrine permits. What the
// parsed map does at Register is covered end to end through the RPC surface in
// the transport package.

// creditBootLedgers are the currencies this test's deployment bills in — the
// value selectAdapters would hand the boot step. A welcome credit in any other
// currency must be refused.
var creditBootLedgers = []string{"EUR", "USD"}

// assertCredits checks a parsed map against decimal strings by currency.
func assertCredits(t *testing.T, raw string, want map[string]string) {
	t.Helper()
	t.Setenv(envDefaultAgentCredit, raw)
	got, err := loadDefaultAgentCredits(testutil.DiscardLogger(), creditBootLedgers)
	if err != nil {
		t.Fatalf("load(%q): %v", raw, err)
	}
	if len(got) != len(want) {
		t.Fatalf("load(%q) = %v, want %d currencies", raw, got, len(want))
	}
	for currency, amount := range want {
		if got[currency] == nil || got[currency].Cmp(testutil.MustRat(t, amount)) != 0 {
			t.Fatalf("load(%q): %s = %v, want %s", raw, currency, got[currency], amount)
		}
	}
}

// TestLoadDefaultAgentCredits_Accepts: a set variable parses to a per-currency
// map; a deployment billing in two currencies may configure only one; and an
// unset or empty variable is an empty map, which disables the grant. The
// finest amount the ledger can post and the widest one the money columns allow
// both parse.
func TestLoadDefaultAgentCredits_Accepts(t *testing.T) {
	assertCredits(t, "EUR=100,USD=50", map[string]string{"EUR": "100", "USD": "50"})
	assertCredits(t, " EUR = 12.5 , USD=0 ", map[string]string{"EUR": "12.5", "USD": "0"})
	assertCredits(t, "USD=25", map[string]string{"USD": "25"})
	assertCredits(t, "EUR=0.00000001", map[string]string{"EUR": "0.00000001"})
	assertCredits(t, "EUR=999999999999.99999999", map[string]string{"EUR": "999999999999.99999999"})
	assertCredits(t, "", map[string]string{})
	assertCredits(t, "   ", map[string]string{})
}

// TestLoadDefaultAgentCredits_RejectsMalformedValues: a value that is not a
// currency=amount map, an amount that is not a plain decimal (digits with an
// optional fraction), is finer than ledger asset scale 8 or is wider than 12
// integer digits, and a currency this deployment does not bill in all fail the
// boot step — bad configuration surfaces loudly instead of rounding, guessing a
// currency, or silently disabling. The non-plain shapes big.Rat.SetString would
// happily parse (exponent, fraction, hex float, digit separator) are pinned as
// rejected, and so is the BARE NUMBER this variable carried while the Exchange
// was single-currency: there is no longer a currency to infer.
func TestLoadDefaultAgentCredits_RejectsMalformedValues(t *testing.T) {
	for name, raw := range map[string]string{
		"bare number (the old single-currency format)": "100",
		"not a decimal":                 "EUR=ten",
		"negative":                      "EUR=-5",
		"finer than scale 8":            "EUR=0.000000001",
		"exceeds 12 integer digits":     "EUR=1000000000000",
		"exponent":                      "EUR=1e9",
		"rational fraction":             "EUR=5/2",
		"hex float":                     "EUR=0x10p2",
		"digit separator":               "EUR=1_000",
		"currency not billed in":        "GBP=10",
		"lowercase currency":            "eur=10",
		"numeric currency code":         "978=10",
		"over-long currency":            "EURO=10",
		"duplicate currency":            "EUR=10,EUR=20",
		"missing amount":                "EUR=",
		"trailing comma":                "EUR=10,",
		"one bad entry among good ones": "EUR=10,GBP=10",
	} {
		t.Setenv(envDefaultAgentCredit, raw)
		if got, err := loadDefaultAgentCredits(testutil.DiscardLogger(), creditBootLedgers); err == nil {
			t.Errorf("%s (%q): expected boot to fail, got %v", name, raw, got)
		}
	}
}

// TestRenderCredits_IsStable: the boot log line is the same for the same
// configuration whatever order Go iterates the map in, and an empty map renders
// as nothing.
func TestRenderCredits_IsStable(t *testing.T) {
	credits, err := parseDefaultAgentCredits("USD=50,EUR=12.5", creditBootLedgers)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	const want = "EUR=12.50000000,USD=50.00000000"
	for range 20 {
		if got := renderCredits(credits); got != want {
			t.Fatalf("renderCredits = %q, want %q", got, want)
		}
	}
	if got := renderCredits(nil); got != "" {
		t.Fatalf("renderCredits(nil) = %q, want empty", got)
	}
}
