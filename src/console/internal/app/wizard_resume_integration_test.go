//go:build integration

package app_test

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Finishing the setup of a domain saved and not verified: the domain list and
// page link to it, and the setup page reopens the wizard with the domain's
// saved answers, on the step its setup stands on -- the licensing step while it
// has no terms, the last step after that. Every leg goes through the console's
// routes, the real Exchange behind them and the Exchange's signed reads.

// TestConsole_TheSetupReopensWhereTheDomainStands: a domain saved on the first
// step reopens on the licensing step, and once its terms are saved, on the last
// one. Nothing is asked of the site to decide it: what the Exchange holds says.
func TestConsole_TheSetupReopensWhereTheDomainStands(t *testing.T) {
	var probes atomic.Int32
	f := startConsole(t, verifiedClaims(), withPages(countedProbes(deployedPackage(), &probes)))
	f.signIn(t)
	f.walkToTerms(t, "stoa.example")
	asked := probes.Load()

	if boot := f.bootOn(t, "/domains/stoa.example/setup"); boot.Resume == nil || boot.Resume.Step != "terms" {
		t.Fatalf("a domain with no terms reopens with %+v; want the licensing step", boot.Resume)
	}
	f.setTerms(t, "stoa.example", map[string]any{"terms": addDomainTerms()})
	if boot := f.bootOn(t, "/domains/stoa.example/setup"); boot.Resume == nil || boot.Resume.Step != "connect" {
		t.Fatalf("a domain with terms reopens with %+v; want the last step", boot.Resume)
	}
	if n := probes.Load() - asked; n != 0 {
		t.Fatalf("reopening the setup asked the site %d times", n)
	}
}

// TestConsole_FinishingLaterVerifiesWithoutTypingTheDomainAgain: a domain whose
// package was not installed when the publisher left is finished later from the
// setup page, and becomes verified without the domain being typed again.
//
// The round trip runs from the saved listing, through the setup page's own
// data -- the domain, its network and paths, the price it was saved at and the
// import's answer -- through the last step's check, back to the listing, which
// then holds what it was saved with. The list shows it verified, with no setup
// left to finish.
func TestConsole_FinishingLaterVerifiesWithoutTypingTheDomainAgain(t *testing.T) {
	var up atomic.Bool
	f := startConsole(t, verifiedClaims(), withPages(switchablePackage(&up)))
	f.signIn(t)
	if out := addDomainAnswering(t, f, "stoa.example", map[string]any{
		"terms": addDomainTerms(), "import_replaces_manual_terms": true,
	}); out["verified"] != false {
		t.Fatalf("the last step with no package answered %v; want saved, not verified", out)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), `href="/domains/stoa.example/setup"`)
	mustContain(t, "the domain page", f.get2(t, "/domains/stoa.example"), `href="/domains/stoa.example/setup"`)

	boot := f.bootOn(t, "/domains/stoa.example/setup")
	requireReopenedAsSaved(t, boot)

	up.Store(true)
	requireVerified(t, f.check(t, boot.Resume.Domain))
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

// requireReopenedAsSaved fails the test unless the setup page opens on the last
// step with everything stoa.example was saved with: its network and paths, the
// import's answer, and the price.
func requireReopenedAsSaved(t *testing.T, boot wizardBoot) {
	t.Helper()
	if boot.Resume == nil || boot.Resume.Domain != "stoa.example" || boot.Resume.CDN != "cloudflare" ||
		boot.Resume.Paths != "/articles/" || !boot.Resume.TermsSet || !boot.Resume.ImportReplacesManualTerms ||
		boot.Resume.Step != "connect" {
		t.Fatalf("the setup page opens with %+v; want stoa.example's saved answers, on the last step", boot.Resume)
	}
	// The licensing step, if the publisher goes back to it, opens on the price
	// the domain was saved at, not on the free terms a new domain opens on.
	if boot.Terms["pricing_model"] != "per_unit" || boot.Terms["rate"] != "0.02" {
		t.Fatalf("the setup page opens on terms %v; want the per-fetch price stoa.example was saved at", boot.Terms)
	}
}

// TestConsole_TheSetupOfAnotherPublishersDomainIsNotFound: the setup page
// answers not-found for a domain somebody else saved, the answer a domain
// nobody holds gets, and the site is not asked anything.
func TestConsole_TheSetupOfAnotherPublishersDomainIsNotFound(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomain(t, f.otherPerson(t, otherClaims()))
	f.registerBusiness(t)
	fetched := f.exchange.Manifests.Calls()

	if status, _ := f.get(t, "/domains/stoa.example/setup"); status != http.StatusNotFound {
		t.Fatalf("the setup page of another publisher's domain = %d, want 404", status)
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
