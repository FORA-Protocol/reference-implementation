//go:build integration

package app_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
)

// The wizard's last step and the button that ends it. Up to here nothing about
// the domain has been written; this is where that changes, and only if the
// Exchange reads the published document one more time and still agrees.

// TestConsole_ShowsTheOfferBeforeAnythingIsRegistered is the step the licensing
// page exists for: a publisher sees what an agent would be offered, at the
// price they set, before they commit to anything.
//
// Two properties matter. The offer follows the controls, and asking for it
// registers nothing -- a preview that registered would make every experiment
// permanent.
func TestConsole_ShowsTheOfferBeforeAnythingIsRegistered(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(stubPageReader{
		page: pagefetch.Page{
			Title: "On rivers", CanonicalURL: "https://stoa.example/articles/on-rivers",
		},
	}))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	page := f.ok(t, "/api/domain/page", map[string]any{"domain": "stoa.example"})

	out := f.ok(t, "/api/domain/terms-preview", map[string]any{
		"page_title": page["page_title"], "page_url": page["page_url"],
		"terms": map[string]any{
			"pricing_model": "per_unit", "rate": "0.05",
			"permitted_functions": []string{"search"}, "attribution_required": true,
		},
	})
	offer := offerText(t, out)
	if !strings.Contains(offer, "0.05") {
		t.Fatalf("the offer does not follow the price that was set: %s", offer)
	}
	// The term permits search, so it allows nothing else, and the offer
	// says so in the words every term in the console is said in.
	if !strings.Contains(offer, "allowed only: search") {
		t.Fatalf("the offer does not say which uses are refused: %s", offer)
	}
	if !strings.Contains(offer, "attribution required") {
		t.Fatalf("the offer does not carry the attribution answer: %s", offer)
	}
	if !strings.Contains(offer, "On rivers") {
		t.Fatalf("the offer is not about the publisher's own page: %s", offer)
	}
	f.assertNoDomains(t, "previewing the offer")
}

// TestConsole_RefusesTermsItCannotOffer covers the answer that produces
// nothing: charging with no price. The publisher is told where they set it, and
// nothing is registered.
func TestConsole_RefusesTermsItCannotOffer(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	preview := f.ok(t, "/api/domain/terms-preview", map[string]any{
		"terms": map[string]any{
			"pricing_model": "per_unit", "rate": "",
			"permitted_functions": []string{"search"},
		},
	})
	if warning, _ := preview["warning"].(string); !strings.Contains(warning, "cannot be offered") {
		t.Fatalf("the preview did not refuse charging with no price: %v", preview)
	}

	status, out := f.api(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "",
		"sitemap_url": "",
		"terms": map[string]any{
			"pricing_model": "per_unit", "rate": "",
			"permitted_functions": []string{"search"},
		},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("finishing on terms that cannot be offered = %d", status)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "cannot be offered") {
		t.Fatalf("the refusal does not name what is wrong: %v", out)
	}
	f.assertNoDomains(t, "terms that cannot be offered")
}

// TestConsole_FindsTheSitemapRatherThanAskingForIt pins that the publisher is
// never asked where their pages are listed.
//
// Finding a page to preview means reading the sitemap anyway, so the address is
// already in hand; asking for it would be asking for something we have, and one
// more field to get wrong. What matters is that it reaches the registration,
// because the catalogue import is what reads it.
func TestConsole_FindsTheSitemapRatherThanAskingForIt(t *testing.T) {
	const found = "https://stoa.example/sitemap.xml"
	f := startConsole(t, verifiedClaims(), withPages(stubPageReader{
		page:    pagefetch.Page{CanonicalURL: "https://stoa.example/articles/on-rivers"},
		sitemap: found,
	}))
	f.signIn(t)
	addDomain(t, f)

	sites := f.mine(t).Sites
	if len(sites) != 1 {
		t.Fatalf("the Exchange holds %d domains, want 1", len(sites))
	}
	if sites[0].SitemapURL != found {
		t.Fatalf("the Exchange holds sitemap %q, want the one discovery found", sites[0].SitemapURL)
	}
}

// TestConsole_LicensingIsWhatTheDomainIsRegisteredWith closes the loop: what
// the publisher set on the last step is what the Exchange is told.
func TestConsole_LicensingIsWhatTheDomainIsRegisteredWith(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f)

	sites := f.mine(t).Sites
	if len(sites) != 1 {
		t.Fatalf("the Exchange holds %d domains, want 1", len(sites))
	}
	got := sites[0].Terms
	if got.PricingModel != "per_unit" || got.Rate != "0.02" {
		t.Fatalf("the domain was registered with %+v, want the arrangement that was set", got)
	}
	if !slices.Contains(got.PermittedFunctions, "search") ||
		!slices.Contains(got.PermittedFunctions, "ai-input") {
		t.Fatalf("the domain was registered permitting %v", got.PermittedFunctions)
	}
}

// TestConsole_AFreeAnswerCarriesNoPrice covers the rate the wizard keeps in the
// browser after the publisher switches to free, so switching back restores it.
// That leftover is not a price anybody is asking for: the preview must not
// refuse it and the domain must not be registered with it.
func TestConsole_AFreeAnswerCarriesNoPrice(t *testing.T) {
	free := map[string]any{
		"pricing_model": "free", "rate": "0.05",
		"permitted_functions": []string{"search"},
	}
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	preview := f.ok(t, "/api/domain/terms-preview", map[string]any{"terms": free})
	if warning, _ := preview["warning"].(string); warning != "" {
		t.Fatalf("the preview refused a free answer: %q", warning)
	}

	addDomainWith(t, f, "stoa.example", free)
	sites := f.mine(t).Sites
	if len(sites) != 1 {
		t.Fatalf("the Exchange holds %d domains, want 1", len(sites))
	}
	if got := sites[0].Terms; got.PricingModel != "free" || got.Rate != "" {
		t.Fatalf("the domain was registered with %+v, want free and no rate", got)
	}
}

// TestConsole_FillsTheTermsFromTheSitesOwnLicence covers the publisher who has
// already stated their terms and should not have to state them twice.
//
// The step is still shown. A form filled in from a file we read is not the same
// as a price somebody agreed, and the price is theirs to agree.
func TestConsole_FillsTheTermsFromTheSitesOwnLicence(t *testing.T) {
	const licence = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="https://stoa.example/articles/">
    <license>
      <permits type="usage">search</permits>
      <prohibits type="usage">train-ai</prohibits>
      <payment type="free"></payment>
    </license>
  </content>
</rsl>`
	f := startConsole(t, verifiedClaims(), withPages(stubPageReader{
		page: pagefetch.Page{
			Title: "On rivers", CanonicalURL: "https://stoa.example/articles/on-rivers",
		},
		license: licence,
	}))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	out := f.ok(t, "/api/domain/page", map[string]any{"domain": "stoa.example"})
	terms, _ := out["terms"].(map[string]any)
	if terms == nil {
		t.Fatalf("the licence the site publishes was not read: %v", out)
	}
	if terms["pricing_model"] != "free" {
		t.Fatalf("the prefill priced the terms %v, want what the licence says", terms["pricing_model"])
	}
	note, _ := out["terms_note"].(string)
	if !strings.Contains(note, "rsl.txt") {
		t.Fatalf("the publisher is not told where the answers came from: %q", note)
	}
}

// TestConsole_SaysWhenALicenceSaysMoreThanThisPageCanSet is the other half, and
// the one worth being careful about.
//
// A licensing document can restrict by who the reader is or where they are;
// this page sets a price, a list of uses and whether attribution is required.
// Quietly dropping the rest would show the publisher a form permitting more
// than their own document does, and they would read it as their terms and press
// the button. So nothing is filled in and the note says why.
func TestConsole_SaysWhenALicenceSaysMoreThanThisPageCanSet(t *testing.T) {
	const licence = `<rsl xmlns="https://rslstandard.org/rsl">
  <content url="https://stoa.example/articles/">
    <license>
      <permits type="user">commercial</permits>
      <payment type="free"></payment>
    </license>
  </content>
</rsl>`
	f := startConsole(t, verifiedClaims(), withPages(stubPageReader{
		page: pagefetch.Page{
			Title: "On rivers", CanonicalURL: "https://stoa.example/articles/on-rivers",
		},
		license: licence,
	}))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")

	out := f.ok(t, "/api/domain/page", map[string]any{"domain": "stoa.example"})
	if out["terms"] != nil {
		t.Fatalf("a licence this page cannot express was filled in anyway: %v", out["terms"])
	}
	note, _ := out["terms_note"].(string)
	if !strings.Contains(note, "more than") {
		t.Fatalf("the publisher is not told why the form is empty: %q", note)
	}
}

// TestConsole_SavesAWithdrawnFileAsNotVerified: the last button saves the
// domain whatever its checks say, and verifies it only if they pass then.
//
// Every check before the last button is advice. The Exchange reads the
// published document again when it is asked to verify the domain, so a domain
// whose document passed at the file step and was taken down since is saved and
// listed as not verified -- nothing the publisher entered is lost -- and it is
// not verified. The package answers, so the console does ask; the Exchange's
// own read of the site is what refuses, and it records that it looked.
func TestConsole_SavesAWithdrawnFileAsNotVerified(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	f.publish(t)
	if out := f.ok(t, "/api/domain/check-manifest", map[string]any{"domain": "stoa.example"}); out["passed"] != true {
		t.Fatalf("the wizard's own check did not pass: %v", out)
	}
	// The document passed the wizard's check and is gone by the last button.
	f.exchange.Manifests.Withdraw("stoa.example")

	out := f.ok(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "",
		"sitemap_url": "",
		"terms": map[string]any{
			"pricing_model": "free", "permitted_functions": []string{"search"},
		},
	})
	if out["domain"] != "stoa.example" || out["verified"] != false {
		t.Fatalf("the last button on a withdrawn file answered %v; want stoa.example saved, not verified", out)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].LastCheckedAt == nil {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved, checked once and not verified", sites)
	}
	status, list := f.get(t, "/domains")
	if status != http.StatusOK {
		t.Fatalf("the domains list = %d", status)
	}
	mustContain(t, "the domains list", list, "stoa.example", "Not verified")
}

// offerText flattens the offer preview into something a test can read.
func offerText(t *testing.T, out map[string]any) string {
	t.Helper()
	lines, _ := out["lines"].([]any)
	if len(lines) == 0 {
		t.Fatalf("the preview produced no offer: %v", out)
	}
	var sb strings.Builder
	for _, raw := range lines {
		line, _ := raw.(map[string]any)
		label, _ := line["label"].(string)
		value, _ := line["value"].(string)
		sb.WriteString(label + ": " + value + "\n")
	}
	return sb.String()
}
