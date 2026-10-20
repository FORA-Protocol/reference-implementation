//go:build integration

package app_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Finishing the setup of a domain saved and not verified: the domain list and
// page link to it, the setup page reopens the wizard with the domain's saved
// answers, the progress route says which step to reopen on by asking the site
// what it answers now, and the last button then saves the terms chosen and
// verifies the domain. Every leg goes through the console's routes, the real
// Exchange behind them and the Exchange's signed reads.

// TestConsole_ProgressFollowsTheSiteAsItIsFixed: the step the wizard reopens on
// is the first one the site does not pass yet -- the file, then the package,
// then, once both answer, the licensing step.
func TestConsole_ProgressFollowsTheSiteAsItIsFixed(t *testing.T) {
	var up atomic.Bool
	f := startConsole(t, verifiedClaims(), withPages(switchablePackage(&up)))
	f.signIn(t)
	addDomain(t, f)
	f.exchange.Manifests.Withdraw("stoa.example")

	for _, tc := range []struct {
		fix  func()
		want string
	}{
		{func() {}, "document"},
		{func() { f.publish(t) }, "package"},
		{func() { up.Store(true) }, "terms"},
	} {
		tc.fix()
		if at := f.ok(t, "/api/domain/progress", map[string]any{"domain": "stoa.example"}); at["step"] != tc.want {
			t.Fatalf("progress = %v; want the %s step", at, tc.want)
		}
	}
}

// TestConsole_FinishingLaterVerifiesWithoutTypingTheDomainAgain is the ticket's
// third acceptance: a domain saved while its package was not installed is
// finished later from the setup page and becomes verified without the domain
// being typed again.
//
// The round trip runs from the saved listing, through the setup page's own
// data -- the domain, its network and paths, the price it was saved at and the
// import's answer -- through the last step's save, back to the listing, which
// then holds what it was saved with. The list shows it verified, with no setup
// left to finish.
func TestConsole_FinishingLaterVerifiesWithoutTypingTheDomainAgain(t *testing.T) {
	var up atomic.Bool
	f := startConsole(t, verifiedClaims(), withPages(switchablePackage(&up)))
	f.signIn(t)
	if out := addDomainAnswering(t, f, "stoa.example", map[string]any{
		"terms": addDomainTerms(), "import_replaces_manual_terms": true,
	}); out["verified"] != false {
		t.Fatalf("the last button with no package answered %v; want saved, not verified", out)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), `href="/domains/stoa.example/setup"`)
	mustContain(t, "the domain page", f.get2(t, "/domains/stoa.example"), `href="/domains/stoa.example/setup"`)

	boot := f.bootOn(t, "/domains/stoa.example/setup")
	if boot.Resume == nil || boot.Resume.Domain != "stoa.example" || boot.Resume.CDN != "cloudflare" ||
		boot.Resume.Paths != "/articles/" || !boot.Resume.TermsSet || !boot.Resume.ImportReplacesManualTerms {
		t.Fatalf("the setup page opens with %+v; want stoa.example's saved answers", boot.Resume)
	}
	// The licensing step opens on the price the domain was saved at, not on the
	// free terms a new domain opens on.
	if boot.Terms["pricing_model"] != "per_unit" || boot.Terms["rate"] != "0.02" {
		t.Fatalf("the setup page opens on terms %v; want the per-access price stoa.example was saved at", boot.Terms)
	}

	up.Store(true)
	requireVerified(t, f.ok(t, "/api/domain", map[string]any{
		"domain": boot.Resume.Domain, "cdn": boot.Resume.CDN, "content_paths": boot.Resume.Paths,
		"sitemap_url": "", "terms": boot.Terms,
		"import_replaces_manual_terms": boot.Resume.ImportReplacesManualTerms,
	}))
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Terms == nil || sites[0].Terms.PricingModel != "per_unit" ||
		sites[0].Terms.Rate != "0.02" || !sites[0].ImportReplacesManualTerms {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified with the price and the answer it was saved with", sites)
	}
	list := f.get2(t, "/domains")
	mustContain(t, "the domains list", list, "Verified")
	mustNotContain(t, "the domains list", list, "Not verified", "/setup")
	mustNotContain(t, "the domain page", f.get2(t, "/domains/stoa.example"), "/setup")
}

// TestConsole_FinishingASavedDomainSetsTheTermsChosenNow: the last button on a
// domain already saved and not verified saves the terms chosen this time over
// the ones it was saved with, keeping the domain otherwise as it was saved. A
// sitemap the licensing step did not find this time leaves the saved one.
func TestConsole_FinishingASavedDomainSetsTheTermsChosenNow(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomainAnswering(t, f, "stoa.example", map[string]any{"terms": addDomainTerms(), "sitemap_url": stoaSitemap})
	f.ok(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
		"sitemap_url": "", "terms": map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	})
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Terms == nil || sites[0].Terms.PricingModel != "free" {
		t.Fatalf("the Exchange holds %+v; want stoa.example with the free terms chosen last", sites)
	}
	if got := sites[0].ContentPathScope; len(got) != 1 || got[0] != "/articles/" {
		t.Fatalf("the saved paths became %v; want /articles/ as saved", got)
	}
	if sites[0].SitemapURL != stoaSitemap {
		t.Fatalf("the saved sitemap became %q; want %s, kept when none is found", sites[0].SitemapURL, stoaSitemap)
	}
}

// TestConsole_TheSetupOfAnotherPublishersDomainIsNotFound: the setup page and
// the progress route answer not-found for a domain somebody else saved, the
// answer a domain nobody holds gets, and the site is not asked anything.
func TestConsole_TheSetupOfAnotherPublishersDomainIsNotFound(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f.otherPerson(t, otherClaims()))
	f.registerBusiness(t)
	fetched := f.exchange.Manifests.Calls()

	if status, _ := f.get(t, "/domains/stoa.example/setup"); status != http.StatusNotFound {
		t.Fatalf("the setup page of another publisher's domain = %d, want 404", status)
	}
	if status, out := f.api(t, "/api/domain/progress", map[string]any{"domain": "stoa.example"}); status != http.StatusNotFound {
		t.Fatalf("the progress of another publisher's domain = %d %v, want 404", status, out)
	}
	if n := f.exchange.Manifests.Calls() - fetched; n != 0 {
		t.Fatalf("asking about another publisher's domain read its document %d times", n)
	}
}

// TestConsole_TheSetupOfAVerifiedDomainOpensTheDomain: a verified domain has no
// setup left to finish, so its setup page sends the publisher to the domain.
func TestConsole_TheSetupOfAVerifiedDomainOpensTheDomain(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	requireVerified(t, addDomain(t, f))

	resp := f.do(t, http.MethodGet, "/domains/stoa.example/setup", nil)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/domains/stoa.example" {
		t.Fatalf("the setup page of a verified domain ended at %s with %d, want the domain's page",
			resp.Request.URL.Path, resp.StatusCode)
	}
}

// TestConsole_LookupOfADomainAlreadyYoursAsksNothing: typing a domain the
// publisher already saved answers that it is theirs, with the page that
// finishes its setup, and asks nothing about it: no network is worked out and
// its document is not read.
func TestConsole_LookupOfADomainAlreadyYoursAsksNothing(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f)
	fetched := f.exchange.Manifests.Calls()

	out := f.ok(t, "/api/domain/lookup", map[string]any{"domain": "Stoa.Example"})
	if out["yours"] != true || out["verified"] != false || out["link"] != "/domains/stoa.example/setup" {
		t.Fatalf("looking up a domain already saved answered %v; want it named as yours, with its setup page", out)
	}
	if provider, _ := out["provider"].(string); provider != "" || !strings.EqualFold(out["domain"].(string), "stoa.example") {
		t.Fatalf("the lookup worked out a network for a domain already saved: %v", out)
	}
	if n := f.exchange.Manifests.Calls() - fetched; n != 0 {
		t.Fatalf("looking up a domain already saved read its document %d times", n)
	}
}
