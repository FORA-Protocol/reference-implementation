package rsl_test

import (
	"strings"
	"testing"

	forav1 "github.com/FORA-Protocol/protocol/gen/go/fora/v1"
	"github.com/FORA-Protocol/protocol/sdk/go/helpers"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/rsl"
)

var testDefaults = rsl.Defaults{Currency: "EUR", Domain: "publisher.example"}

// mapFirst parses a one-rule document and maps it, which is what every mapping
// case does before it asserts anything.
func mapFirst(t *testing.T, license string) rsl.Mapped {
	t.Helper()
	body := `<rsl xmlns="https://rslstandard.org/rsl"><content url="/">` + license + `</content></rsl>`
	doc, err := rsl.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	content, ok := doc.Match("https://publisher.example/news/a")
	if !ok {
		t.Fatal("the root rule did not match")
	}
	m, err := rsl.ToLicenseTerms(content, testDefaults)
	if err != nil {
		t.Fatalf("map: %v", err)
	}
	return m
}

// assertAccepted checks that the Exchange would take these terms. A mapping
// that produces a term the Exchange refuses is worse than no mapping, because
// the publisher is shown a preview they could never publish.
func assertAccepted(t *testing.T, terms []*forav1.LicenseTerm) {
	t.Helper()
	title := "Midnight Harbour"
	entry := &forav1.ResourceEntry{
		Domain: "publisher.example",
		Path:   "/news/a",
		Title:  &title,
		Terms:  terms,
	}
	verdict := helpers.ValidateResourceEntry(entry)
	if !verdict.OK() {
		t.Fatalf("the mapped terms would be refused: %+v", verdict.Violations)
	}
}

// One case per row of the payment-type mapping published with the protocol.
func TestPaymentTypeMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		payment     string
		wantModel   forav1.PricingModel
		wantRate    string
		wantUnit    string
		wantOblig   forav1.ObligationKind
		wantScope   string
		wantImplied string
	}{
		{
			name:      "free",
			payment:   `<payment type="free"/>`,
			wantModel: forav1.PricingModel_PRICING_MODEL_FREE,
			wantRate:  "0",
		},
		{
			name:      "attribution is free with a duty",
			payment:   `<payment type="attribution"/>`,
			wantModel: forav1.PricingModel_PRICING_MODEL_FREE,
			wantRate:  "0",
			wantOblig: forav1.ObligationKind_OBLIGATION_KIND_ATTRIBUTION,
		},
		{
			name:      "contribution is free with a duty",
			payment:   `<payment type="contribution"/>`,
			wantModel: forav1.PricingModel_PRICING_MODEL_FREE,
			wantRate:  "0",
			wantOblig: forav1.ObligationKind_OBLIGATION_KIND_CONTRIBUTION,
		},
		{
			name:      "subscription is free and scope gated",
			payment:   `<payment type="subscription"/>`,
			wantModel: forav1.PricingModel_PRICING_MODEL_FREE,
			wantRate:  "0",
			wantScope: "subscription:publisher.example",
		},
		{
			name:        "crawl is priced per fetch",
			payment:     `<payment type="crawl"><amount currency="EUR">0.02</amount></payment>`,
			wantModel:   forav1.PricingModel_PRICING_MODEL_PER_UNIT,
			wantRate:    "0.02",
			wantUnit:    "fetches",
			wantImplied: "crawl",
		},
		{
			name:        "training is priced per token",
			payment:     `<payment type="training"><amount currency="EUR">0.001</amount></payment>`,
			wantModel:   forav1.PricingModel_PRICING_MODEL_PER_UNIT,
			wantRate:    "0.001",
			wantUnit:    "tokens",
			wantImplied: "ai-train",
		},
		{
			name:        "use is priced per token",
			payment:     `<payment type="use"><amount currency="EUR">0.003</amount></payment>`,
			wantModel:   forav1.PricingModel_PRICING_MODEL_PER_UNIT,
			wantRate:    "0.003",
			wantUnit:    "tokens",
			wantImplied: "ai-input",
		},
		{
			name:        "inference is the same as use",
			payment:     `<payment type="inference"><amount currency="EUR">0.003</amount></payment>`,
			wantModel:   forav1.PricingModel_PRICING_MODEL_PER_UNIT,
			wantRate:    "0.003",
			wantUnit:    "tokens",
			wantImplied: "ai-input",
		},
		{
			name:      "purchase is a flat fee",
			payment:   `<payment type="purchase"><amount currency="EUR">3.50</amount></payment>`,
			wantModel: forav1.PricingModel_PRICING_MODEL_FLAT,
			// Canonical form, not the spelling the document used: money crosses
			// every FORA surface in one shape, so "3.50" is carried as "3.5".
			wantRate: "3.5",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := mapFirst(t, `<license>`+tc.payment+`</license>`)
			if len(m.Terms) != 1 {
				t.Fatalf("terms = %d, want 1 (warnings: %v)", len(m.Terms), m.Warnings)
			}
			term := m.Terms[0]
			assertAccepted(t, m.Terms)
			if term.GetSemantics() != forav1.TermSemantics_TERM_SEMANTICS_ENUMERATED {
				t.Fatalf("semantics = %v, want enumerated", term.GetSemantics())
			}
			if got := term.GetPricing().GetModel(); got != tc.wantModel {
				t.Fatalf("model = %v, want %v", got, tc.wantModel)
			}
			if got := term.GetPricing().GetRate(); got != tc.wantRate {
				t.Fatalf("rate = %q, want %q", got, tc.wantRate)
			}
			if got := term.GetPricing().GetUnit(); got != tc.wantUnit {
				t.Fatalf("unit = %q, want %q", got, tc.wantUnit)
			}
			assertObligation(t, term, tc.wantOblig)
			assertScope(t, term, tc.wantScope)
			assertPermitted(t, term, tc.wantImplied)
		})
	}
}

func assertObligation(t *testing.T, term *forav1.LicenseTerm, want forav1.ObligationKind) {
	t.Helper()
	if want == forav1.ObligationKind_OBLIGATION_KIND_UNSPECIFIED {
		if len(term.GetObligations()) != 0 {
			t.Fatalf("obligations = %+v, want none", term.GetObligations())
		}
		return
	}
	if len(term.GetObligations()) != 1 {
		t.Fatalf("obligations = %+v, want exactly one", term.GetObligations())
	}
	o := term.GetObligations()[0]
	if o.GetKind() != want {
		t.Fatalf("obligation kind = %v, want %v", o.GetKind(), want)
	}
	if o.GetTrigger() != forav1.ObligationTrigger_OBLIGATION_TRIGGER_ON_USE {
		t.Fatalf("obligation trigger = %v, want on use", o.GetTrigger())
	}
}

func assertScope(t *testing.T, term *forav1.LicenseTerm, want string) {
	t.Helper()
	if want == "" {
		if len(term.GetScopes()) != 0 {
			t.Fatalf("scopes = %v, want none", term.GetScopes())
		}
		return
	}
	if len(term.GetScopes()) != 1 || term.GetScopes()[0] != want {
		t.Fatalf("scopes = %v, want [%s]", term.GetScopes(), want)
	}
}

func assertPermitted(t *testing.T, term *forav1.LicenseTerm, token string) {
	t.Helper()
	if token == "" {
		return
	}
	for _, r := range term.GetRestrictions() {
		if r.GetKind() != forav1.RestrictionKind_RESTRICTION_KIND_FUNCTION {
			continue
		}
		for _, p := range r.GetPermitted() {
			if p == token {
				return
			}
		}
	}
	t.Fatalf("restrictions %+v do not permit %q, which the payment type names", term.GetRestrictions(), token)
}

// A payment type FORA has no model for produces no term and says so, rather
// than a term with a price nobody stated.
func TestUnknownPaymentTypeIsSkipped(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><payment type="barter"/></license>`)
	if len(m.Terms) != 0 {
		t.Fatalf("terms = %+v, want none", m.Terms)
	}
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "barter") {
		t.Fatalf("warnings = %v, want one naming the payment type", m.Warnings)
	}
}

// RSL reads a license with no payment as "ask the publisher", which is not a
// price this preview can show.
func TestMissingPaymentIsSkipped(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><permits type="usage">search</permits></license>`)
	if len(m.Terms) != 0 {
		t.Fatalf("terms = %+v, want none", m.Terms)
	}
	if len(m.Warnings) != 1 {
		t.Fatalf("warnings = %v, want one explaining the omission", m.Warnings)
	}
}

// A priced type with no amount is a statement with a hole in it, and the hole
// is the one part nothing can stand in for. Pricing it at zero would hand the
// publisher a preview in which their paid content is free, so the license is
// skipped and the omission is named.
func TestPricedTypeWithoutAnAmountIsSkipped(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><payment type="purchase"/></license>`)
	if len(m.Terms) != 0 {
		t.Fatalf("terms = %+v, want none; a priced license with no amount is not a free term", m.Terms)
	}
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "no amount") {
		t.Fatalf("warnings = %v, want one naming the missing amount", m.Warnings)
	}
	if !strings.Contains(m.Warnings[0], "not carried") {
		t.Fatalf("warning = %q, want it to say the license was not carried", m.Warnings[0])
	}
}

// A free type that also names a price contradicts itself. The charge is
// dropped, because a term that says free and charges cannot be acted on.
func TestFreeTypeWithAnAmountDropsTheCharge(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><payment type="contribution"><amount currency="EUR">5.00</amount></payment></license>`)
	if len(m.Terms) != 1 {
		t.Fatalf("terms = %d, want 1", len(m.Terms))
	}
	assertAccepted(t, m.Terms)
	if got := m.Terms[0].GetPricing().GetRate(); got != "0" {
		t.Fatalf("rate = %q, want 0 on a free term", got)
	}
	if len(m.Warnings) != 1 || !strings.Contains(m.Warnings[0], "5") {
		t.Fatalf("warnings = %v, want one naming the dropped amount", m.Warnings)
	}
}

// Money is a decimal string on every FORA surface. A price that is not one is
// neither rounded into one nor replaced by zero: the license is skipped, and
// both the value that could not be read and the consequence are reported.
func TestUnparseableAmountSkipsTheLicense(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><payment type="purchase"><amount currency="EUR">about five euro</amount></payment></license>`)
	if len(m.Terms) != 0 {
		t.Fatalf("terms = %+v, want none; an unreadable price is not a price of zero", m.Terms)
	}
	if !anyWarningContains(m.Warnings, "about five euro") {
		t.Fatalf("warnings = %v, want one quoting the value that could not be read", m.Warnings)
	}
	if !anyWarningContains(m.Warnings, "not carried") {
		t.Fatalf("warnings = %v, want one saying the license was not carried", m.Warnings)
	}
}

// A publisher who prices one use and leaves another free keeps the free one
// even when the priced one cannot be read. Skipping the license the document
// broke is not the same as discarding the document.
func TestOneUnreadableLicenseDoesNotLoseTheOthers(t *testing.T) {
	t.Parallel()
	m := mapFirst(t,
		`<license><payment type="free"/><permits type="usage">search</permits></license>`+
			`<license><payment type="training"><amount currency="EUR">nope</amount></payment></license>`)
	if len(m.Terms) != 1 {
		t.Fatalf("terms = %d, want the one license that could be read", len(m.Terms))
	}
	assertAccepted(t, m.Terms)
	if !anyWarningContains(m.Warnings, "license 2") {
		t.Fatalf("warnings = %v, want one naming the license that was skipped", m.Warnings)
	}
}

// An amount that names no currency takes the deployment's.
func TestAmountWithoutACurrencyTakesTheDefault(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><payment type="purchase"><amount>1.00</amount></payment></license>`)
	if got := m.Terms[0].GetPricing().GetCurrency(); got != "EUR" {
		t.Fatalf("currency = %q, want the default EUR", got)
	}
}

func TestDeclaredCurrencyWins(t *testing.T) {
	t.Parallel()
	m := mapFirst(t, `<license><payment type="purchase"><amount currency="usd">1.00</amount></payment></license>`)
	if got := m.Terms[0].GetPricing().GetCurrency(); got != "USD" {
		t.Fatalf("currency = %q, want the declared USD, upper cased", got)
	}
}

// A price is carried in the protocol's canonical money form rather than the
// spelling the document happened to use, so two documents stating the same
// price produce the same term.
func TestAmountIsCanonicalised(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"0.0200": "0.02",
		"1.50":   "1.5",
		"007":    "7",
		"3":      "3",
	}
	for written, want := range cases {
		t.Run(written, func(t *testing.T) {
			t.Parallel()
			m := mapFirst(t, `<license><payment type="purchase"><amount currency="EUR">`+written+`</amount></payment></license>`)
			if len(m.Terms) != 1 {
				t.Fatalf("terms = %d, want 1", len(m.Terms))
			}
			if got := m.Terms[0].GetPricing().GetRate(); got != want {
				t.Fatalf("rate = %q, want %q", got, want)
			}
			assertAccepted(t, m.Terms)
		})
	}
}
