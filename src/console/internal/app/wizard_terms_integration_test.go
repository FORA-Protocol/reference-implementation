//go:build integration

package app_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/pagefetch"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/console/internal/web"
)

// The wizard's licensing step: what the publisher's site already says about its
// terms, the offer an agent would be shown under the answers on screen, and the
// step's Next, which saves the terms on the domain the first step saved.
//
// What the Exchange holds is read back through its signed registrations route
// with the console's own client; whether a terms save was recorded is read one
// tier lower, through the Exchange's audit repository, because the Exchange
// publishes no read surface for its audit log (exchangetest.AuditActions says
// so on itself).

// TestConsole_ShowsTheOfferBeforeAnythingIsRegistered is the step the licensing
// page exists for: a publisher sees what an agent would be offered, at the
// price they set, before they commit to anything.
//
// Two properties matter. The offer follows the controls, and asking for it
// saves nothing -- a preview that saved would make every experiment permanent.
func TestConsole_ShowsTheOfferBeforeAnythingIsRegistered(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(stubPageReader{
		page: pagefetch.Page{
			Title: "On rivers", CanonicalURL: "https://stoa.example/articles/on-rivers",
		},
	}))
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")
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
	f.requireNoTerms(t, "previewing the offer")
}

// TestConsole_RefusesTermsItCannotOffer covers the answer that produces
// nothing: charging with no price. The publisher is told where they set it, on
// the step they set it on, and the domain keeps no terms.
func TestConsole_RefusesTermsItCannotOffer(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")

	preview := f.ok(t, "/api/domain/terms-preview", map[string]any{
		"terms": map[string]any{
			"pricing_model": "per_unit", "rate": "",
			"permitted_functions": []string{"search"},
		},
	})
	if warning, _ := preview["warning"].(string); !strings.Contains(warning, "cannot be offered") {
		t.Fatalf("the preview did not refuse charging with no price: %v", preview)
	}

	status, out := f.api(t, web.APITermsPath, map[string]any{
		"domain": "stoa.example",
		"terms": map[string]any{
			"pricing_model": "per_unit", "rate": "",
			"permitted_functions": []string{"search"},
		},
	})
	if status != http.StatusBadRequest {
		t.Fatalf("saving terms that cannot be offered = %d", status)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "cannot be offered") {
		t.Fatalf("the refusal does not name what is wrong: %v", out)
	}
	f.requireNoTerms(t, "terms that cannot be offered")
}

// TestConsole_TheTermsStepRefusesNoTerms: a save that sends no terms, or leaves
// them out, is refused rather than read as terms chosen -- a missing answer
// would otherwise build a free term that permits every use.
func TestConsole_TheTermsStepRefusesNoTerms(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")

	for name, body := range map[string]map[string]any{
		"terms sent as null": {"domain": "stoa.example", "terms": nil},
		"terms left out":     {"domain": "stoa.example"},
	} {
		status, out := f.api(t, web.APITermsPath, body)
		if msg, _ := out["error"].(string); status != http.StatusBadRequest || !strings.Contains(msg, "Choose your terms") {
			t.Fatalf("%s = %d %v, want 400 asking for the terms", name, status, out)
		}
	}
	f.requireNoTerms(t, "a save with no terms")
}

// TestConsole_TheTermsStepShowsTheExchangesRefusal: terms the console's own
// check accepts and the Exchange refuses are refused on the step they were set
// on, in the Exchange's own words, and nothing is saved. The sitemap is such an
// answer: the console's term builder does not look at it, the console passes
// the one the request names on as it is, and the Exchange keeps only a sitemap
// on the domain or one of its subdomains.
func TestConsole_TheTermsStepShowsTheExchangesRefusal(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")

	status, out := f.api(t, web.APITermsPath, map[string]any{
		"domain": "stoa.example", "terms": addDomainTerms(),
		"sitemap_url": "https://elsewhere.example/sitemap.xml",
	})
	if status != http.StatusBadRequest {
		t.Fatalf("saving terms the Exchange refuses = %d %v, want %d", status, out, http.StatusBadRequest)
	}
	if msg, _ := out["error"].(string); msg != "the sitemap address must be on stoa.example, not elsewhere.example" {
		t.Fatalf("the step does not show the Exchange's own refusal: %q", msg)
	}
	f.requireNoTerms(t, "terms the Exchange refused")
	if n := f.exchange.CountAuditActions(t, "SetDomainTerms"); n != 0 {
		t.Fatalf("the Exchange recorded %d terms saves; it refused the only one asked for", n)
	}
}

// TestConsole_TheTermsStepSavesSoClosingTheTabLosesNothing covers the step's
// Next, and a publisher who closes the tab right after it: the terms are saved
// on the domain with the catalog question's answer, the domain is still not
// verified, and its setup reopens on the last step with what was saved.
func TestConsole_TheTermsStepSavesSoClosingTheTabLosesNothing(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")

	out := f.setTerms(t, "stoa.example", map[string]any{
		"terms": addDomainTerms(), "import_replaces_manual_terms": true,
	})
	if out["domain"] != "stoa.example" || out["verified"] != false {
		t.Fatalf("the licensing step's Next answered %v; want stoa.example, not verified", out)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].Terms == nil || sites[0].Terms.Rate != "0.02" ||
		!sites[0].ImportReplacesManualTerms {
		t.Fatalf("the Exchange holds %+v; want stoa.example with the terms and the answer just saved", sites)
	}
	if n := f.exchange.CountAuditActions(t, "SetDomainTerms"); n != 1 {
		t.Fatalf("the Exchange recorded %d terms saves, want 1", n)
	}
	boot := f.bootOn(t, "/domains/stoa.example/setup")
	if boot.Resume == nil || boot.Resume.Step != "connect" || !boot.Resume.TermsSet ||
		!boot.Resume.ImportReplacesManualTerms || boot.Terms["rate"] != "0.02" {
		t.Fatalf("the setup page reopens with %+v and terms %v; want the last step, on what was saved",
			boot.Resume, boot.Terms)
	}
}

// TestConsole_TheTermsStepKeepsASavedSitemap: saving the terms again changes
// them to the ones chosen now and keeps the domain otherwise as it was saved.
// A sitemap the licensing step did not find this time leaves the saved one.
func TestConsole_TheTermsStepKeepsASavedSitemap(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")
	f.setTerms(t, "stoa.example", map[string]any{"terms": addDomainTerms(), "sitemap_url": stoaSitemap})

	f.ok(t, web.APITermsPath, map[string]any{
		"domain": "stoa.example", "sitemap_url": "",
		"terms": map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	})
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Terms == nil || sites[0].Terms.PricingModel != "free" {
		t.Fatalf("the Exchange holds %+v; want stoa.example with the free terms chosen last", sites)
	}
	if sites[0].SitemapURL != stoaSitemap || !slices.Equal(sites[0].ContentPathScope, []string{"/articles/"}) {
		t.Fatalf("the Exchange holds %+v; want the saved sitemap and paths kept", sites[0])
	}
}

// TestConsole_TheTermsOfAVerifiedDomainAreNotChanged: a verified domain is sold
// under its terms, so the licensing step's Next on it changes none of them,
// writes nothing, and answers that it is verified.
func TestConsole_TheTermsOfAVerifiedDomainAreNotChanged(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	requireVerified(t, addDomain(t, f))
	saved := f.exchange.CountAuditActions(t, "SetDomainTerms")

	out := f.ok(t, web.APITermsPath, map[string]any{
		"domain": "stoa.example",
		"terms":  map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	})
	if out["verified"] != true {
		t.Fatalf("the terms step on a verified domain answered %v; want it verified", out)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || !sites[0].Active() || sites[0].Terms == nil || sites[0].Terms.PricingModel != "per_unit" {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified under the terms it was sold under", sites)
	}
	if n := f.exchange.CountAuditActions(t, "SetDomainTerms") - saved; n != 0 {
		t.Fatalf("the Exchange recorded %d terms saves on a verified domain", n)
	}
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
	f.walkToTerms(t, "stoa.example")

	preview := f.ok(t, "/api/domain/terms-preview", map[string]any{"terms": free})
	if warning, _ := preview["warning"].(string); warning != "" {
		t.Fatalf("the preview refused a free answer: %q", warning)
	}

	f.setTerms(t, "stoa.example", map[string]any{"terms": free})
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
	f.walkToTerms(t, "stoa.example")

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
	f.walkToTerms(t, "stoa.example")

	out := f.ok(t, "/api/domain/page", map[string]any{"domain": "stoa.example"})
	if out["terms"] != nil {
		t.Fatalf("a licence this page cannot express was filled in anyway: %v", out["terms"])
	}
	note, _ := out["terms_note"].(string)
	if !strings.Contains(note, "more than") {
		t.Fatalf("the publisher is not told why the form is empty: %q", note)
	}
}

// requireNoTerms fails the test unless the fixture's one domain is saved with
// no terms and the Exchange recorded no terms save: after names what should
// have saved none.
func (f *consoleFixture) requireNoTerms(t *testing.T, after string) {
	t.Helper()
	if sites := f.mine(t).Sites; len(sites) != 1 || sites[0].Terms != nil {
		t.Fatalf("after %s the Exchange holds %+v; want one domain with no terms", after, sites)
	}
	if n := f.exchange.CountAuditActions(t, "SetDomainTerms"); n != 0 {
		t.Fatalf("after %s the Exchange recorded %d terms saves", after, n)
	}
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
