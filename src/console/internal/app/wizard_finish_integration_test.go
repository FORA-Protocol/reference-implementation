//go:build integration

package app_test

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/exchangetest"
)

// The last button. It saves the domain whatever its checks say, and verifies it
// when both pass at that moment: the package check, which the console runs on
// its own server, and then the Exchange's own read of the discovery document.
// Every leg goes through the console's routes, the real Exchange behind them
// and the Exchange's signed reads.

// TestConsole_FinishVerifiesWhenFileAndPackageCheckOut: a publisher whose file
// is published and whose package answers ends the wizard with the domain
// verified -- on the Exchange, in their list of domains and on the domain's own
// page.
func TestConsole_FinishVerifiesWhenFileAndPackageCheckOut(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	requireVerified(t, addDomain(t, f))

	if sites := f.mine(t).Sites; len(sites) != 1 || !sites[0].Active() {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified", sites)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), "stoa.example", "Verified")
	mustNotContain(t, "the domains list", f.get2(t, "/domains"), "Not verified")
	mustContain(t, "the domain page", f.get2(t, "/domains/stoa.example"), "Verified")
}

// TestConsole_ThePackageCheckComesBeforeTheExchangeIsAskedToActivate: with the
// file published but no package in front of the site, the last button saves
// the domain and does not ask the Exchange to verify it -- the Exchange records
// no check at all. Once the package answers, the same button verifies it.
func TestConsole_ThePackageCheckComesBeforeTheExchangeIsAskedToActivate(t *testing.T) {
	var up atomic.Bool
	f := startConsole(t, verifiedClaims(), withPages(switchablePackage(&up)))
	f.signIn(t)

	if out := addDomain(t, f); out["verified"] != false {
		t.Fatalf("the last button with no package answered %v; want saved, not verified", out)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].LastCheckedAt != nil {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved, never checked", sites)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), "stoa.example", "Not verified")

	up.Store(true)
	requireVerified(t, addDomain(t, f))
	if sites := f.mine(t).Sites; len(sites) != 1 || !sites[0].Active() {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified once its package answers", sites)
	}
}

// TestConsole_ANotVerifiedDomainIsNotSoldUntilFinished: a domain saved and not
// verified sells nothing. The operator's catalog contributor cannot list a
// page of it, although the site already serves the document the wizard handed
// over; once the package answers and the publisher presses the last button
// again, the same page is listed and bought.
func TestConsole_ANotVerifiedDomainIsNotSoldUntilFinished(t *testing.T) {
	var up atomic.Bool
	f := startConsoleOver(t, verifiedClaims(), sellingOver(func(*exchangetest.Options) {}),
		withPages(switchablePackage(&up)))
	f.signIn(t)
	if out := addDomain(t, f); out["verified"] != false {
		t.Fatalf("the last button with no package answered %v; want saved, not verified", out)
	}

	term := exchangetest.PerUnitTerm("0.05", 20)
	err := f.exchange.PushAsContributor(t, "stoa.example", "/articles/held", term)
	if err == nil || !strings.Contains(err.Error(), "publisher_domain_listing_not_active") {
		t.Fatalf("listing a page of a domain that is not verified = %v; want it refused", err)
	}

	up.Store(true)
	requireVerified(t, addDomain(t, f))
	if sale := f.exchange.Buyer(t).Buy(t, "stoa.example", "/articles/held", term); sale.TransactionID == "" {
		t.Fatal("the verified domain's page sold with no transaction id")
	}
}

// TestConsole_AFailedCheckAfterTheSaveStillReportsItSaved: when the Exchange
// fails while verifying -- here its read of the site fails in a way that says
// nothing about the publisher's document -- the domain stays saved, and the
// last button says so rather than reporting that nothing was changed.
func TestConsole_AFailedCheckAfterTheSaveStillReportsItSaved(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	f.publish(t)
	if out := f.ok(t, "/api/domain/check-manifest", map[string]any{"domain": "stoa.example"}); out["passed"] != true {
		t.Fatalf("the wizard's own check did not pass: %v", out)
	}
	f.exchange.Manifests.Fail("stoa.example", errors.New("the resolver went away"))

	out := f.ok(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "",
		"sitemap_url": "", "terms": addDomainTerms(),
	})
	if out["domain"] != "stoa.example" || out["verified"] != false {
		t.Fatalf("the last button answered %v; want stoa.example saved, not verified", out)
	}
	if sites := f.mine(t).Sites; len(sites) != 1 || sites[0].Active() {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved, not verified", sites)
	}
}

// TestConsole_SkippingTheTermsOfASavedDomainAgainChangesNothing: skipping the
// licensing step again on a domain already saved and not verified saves
// nothing new. The domain keeps the terms, the sitemap and the import's answer
// it was saved with, and stays not verified.
func TestConsole_SkippingTheTermsOfASavedDomainAgainChangesNothing(t *testing.T) {
	f := startConsole(t, verifiedClaims())
	f.signIn(t)
	addDomainAnswering(t, f, "stoa.example", map[string]any{"terms": addDomainTerms(), "sitemap_url": stoaSitemap})

	out := f.ok(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
		"sitemap_url": "", "terms": nil, "import_replaces_manual_terms": true,
	})
	if out["verified"] != false {
		t.Fatalf("skipping the terms again answered %v; want the domain saved, not verified", out)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].Terms == nil || sites[0].Terms.PricingModel != "per_unit" ||
		sites[0].SitemapURL != stoaSitemap || sites[0].ImportReplacesManualTerms {
		t.Fatalf("the Exchange holds %+v; want stoa.example as it was saved", sites)
	}
}

// TestConsole_TheLastStepOnAVerifiedDomainChangesNothing: a verified domain is
// sold under its terms, so the last step on it again changes none of them and
// answers that it is verified.
func TestConsole_TheLastStepOnAVerifiedDomainChangesNothing(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(deployedPackage()))
	f.signIn(t)
	requireVerified(t, addDomain(t, f))

	requireVerified(t, f.ok(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
		"sitemap_url": "", "terms": map[string]any{"pricing_model": "free", "permitted_functions": []string{"search"}},
	}))
	sites := f.mine(t).Sites
	if len(sites) != 1 || !sites[0].Active() || sites[0].Terms == nil || sites[0].Terms.PricingModel != "per_unit" {
		t.Fatalf("the Exchange holds %+v; want stoa.example verified under the terms it was sold under", sites)
	}
}
