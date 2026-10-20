package main

import (
	"fmt"
	"log/slog"
	"math/big"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/runhttp"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/money"
)

// envDefaultAgentCredit is the welcome credit a newly registered agent receives
// at its first Register — the SOLE configuration channel for it (the ADR-009
// amendment of 2026-08-13). There is no admin RPC and no database column: the
// Exchange parses this variable once at boot and keeps the result in
// ExchangeConfig.DefaultAgentCredits, so unset (or empty) means no grant, and
// changing or removing the variable takes effect at the next restart.
//
// The value is a per-currency map, written as comma-separated CODE=AMOUNT pairs:
// "EUR=50,USD=50". A currency the deployment does not bill in fails boot, and a
// currency the deployment bills in but the map omits simply gets no grant.
const envDefaultAgentCredit = "EXCHANGE_DEFAULT_AGENT_CREDIT"

// creditMaxIntegerDigits is the integer-digit capacity a welcome-credit amount
// is held to: 20 total digits minus money.AssetScale (8) fraction digits, the
// NUMERIC(20,8) width every money column in this schema has. A credit wider than
// that could not be recorded anywhere else money is stored.
const creditMaxIntegerDigits = 12

// decimalCreditPattern pins the accepted amount shape to a plain decimal —
// digits with an optional fraction. big.Rat.SetString alone would also accept
// exponents ("1e9"), fractions ("5/2"), hex floats ("0x10p2") and digit
// separators ("1_000"); none of those belong in a money knob, so the shape is
// checked before parsing. The pattern admits no sign, so no amount is negative.
var decimalCreditPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)

// currencyCodePattern is the accepted key shape: an ISO 4217 alpha code.
var currencyCodePattern = regexp.MustCompile(`^[A-Z]{3}$`)

// loadDefaultAgentCredits reads and parses EXCHANGE_DEFAULT_AGENT_CREDIT and
// logs the result, so every boot states the welcome credit it will grant. A
// malformed value is returned as an error, which fails boot.
func loadDefaultAgentCredits(logger *slog.Logger, ledgerCurrencies []string) (map[string]*big.Rat, error) {
	credits, err := parseDefaultAgentCredits(runhttp.EnvOr(envDefaultAgentCredit, ""), ledgerCurrencies)
	if err != nil {
		return nil, err
	}
	logger.Info("default agent credits configured", "default_agent_credits", renderCredits(credits))
	return credits, nil
}

// parseDefaultAgentCredits parses EXCHANGE_DEFAULT_AGENT_CREDIT into a map of
// amounts by ISO 4217 alpha code. An empty value is an empty map, which
// disables the grant.
//
// Every failure is a boot failure with a message naming the offending text,
// because there is no safe interpretation of a malformed money knob. In
// particular a BARE NUMBER — the format this variable carried while the Exchange
// was single-currency — is refused rather than guessed at: the Exchange can no
// longer tell which currency the operator meant, and picking one would grant
// money in a currency nobody asked for.
func parseDefaultAgentCredits(raw string, ledgerCurrencies []string) (map[string]*big.Rat, error) {
	credits := map[string]*big.Rat{}
	if strings.TrimSpace(raw) == "" {
		return credits, nil
	}
	for _, field := range strings.Split(raw, ",") {
		entry := strings.TrimSpace(field)
		currency, amount, err := parseCreditEntry(entry, raw)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ledgerCurrencies, currency) {
			return nil, fmt.Errorf(
				"%s grants %s, which this deployment does not bill in (EXCHANGE_BILLING_LEDGER gives %s)",
				envDefaultAgentCredit, currency, strings.Join(ledgerCurrencies, ","))
		}
		if _, dup := credits[currency]; dup {
			return nil, fmt.Errorf("%s lists %s more than once", envDefaultAgentCredit, currency)
		}
		credits[currency] = amount
	}
	return credits, nil
}

// parseCreditEntry splits and validates one CODE=AMOUNT pair. raw is the whole
// variable value, quoted in the error so an operator who wrote the old bare-
// number format sees what they actually set.
func parseCreditEntry(entry, raw string) (string, *big.Rat, error) {
	currency, amountText, found := strings.Cut(entry, "=")
	if !found {
		return "", nil, fmt.Errorf(
			"%s %q is not a currency=amount map (expected e.g. \"EUR=50,USD=50\")",
			envDefaultAgentCredit, raw)
	}
	currency = strings.TrimSpace(currency)
	amountText = strings.TrimSpace(amountText)
	if !currencyCodePattern.MatchString(currency) {
		return "", nil, fmt.Errorf("%s: %q is not an ISO 4217 alpha currency code",
			envDefaultAgentCredit, currency)
	}
	if !decimalCreditPattern.MatchString(amountText) {
		return "", nil, fmt.Errorf(
			"%s: amount %q for %s is not a plain decimal (digits with an optional fraction)",
			envDefaultAgentCredit, amountText, currency)
	}
	amount, ok := new(big.Rat).SetString(amountText)
	if !ok {
		return "", nil, fmt.Errorf("%s: amount %q for %s is not a decimal",
			envDefaultAgentCredit, amountText, currency)
	}
	if err := checkCreditBounds(currency, amountText, amount); err != nil {
		return "", nil, err
	}
	return currency, amount, nil
}

// checkCreditBounds refuses an amount the ledger cannot post exactly: one that
// is not a whole number of minor units at ledger asset scale 8 (the exactness
// rule is money.MinorUnits, the same conversion the billing adapter posts
// with), or one wider than the 12 integer digits every money column allows.
func checkCreditBounds(currency, amountText string, amount *big.Rat) error {
	if _, err := money.MinorUnits(amount); err != nil {
		return fmt.Errorf("%s: amount %q for %s is finer than asset scale %d",
			envDefaultAgentCredit, amountText, currency, money.AssetScale)
	}
	limit := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(creditMaxIntegerDigits), nil))
	if amount.Cmp(limit) >= 0 {
		return fmt.Errorf("%s: amount %q for %s exceeds %d integer digits",
			envDefaultAgentCredit, amountText, currency, creditMaxIntegerDigits)
	}
	return nil
}

// renderCredits formats a credits map for the boot log in a stable order, so
// two boots with the same configuration print the same line. An empty map
// renders as the empty string: no grant.
func renderCredits(credits map[string]*big.Rat) string {
	currencies := make([]string, 0, len(credits))
	for currency := range credits {
		currencies = append(currencies, currency)
	}
	sort.Strings(currencies)
	parts := make([]string, 0, len(currencies))
	for _, currency := range currencies {
		parts = append(parts, currency+"="+money.DecimalString(credits[currency]))
	}
	return strings.Join(parts, ",")
}
