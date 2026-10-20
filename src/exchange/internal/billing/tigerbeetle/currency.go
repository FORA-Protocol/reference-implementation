package tigerbeetle

import (
	"fmt"
	"sort"
	"strings"
)

// The Exchange keeps one TigerBeetle ledger per currency, numbered with that
// currency's ISO 4217 numeric code. ledgerCurrencies is the source of truth
// for which currencies a deployment may configure: boot validates
// EXCHANGE_BILLING_LEDGER against it, the billing adapter resolves a charge's
// ledger through it, and the account-id derivation reads the alpha code from it.
// Nothing else in the GO code is currency-specific, so one entry here covers
// every Go caller.
//
// OUTSIDE Go, three more places enumerate the same set and CANNOT read this
// map: the Terraform validation on the billing_ledger variable, which would
// otherwise reject the new code at plan time, and the currency-to-ledger tables
// in the funding and balance operator scripts, which are shell. Adding a
// currency the Exchange has never run therefore means four production edits,
// not one. They are listed in the add-a-currency procedure in the production
// operations guide, and a guard test
// (deploy/terraform/scripts/tests/currency-table-consistency-test.py) fails
// when the four stop agreeing — so this comment cannot quietly go stale again.
// It said the opposite once, which is how the omission was found.
var ledgerCurrencies = map[uint32]string{
	978: "EUR",
	840: "USD",
}

// CurrencyForLedger returns the ISO 4217 alpha code for a ledger number and
// whether that ledger is one the Exchange supports.
func CurrencyForLedger(ledger uint32) (string, bool) {
	currency, ok := ledgerCurrencies[ledger]
	return currency, ok
}

// LedgerForCurrency returns the ledger number for an ISO 4217 alpha code and
// whether that currency is one the Exchange supports. It is the inverse of
// CurrencyForLedger.
func LedgerForCurrency(currency string) (uint32, bool) {
	for ledger, alpha := range ledgerCurrencies {
		if alpha == currency {
			return ledger, true
		}
	}
	return 0, false
}

// SupportedLedgers renders the supported ledgers as "978=EUR, 840=USD", ordered
// by ledger number. Configuration errors name the supported set by calling this
// rather than repeating the literals, so a currency added to the table above
// appears in every message without a second edit.
func SupportedLedgers() string {
	numbers := make([]uint32, 0, len(ledgerCurrencies))
	for ledger := range ledgerCurrencies {
		numbers = append(numbers, ledger)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	parts := make([]string, 0, len(numbers))
	for _, ledger := range numbers {
		parts = append(parts, fmt.Sprintf("%d=%s", ledger, ledgerCurrencies[ledger]))
	}
	return strings.Join(parts, ", ")
}
