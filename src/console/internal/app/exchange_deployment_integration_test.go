//go:build integration

package app_test

import (
	"net/http"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/termspreset"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// What the console takes from the Exchange's deployment rather than from its
// own: the denomination terms are priced in, and whose fault a refusal is.

// TestConsole_PricesInTheExchangesDenomination covers the console having no
// currency of its own. The Exchange stores every agreed arrangement in its
// denomination, so the wizard opens on it and the preview a publisher agrees to
// is priced in it. A console configured apart from the Exchange could preview
// one currency while the Exchange stored another.
func TestConsole_PricesInTheExchangesDenomination(t *testing.T) {
	f := startConsoleOver(t, verifiedClaims(), func(o *exchangetest.Options) {
		o.TermsDefaults = termspreset.Defaults{Currency: "USD", Unit: "fetches"}
	})
	f.signIn(t)
	boot := f.openWizard(t)
	if boot.Currency != "USD" || boot.Unit != "fetches" {
		t.Fatalf("the wizard opens on %q per %q, want the Exchange's USD per fetches", boot.Currency, boot.Unit)
	}
	f.walkToDocument(t, "stoa.example")

	out := f.ok(t, "/api/domain/terms-preview", map[string]any{
		"terms": map[string]any{
			"pricing_model": "per_unit", "rate": "0.05",
			"permitted_functions": []string{"search"},
		},
	})
	offer := offerText(t, out)
	if !strings.Contains(offer, "0.05 USD per fetches") {
		t.Fatalf("the preview is not priced in the Exchange's denomination: %s", offer)
	}
	if strings.Contains(offer, "EUR") {
		t.Fatalf("the preview carries a currency the Exchange does not price in: %s", offer)
	}
}

// TestConsole_TakesTheBlameWhenTheExchangeHasNoSeat covers an Exchange whose
// operator never seeded its seat. No registration can succeed, and nothing the
// publisher changes would help, so they are told the problem is on our side and
// are never shown the Exchange's instruction to its operator.
func TestConsole_TakesTheBlameWhenTheExchangeHasNoSeat(t *testing.T) {
	f := startConsoleOver(t, verifiedClaims(), func(o *exchangetest.Options) { o.WithoutSeat = true })

	// Signing in lands on the home page, which lists the person's
	// registrations: the first call that needs the seat.
	status, body := f.get(t, "/login")
	if status != http.StatusInternalServerError {
		t.Fatalf("the home page with no seat = %d, want %d", status, http.StatusInternalServerError)
	}
	if !strings.Contains(body, "on our side") {
		t.Fatalf("the page does not say the fault is ours: %s", head(body))
	}
	assertNoOperatorText(t, body)

	// The wizard opens on the same read, so it cannot hand out a form either.
	status, body = f.get(t, "/domains/new")
	if status != http.StatusInternalServerError {
		t.Fatalf("the wizard with no seat = %d, want %d", status, http.StatusInternalServerError)
	}
	assertNoOperatorText(t, body)
}

// TestConsole_ShowsTermsRefusalsWithoutInternalText covers a price that is not
// a number. The publisher reads what is wrong with the price they typed, and
// nothing of the shared terms package's own error text: no kind token, no
// package prefix.
func TestConsole_ShowsTermsRefusalsWithoutInternalText(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	terms := map[string]any{
		"pricing_model": "per_unit", "rate": "a lot",
		"permitted_functions": []string{"search"},
	}

	preview := f.ok(t, "/api/domain/terms-preview", map[string]any{"terms": terms})
	warning, _ := preview["warning"].(string)
	status, out := f.api(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "",
		"sitemap_url": "", "terms": terms,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("finishing on a price that is not a number = %d", status)
	}
	refusal, _ := out["error"].(string)
	for where, text := range map[string]string{"preview": warning, "register": refusal} {
		if !strings.Contains(text, `rate "a lot" is not a decimal amount`) {
			t.Fatalf("the %s does not say what is wrong with the price: %q", where, text)
		}
		for _, internal := range []string{"invalid_request", "onboarding", "publicerr"} {
			if strings.Contains(text, internal) {
				t.Fatalf("the %s repeats internal text %q: %q", where, internal, text)
			}
		}
	}
	f.assertNoDomains(t, "a price that is not a number")
}

// assertNoOperatorText fails when a publisher-facing text carries what the
// Exchange wrote for its operator about the missing seat.
func assertNoOperatorText(t *testing.T, text string) {
	t.Helper()
	for _, operator := range []string{"seat", "seed", testExchangeDomain} {
		if strings.Contains(text, operator) {
			t.Fatalf("the publisher is shown the operator's text (%q): %s", operator, head(text))
		}
	}
}
