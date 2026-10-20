//go:build integration

package app_test

import (
	"net/http"
	"sync/atomic"
	"testing"
)

// Skip for now, as the console's routes see it. Skipping the domain step, the
// file step and the package step happens in the page and sends nothing: the
// wizard script's own tests cover those moves. Skipping the licensing step sends
// the last step's save with no terms, which is what these tests drive.
//
// Legs: every save goes through the console's route to the real Exchange, and
// is read back through the Exchange's signed registrations route and through
// the console's own pages.

// TestConsole_SkippingTheTermsSavesTheDomainNotVerified covers the second and
// third acceptance of the ticket on the licensing step. A domain whose file and
// package both answer is saved with that step skipped: it has no terms and is
// listed as not verified. The console neither asks the site about its package
// nor asks the Exchange to verify it, because a domain with no terms cannot be
// verified. The import's answer is not saved without terms. The domain's page
// says its terms are not set, and its files still download. Its setup reopens
// on the licensing step, and finishing there verifies it without the domain
// being typed again.
func TestConsole_SkippingTheTermsSavesTheDomainNotVerified(t *testing.T) {
	var probes atomic.Int32
	f := startConsole(t, verifiedClaims(), withPages(countedProbes(deployedPackage(), &probes)))
	f.signIn(t)
	walked := f.walkToLastStep(t, "stoa.example")
	asked := probes.Load()
	out := f.pressLastStep(t, walked, map[string]any{"terms": nil, "import_replaces_manual_terms": true})
	if out["verified"] != false {
		t.Fatalf("saving with no terms answered %v; want saved, not verified", out)
	}
	if n := probes.Load() - asked; n != 0 {
		t.Fatalf("saving with no terms asked the site about its package %d times; want none", n)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].Terms != nil || sites[0].LastCheckedAt != nil ||
		sites[0].ImportReplacesManualTerms {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved with no terms and no import answer, never checked",
			sites)
	}

	page := f.get2(t, "/domains/stoa.example")
	mustContain(t, "the domain page", page, "Not verified", "Not set yet", `href="/domains/stoa.example/setup"`)
	links := rePageLink.FindAllStringSubmatch(page, -1)
	if len(links) == 0 {
		t.Fatalf("the domain page offers no files: %s", head(page))
	}
	for _, m := range links {
		if status, _ := f.get(t, m[1]); status != http.StatusOK {
			t.Fatalf("downloading %s = %d", m[1], status)
		}
	}

	if at := f.ok(t, "/api/domain/progress", map[string]any{"domain": "stoa.example"}); at["step"] != "terms" {
		t.Fatalf("progress = %v; want the licensing step", at)
	}
	if boot := f.bootOn(t, "/domains/stoa.example/setup"); boot.Resume == nil || boot.Resume.TermsSet ||
		boot.Resume.ImportReplacesManualTerms {
		t.Fatalf("the setup page opens with %+v; want no terms and no import answer", boot.Resume)
	}
	requireVerified(t, f.pressLastStep(t, walked, map[string]any{"terms": addDomainTerms()}))
}

// TestConsole_SkippingTheFileAndThePackageSavesTheDomainNotVerified is the
// ticket's second acceptance: a publisher adds a domain, skips the file and
// package steps, and sees the domain listed as not verified. Neither the file
// nor the package is in place, and the domain is saved at the last step with
// its terms. My domains lists it as not verified, with its setup to finish,
// which reopens on the file step.
func TestConsole_SkippingTheFileAndThePackageSavesTheDomainNotVerified(t *testing.T) {
	f := startConsole(t, verifiedClaims(), withPages(switchablePackage(new(atomic.Bool))))
	f.signIn(t)
	f.walkToDocument(t, "stoa.example")
	// Both skips only move the page on, to the licensing step, which reads the
	// site's page before the publisher saves.
	page := f.ok(t, "/api/domain/page", map[string]any{"domain": "stoa.example"})
	out := f.ok(t, "/api/domain", map[string]any{
		"domain": "stoa.example", "cdn": "cloudflare", "content_paths": "/articles/",
		"sitemap_url": page["sitemap_url"], "terms": addDomainTerms(),
	})
	if out["verified"] != false {
		t.Fatalf("saving with no file and no package answered %v; want saved, not verified", out)
	}
	sites := f.mine(t).Sites
	if len(sites) != 1 || sites[0].Active() || sites[0].Terms == nil || sites[0].LastCheckedAt != nil {
		t.Fatalf("the Exchange holds %+v; want stoa.example saved with its terms, never checked", sites)
	}
	mustContain(t, "the domains list", f.get2(t, "/domains"), "Not verified", `href="/domains/stoa.example/setup"`)
	if at := f.ok(t, "/api/domain/progress", map[string]any{"domain": "stoa.example"}); at["step"] != "document" {
		t.Fatalf("progress = %v; want the file step", at)
	}
}
